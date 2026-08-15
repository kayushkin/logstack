package main

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// What these tests hold, and why the card that asked for them was half wrong.
//
// `603e3ded` filed processFile's unread scanner.Err() as silent bulk data loss:
// "every remaining message in that session file is skipped" and "the cursor is
// advanced past the skipped messages and they are never retried on the next
// poll". Its own body flagged that as inferred from the Seek that follows and
// asked for a fixture before anyone fixed it. Measured 2026-08-15 by driving
// processFile poll by poll from its own returned cursor:
//
//	huge line   polls to drain   entries delivered
//	1MB-1                    1   e1, huge, e3, e4      (under the ceiling)
//	1MB                      2   e1,       e3, e4
//	1MB+1                    2   e1,       e3, e4
//	3MB                      4   e1,       e3, e4
//
// The cursor advances into the MIDDLE of the over-long line, not past the
// entries after it — bufio.Scanner only reports ErrTooLong when the buffer is
// full with no newline in it, so everything buffered at that moment belongs to
// the one long line and nothing complete is stranded behind it. The entries
// after it are re-read from the new cursor and delivered on the next poll.
//
// So the real defect is smaller than the card and still worth fixing:
//   - the over-long entry itself is dropped PERMANENTLY, and
//   - nothing anywhere says so — processFile returned nil and poll() logged
//     "pushed N messages".
//
// That silence is what the fix removes. It does not change which entries are
// delivered, which is why TestEntriesAfterAnOverLongLineArriveOnALaterPoll
// asserts the recovery too: it is the reason the error must NOT be propagated,
// and a later pass answering 6fbf83b3 needs to see it before changing policy.

// lineOfExactly renders one valid JSONL entry of exactly n bytes.
func lineOfExactly(t *testing.T, id string, n int) string {
	t.Helper()
	empty := message(id, "assistant", "")
	if len(empty) > n {
		t.Fatalf(reachGuard+"envelope is %d bytes, cannot build a line of %d", len(empty), n)
	}
	line := message(id, "assistant", strings.Repeat("a", n-len(empty)))
	if len(line) != n {
		t.Fatalf(reachGuard+"built a line of %d bytes, want %d", len(line), n)
	}
	return line
}

// writeSession writes lines to a session file and returns its path and size.
func writeSession(t *testing.T, lines ...string) (string, int64) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "01234567-89ab-cdef.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf(reachGuard+"write session file: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf(reachGuard+"stat session file: %v", err)
	}
	return path, info.Size()
}

// TestAnOverLongLineIsReportedInsteadOfPassingAsCleanEOF is the regression for
// the swallowed scanner.Err().
//
// The assertion is on the LOG, not on the returned error, and that is the point
// of the fix rather than a weaker version of it: poll() treats a non-nil error
// as "do not advance the cursor", so returning ErrTooLong here would pin the
// daemon to the same over-long line every tick and lose every entry after it —
// turning a one-entry loss into a permanent stall. The error is reported where
// it happens and the cursor still advances.
func TestAnOverLongLineIsReportedInsteadOfPassingAsCleanEOF(t *testing.T) {
	const ceiling = 1024*1024 - 1

	path, size := writeSession(t,
		message("e1", "assistant", "first"),
		lineOfExactly(t, "huge", ceiling+1),
		message("e3", "assistant", "third"),
	)

	var logged bytes.Buffer
	restore := captureLog(&logged)
	c := newCollector(t, http.StatusOK)
	newOffset, count, err := processFile(path, "openclaw", "01234567-89ab-cdef", 0, false, c.server.URL)
	restore()

	// NOT a reach guard. The nil here is the behaviour under test: the scan
	// error is deliberately reported rather than returned, because poll() reads
	// a non-nil error as "leave the cursor alone and retry next tick" and the
	// daemon would then re-read this same over-long line every tick forever,
	// never reaching the entries after it.
	if err != nil {
		t.Fatalf("processFile returned %v for an over-long line, want nil.\n"+
			"Propagating here stalls the daemon on the long line permanently. If you are "+
			"deliberately changing that policy — 6fbf83b3 asks the same question of "+
			"llm-bridge-adapter's 1MB SSE ceiling — then "+
			"TestEntriesAfterAnOverLongLineArriveOnALaterPoll describes what you are giving up.", err)
	}
	if count != 1 {
		t.Fatalf(reachGuard+"want the scan to stop after the first entry, got count=%d", count)
	}

	got := logged.String()
	if got == "" {
		t.Fatal("an over-long line logged NOTHING — scanner.Err() is being swallowed and the dropped entry is invisible")
	}
	for _, want := range []string{
		"01234567-89ab-cdef", // which session lost an entry
		"token too long",     // what actually went wrong
	} {
		if !strings.Contains(got, want) {
			t.Errorf("scan-error log does not mention %q, so the report cannot be acted on\nlog: %s", want, got)
		}
	}
	if !strings.Contains(got, "openclaw-logpush") {
		t.Errorf("scan-error log is unattributed, it will not be findable in a shared log\nlog: %s", got)
	}

	// The cursor must still move, or the next poll re-reads the same long line.
	if newOffset == 0 {
		t.Error("cursor did not advance past the over-long line; the daemon would stall on it every tick")
	}
	if newOffset >= size {
		t.Errorf("cursor jumped to %d of %d — past the entries after the long line, which IS the bulk loss the card feared", newOffset, size)
	}
}

