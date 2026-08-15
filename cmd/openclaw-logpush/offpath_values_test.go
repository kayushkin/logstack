package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// The numbers in main.go that are NOT truncation budgets.
//
// boundary_values_test.go next door pins the 80-byte dry-run cut and the 8-byte
// session-id cut. Everything else this file compares against was enumerated by
// the 186th nightly pass and scored by nothing: the chunk size, the status
// threshold, the scanner's line ceiling, the two file modes, and the author
// capture-index guard.
//
// Measured 2026-08-15 by scripts/sabotage-offpath.py: this package caught 0 of
// its 8 off-path rows before this file.
//
// The chunking is the richest of them and the one with the worst failure mode.
// The chunk size is spelled twice —
//
//	for i := 0; i < len(batch); i += 50 {
//	    end := i + 50
//
// — and the two spellings have to agree. Move only the step and one entry per
// chunk is silently dropped; move only the end and one entry per chunk is
// pushed twice. Neither is a truncation, both keep the suite green, and the
// consequence in production is missing or duplicated log rows. Nothing tested
// the chunking at all: every existing test drives processFile in DRY-RUN mode,
// which returns before the loop is reached.

// message renders one openclaw JSONL line with the given id and body.
func message(id, role, body string) string {
	block, err := json.Marshal(map[string]string{"type": "text", "text": body})
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf(
		`{"type":"message","id":%q,"timestamp":"2026-08-08T12:00:00Z",`+
			`"message":{"role":%q,"model":"claude","content":[%s]}}`,
		id, role, block,
	)
}

// sessionFile writes the given JSONL lines to a session file and returns its path.
func sessionFile(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "01234567-89ab-cdef.jsonl")
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf(reachGuard+"write session file: %v", err)
	}
	return path
}

// collector is a stand-in logstack that records each batch it was handed,
// separately rather than merged. What the chunking gets wrong is the SHAPE of
// the batches, so a collector that concatenated them would lose the very thing
// under test — and would still catch a dropped entry, which is exactly the
// half-working instrument that makes a partial score look like a full one.
type collector struct {
	server  *httptest.Server
	batches [][]string // ids, per batch, in arrival order
	status  int
}

func newCollector(t *testing.T, status int) *collector {
	t.Helper()
	c := &collector{status: status}
	c.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/logs/batch" {
			t.Errorf("push went to %q, want /api/v1/logs/batch", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf(reachGuard+"read batch body: %v", err)
			return
		}
		var entries []struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(body, &entries); err != nil {
			t.Errorf(reachGuard+"decode batch body: %v", err)
			return
		}
		ids := make([]string, len(entries))
		for i, e := range entries {
			ids[i] = e.ID
		}
		c.batches = append(c.batches, ids)
		w.WriteHeader(c.status)
	}))
	t.Cleanup(c.server.Close)
	return c
}

// sizes reports how many entries each batch carried, in arrival order.
func (c *collector) sizes() []int {
	out := make([]int, len(c.batches))
	for i, b := range c.batches {
		out[i] = len(b)
	}
	return out
}

// flat reports every id the collector saw, in arrival order, duplicates kept.
func (c *collector) flat() []string {
	var out []string
	for _, b := range c.batches {
		out = append(out, b...)
	}
	return out
}

// TestBatchesArePushedInChunksOfExactlyFifty pins BOTH spellings of the chunk
// size at once, which is what it takes: the two defects are mirror images and
// each is invisible to the assertion that catches the other.
//
// 101 entries is the smallest count that makes both observable and tells them
// apart. It gives three chunks, so the loop runs more than once — a count of 51
// would leave the second chunk as the remainder and a moved step would look
// like a correct short tail.
func TestBatchesArePushedInChunksOfExactlyFifty(t *testing.T) {
	const (
		chunk = 50
		total = 2*chunk + 1
	)

	lines := make([]string, total)
	want := make([]string, total)
	for i := range lines {
		id := fmt.Sprintf("e%03d", i)
		lines[i] = message(id, "assistant", "body")
		want[i] = "01234567-89ab-cdef-" + id
	}

	c := newCollector(t, http.StatusOK)
	_, count, err := processFile(sessionFile(t, lines...), "openclaw", "01234567-89ab-cdef", 0, false, c.server.URL)
	if err != nil {
		t.Fatalf(reachGuard+"processFile: %v", err)
	}
	if count != total {
		t.Fatalf(reachGuard+"processFile handled %d entries, want %d — it never reached the chunking", count, total)
	}

	// The sizes are the whole point. A step one too large yields 50,50 and
	// loses an entry; an end one too large yields 51,51 and repeats one.
	if got := c.sizes(); len(got) != 3 || got[0] != chunk || got[1] != chunk || got[2] != 1 {
		t.Errorf("batch sizes = %v, want [%d %d 1]", got, chunk, chunk)
	}

	got := c.flat()
	if len(got) != total {
		t.Errorf("logstack received %d entries in total, want %d", len(got), total)
	}
	for i := range got {
		if i < len(want) && got[i] != want[i] {
			t.Errorf("entry %d of the push stream = %q, want %q — the chunk windows do not tile the batch", i, got[i], want[i])
			break
		}
	}

	seen := make(map[string]int, len(got))
	for _, id := range got {
		seen[id]++
	}
	for _, id := range want {
		switch seen[id] {
		case 1:
		case 0:
			t.Errorf("entry %q never reached logstack — a chunk window skipped it", id)
		default:
			t.Errorf("entry %q reached logstack %d times — the chunk windows overlap", id, seen[id])
		}
	}
}

// TestPushFailsOnTheFirstNonSuccessStatus straddles the status threshold. 300 is
// the boundary and 299 is the last status that must be accepted; a fixture
// built on 200 and 500 passes whatever this number says.
//
// It matters more than a cosmetic threshold because of what the caller does with
// the answer: processFile returns the new offset alongside the error, and a
// status wrongly read as success advances the cursor past entries logstack
// never stored.
func TestPushFailsOnTheFirstNonSuccessStatus(t *testing.T) {
	for _, tc := range []struct {
		status  int
		wantErr bool
	}{
		{299, false},
		{300, true},
	} {
		c := newCollector(t, tc.status)
		_, _, err := processFile(
			sessionFile(t, message("e1", "assistant", "body")),
			"openclaw", "01234567-89ab-cdef", 0, false, c.server.URL,
		)
		if len(c.batches) != 1 {
			t.Fatalf(reachGuard+"status %d: %d batches pushed, want 1 — the test never reached the threshold", tc.status, len(c.batches))
		}
		if gotErr := err != nil; gotErr != tc.wantErr {
			t.Errorf("status %d: processFile error = %v, want an error: %v", tc.status, err, tc.wantErr)
		}
	}
}

// TestScannerAcceptsALineOfExactlyItsCeiling pins the JSONL line ceiling, and
// the ceiling is NOT the 1MB the source comment claims.
//
//	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB lines
//
// Measured: the largest line bufio.Scanner accepts under that call is
// 1024*1024-1 bytes. A line of exactly 1MB fills the buffer with no newline in
// it, and the token-too-long check fires before the split can succeed. The
// comment is one byte optimistic.
//
// The two spellings of 1024*1024 are NOT two boundaries, which is the finding
// here. The effective ceiling is the LARGER of the two, so moving either one
// DOWN by one changes nothing at all — the scorer carries both of those as
// known-negatives. Only moving one UP moves the ceiling, and that is what this
// test holds.
//
// ⚠️ The over-ceiling half asserts current behaviour, not correct behaviour: a
// line over the ceiling stops the scan and processFile returns a nil error,
// because scanner.Err() is never read. That defect is filed separately as
// 603e3ded. Pinning the count here is what makes the ceiling observable at all.
func TestScannerAcceptsALineOfExactlyItsCeiling(t *testing.T) {
	const ceiling = 1024*1024 - 1

	// A line of exactly n bytes: pad the text field to make up the difference.
	lineOf := func(n int) string {
		empty := message("e1", "assistant", "")
		if len(empty) > n {
			t.Fatalf(reachGuard+"envelope is %d bytes, cannot build a line of %d", len(empty), n)
		}
		line := message("e1", "assistant", strings.Repeat("a", n-len(empty)))
		if len(line) != n {
			t.Fatalf(reachGuard+"built a line of %d bytes, want %d", len(line), n)
		}
		return line
	}

	c := newCollector(t, http.StatusOK)
	_, count, err := processFile(sessionFile(t, lineOf(ceiling)), "openclaw", "01234567-89ab-cdef", 0, false, c.server.URL)
	if err != nil {
		t.Fatalf(reachGuard+"processFile on a line at the ceiling: %v", err)
	}
	if count != 1 {
		t.Errorf("a line of exactly %d bytes yielded %d entries, want 1 — the ceiling is lower than it ships as", ceiling, count)
	}

	over := newCollector(t, http.StatusOK)
	_, count, err = processFile(sessionFile(t, lineOf(ceiling+1)), "openclaw", "01234567-89ab-cdef", 0, false, over.server.URL)
	if err != nil {
		t.Fatalf(reachGuard+"processFile on a line over the ceiling: %v", err)
	}
	if count != 0 {
		t.Errorf("a line of %d bytes yielded %d entries, want 0 — the ceiling is higher than it ships as", ceiling+1, count)
	}
}