// TestACleanFileLogsNothing keeps the report above from becoming noise. A
// scan-error line that fires on every ordinary poll is one nobody reads, and
// the whole value of the fix is that the line means something when it appears.
func TestACleanFileLogsNothing(t *testing.T) {
	path, _ := writeSession(t,
		message("e1", "assistant", "first"),
		message("e2", "assistant", "second"),
	)

	var logged bytes.Buffer
	restore := captureLog(&logged)
	c := newCollector(t, http.StatusOK)
	_, count, err := processFile(path, "openclaw", "01234567-89ab-cdef", 0, false, c.server.URL)
	restore()

	if err != nil {
		t.Fatalf(reachGuard+"processFile: %v", err)
	}
	if count != 2 {
		t.Fatalf(reachGuard+"want 2 entries from a clean file, got %d", count)
	}
	if s := logged.String(); s != "" {
		t.Errorf("a clean file logged %q, want silence — a report that fires always reports nothing", s)
	}
}

// TestEntriesAfterAnOverLongLineArriveOnALaterPoll pins the recovery the card
// said did not happen, and it is the load-bearing reason the fix logs rather
// than propagates. Anyone answering 6fbf83b3 ("skip it, or kill the stream?")
// changes this test, and should have to.
//
// Driven poll by poll from processFile's own returned cursor, exactly as poll()
// does, because the behaviour under test only appears ACROSS polls — a single
// call genuinely does stop mid-file, which is what made the card's reading of it
// look right.
func TestEntriesAfterAnOverLongLineArriveOnALaterPoll(t *testing.T) {
	const ceiling = 1024*1024 - 1

	for _, tc := range []struct {
		name      string
		hugeLen   int
		wantIDs   []string
		wantPolls int
	}{
		{"at the ceiling, nothing is lost at all", ceiling,
			[]string{"sess-e1", "sess-huge", "sess-e3", "sess-e4"}, 1},
		{"one byte over, the long entry is dropped", ceiling + 1,
			[]string{"sess-e1", "sess-e3", "sess-e4"}, 2},
		{"three megabytes, three polls to step over it", 3 * 1024 * 1024,
			[]string{"sess-e1", "sess-e3", "sess-e4"}, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, size := writeSession(t,
				message("e1", "assistant", "first"),
				lineOfExactly(t, "huge", tc.hugeLen),
				message("e3", "assistant", "third"),
				message("e4", "assistant", "fourth"),
			)

			var delivered []string
			offset := int64(0)
			polls := 0
			restore := captureLog(&bytes.Buffer{})
			for polls < 10 {
				c := newCollector(t, http.StatusOK)
				newOffset, _, err := processFile(path, "openclaw", "sess", offset, false, c.server.URL)
				// NOT a reach guard: an error returned here is poll() being told
				// to stop advancing, which is the exact failure this test exists
				// to describe. Scoring it as a broken fixture would hide it.
				if err != nil {
					restore()
					t.Fatalf("processFile returned %v on poll %d, want nil — a propagated scan error "+
						"pins the cursor to the over-long line and none of the entries after it ever arrive", err, polls+1)
				}
				delivered = append(delivered, c.flat()...)
				// Count only polls that moved the cursor. The last call finds
				// nothing new and is the daemon idling, not a cost of the long
				// line.
				if newOffset == offset {
					break
				}
				polls++
				offset = newOffset
			}
			restore()

			if offset != size {
				t.Errorf("drained to offset %d of %d — the file was not fully consumed", offset, size)
			}
			if polls != tc.wantPolls {
				t.Errorf("took %d polls to drain, want %d — the cost of stepping over a long line moved", polls, tc.wantPolls)
			}
			if strings.Join(delivered, ",") != strings.Join(tc.wantIDs, ",") {
				t.Errorf("delivered %v, want %v", delivered, tc.wantIDs)
			}
		})
	}
}

// TestTheOverLongEntryItselfIsNeverDelivered isolates the one loss that IS
// permanent, so it does not hide inside the recovery asserted above. No number
// of polls brings this entry back: the cursor steps over it a buffer at a time
// and the bytes are never re-scanned as a whole.
func TestTheOverLongEntryItselfIsNeverDelivered(t *testing.T) {
	const ceiling = 1024*1024 - 1

	path, size := writeSession(t,
		lineOfExactly(t, "huge", ceiling+1),
		message("e2", "assistant", "second"),
	)

	var delivered []string
	offset := int64(0)
	restore := captureLog(&bytes.Buffer{})
	for i := 0; i < 10; i++ {
		c := newCollector(t, http.StatusOK)
		newOffset, _, err := processFile(path, "openclaw", "sess", offset, false, c.server.URL)
		if err != nil {
			restore()
			t.Fatalf("processFile returned %v, want nil — a propagated scan error stops the cursor "+
				"and the entry after the long line never arrives", err)
		}
		delivered = append(delivered, c.flat()...)
		if newOffset == offset {
			break
		}
		offset = newOffset
	}
	restore()

	if offset != size {
		t.Fatalf(reachGuard+"file not drained: offset %d of %d", offset, size)
	}
	for _, id := range delivered {
		if id == "sess-huge" {
			t.Fatal("the over-long entry was delivered after all — the ceiling moved and this test is now the wrong shape")
		}
	}
	if strings.Join(delivered, ",") != "sess-e2" {
		t.Errorf("delivered %v, want only sess-e2 — the entry after the long line must survive", delivered)
	}
}