// TestCursorFileAndDirectoryShipWithTheirDeclaredModes pins 0755 and 0644.
//
// The umask is neutralised rather than compensated for. Reading the umask and
// subtracting it would make the assertion agree with whatever the process was
// started with, including a umask that strips the very bits under test — the
// test would then pass on a box where the cursor file ships at 0600.
func TestCursorFileAndDirectoryShipWithTheirDeclaredModes(t *testing.T) {
	const (
		dirMode  os.FileMode = 0o755
		fileMode os.FileMode = 0o644
	)

	previous := syscall.Umask(0)
	defer syscall.Umask(previous)

	path := filepath.Join(t.TempDir(), "cursors", "state.json")
	saveCursors(path, map[string]cursorState{"s1": {}})

	dir, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf(reachGuard+"stat cursor directory: %v", err)
	}
	if got := dir.Mode().Perm(); got != dirMode {
		t.Errorf("cursor directory mode = %04o, want %04o", got, dirMode)
	}

	file, err := os.Stat(path)
	if err != nil {
		t.Fatalf(reachGuard+"stat cursor file: %v", err)
	}
	if got := file.Mode().Perm(); got != fileMode {
		t.Errorf("cursor file mode = %04o, want %04o", got, fileMode)
	}
}

// TestInboundAuthorComesFromTheFirstCaptureGroup pins the capture-index guard.
//
// authorRe has exactly one capture group, so a match is always length 2 and
// `len(m) > 1` is the only thing standing between the prefix and the author
// field. Widen it by one and every inbound message is attributed to "user"
// while the regexp still matches — no error, no panic, just an author that is
// always the same word.
func TestInboundAuthorComesFromTheFirstCaptureGroup(t *testing.T) {
	authorOf := func(t *testing.T, role, text string) string {
		t.Helper()
		var e ocEntry
		if err := json.Unmarshal([]byte(message("e1", role, text)), &e); err != nil {
			t.Fatalf(reachGuard+"decode fixture: %v", err)
		}
		le := convertEntry(e, "openclaw", "01234567-89ab-cdef")
		if le == nil {
			t.Fatalf(reachGuard + "convertEntry dropped the fixture; it never reached the guard")
		}
		author, ok := le.Content["author"].(string)
		if !ok {
			t.Fatalf(reachGuard+"content has no string author: %#v", le.Content)
		}
		return author
	}

	if got := authorOf(t, "user", "[alice] hello"); got != "alice" {
		t.Errorf("author of a prefixed inbound message = %q, want %q — the capture group was not read", got, "alice")
	}
	// The fallback, so the guard cannot simply be deleted.
	if got := authorOf(t, "user", "hello"); got != "user" {
		t.Errorf("author of an unprefixed inbound message = %q, want %q", got, "user")
	}
	// And an outbound message never consults the regexp at all.
	if got := authorOf(t, "assistant", "[alice] hello"); got != "openclaw" {
		t.Errorf("author of an outbound message = %q, want the agent name", got)
	}
}
