package main

import (
	"bytes"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"
)

// reachGuard marks a failure meaning the TEST could not reach the code it claims
// to exercise. The sabotage scorer keys on it to tell a broken fixture from a
// defect in the code under test.
const reachGuard = "REACH-GUARD: "

// sessionDir builds the layout discoverSessions walks: <root>/agents/<agent>/sessions/<id>.jsonl.
func sessionDir(t *testing.T, names ...string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "agents", "openclaw", "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf(reachGuard+"mkdir fixture: %v", err)
	}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("{}\n"), 0o644); err != nil {
			t.Fatalf(reachGuard+"write fixture %q: %v", n, err)
		}
	}
	return root
}

// oneMessageJSONL is a session file with a single entry convertEntry accepts, so
// processFile reaches its dry-run log line.
const oneMessageJSONL = `{"type":"message","id":"e1","timestamp":"2026-08-08T12:00:00Z",` +
	`"message":{"role":"assistant","model":"claude","content":[{"type":"text","text":"hello"}]}}` + "\n"

// TestDryRunLogLineSurvivesAShortOrMultibyteSessionID drives processFile, which
// is where the 8-byte session-id prefix is actually cut.
//
// A session id is a filename stem, not a hex id: discoverSessions takes it from
// strings.TrimSuffix(f.Name(), ".jsonl") with no constraint on its length or its
// bytes. So the cut has to survive a stem shorter than 8 bytes, which a plain
// sessionID[:8] panics on, and a multi-byte rune across offset 8, which it
// corrupts.
//
// Neither case is reachable by any scan in this sweep: every one of them anchors
// on a length guard, and this cut has none. Testing the helper directly would
// not have found it either — the defect is at the call site.
func TestDryRunLogLineSurvivesAShortOrMultibyteSessionID(t *testing.T) {
	cases := []struct {
		name      string
		sessionID string
		wantInLog string
		why       string
	}{
		{"hex id, the ordinary case", "01234567-89ab-cdef", "01234567", "unchanged by the fix"},
		{"stem shorter than the cut", "a", "a", "a plain sessionID[:8] panics here"},
		{"stem exactly the cut", "abcdefgh", "abcdefgh", "boundary case, no cut needed"},
		{"multibyte stem", "日本語セッション", "日本", "offset 8 falls inside the third rune"},
		{"accented stem", "héllo-wörld", "héllo-w", "offset 8 is aligned here, so unchanged"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), c.sessionID+".jsonl")
			if err := os.WriteFile(path, []byte(oneMessageJSONL), 0o644); err != nil {
				t.Fatalf(reachGuard+"write session file: %v", err)
			}

			var logged bytes.Buffer
			restore := captureLog(&logged)
			_, count, err := processFile(path, "openclaw", c.sessionID, 0, true, "")
			restore()

			if err != nil {
				t.Fatalf(reachGuard+"processFile: %v", err)
			}
			if count != 1 {
				t.Fatalf(reachGuard+"processFile handled %d entries, want 1 — it never reached the cut", count)
			}

			got := logged.String()
			if !utf8.ValidString(got) {
				t.Errorf("dry-run log line is not valid UTF-8: %q", got)
			}
			if !strings.Contains(got, "openclaw/"+c.wantInLog+" ") {
				t.Errorf("dry-run log line = %q, want the prefix %q in it (%s)", got, c.wantInLog, c.why)
			}
		})
	}
}

// captureLog redirects the standard logger into w and returns a function
// restoring it.
func captureLog(w io.Writer) func() {
	out, flags := log.Writer(), log.Flags()
	log.SetOutput(w)
	log.SetFlags(0)
	return func() {
		log.SetOutput(out)
		log.SetFlags(flags)
	}
}

// TestDiscoverSessionsYieldsIDsTheCutMustSurvive pins the reason the case above
// matters: discoverSessions really does hand back short and multi-byte ids, so
// the cut's inputs are not hypothetical.
func TestDiscoverSessionsYieldsIDsTheCutMustSurvive(t *testing.T) {
	root := sessionDir(t, "a.jsonl", "short.jsonl", "日本語セッション.jsonl", "01234567-89ab-cdef.jsonl")

	var ids []string
	for _, s := range discoverSessions(root) {
		ids = append(ids, s.sessionID)
	}
	sort.Strings(ids)

	want := []string{"01234567-89ab-cdef", "a", "short", "日本語セッション"}
	if len(ids) != len(want) {
		t.Fatalf(reachGuard+"discovered %d sessions, want %d: %q", len(ids), len(want), ids)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Errorf("session %d = %q, want %q", i, ids[i], want[i])
		}
	}

	// The point of the fixture: two of these are shorter than the 8-byte cut and
	// one straddles it.
	shorter, straddles := 0, 0
	for _, id := range ids {
		if len(id) < 8 {
			shorter++
		} else if !utf8.RuneStart(id[8]) {
			straddles++
		}
	}
	if shorter == 0 || straddles == 0 {
		t.Fatalf(reachGuard+"fixture yielded %d short and %d straddling ids — it proves nothing",
			shorter, straddles)
	}
	t.Logf("%d ids shorter than the cut, %d straddling it", shorter, straddles)
}

// TestDryRunTextCutStaysValidUTF8 covers the other cut in this file: the 80-byte
// truncation of a message body for the dry-run log line.
//
// It drives processFile rather than the helper. Calling the helper here would
// pass against the unfixed byte cut, because the helper is not where the defect
// is — a call site can be fixed in appearance only, and only a test that goes
// through it can tell.
func TestDryRunTextCutStaysValidUTF8(t *testing.T) {
	// The cut lands at 80 bytes of the extracted text. Put a four-byte rune
	// across it.
	body := strings.Repeat("x", 78) + "\U0001D11E" + strings.Repeat("y", 30)
	if utf8.RuneStart(body[80]) {
		t.Fatalf(reachGuard + "fixture does not straddle offset 80 — the test would prove nothing")
	}

	line := `{"type":"message","id":"e1","timestamp":"2026-08-08T12:00:00Z",` +
		`"message":{"role":"assistant","model":"claude","content":[{"type":"text","text":"` + body + `"}]}}` + "\n"
	path := filepath.Join(t.TempDir(), "01234567-89ab-cdef.jsonl")
	if err := os.WriteFile(path, []byte(line), 0o644); err != nil {
		t.Fatalf(reachGuard+"write session file: %v", err)
	}

	var logged bytes.Buffer
	restore := captureLog(&logged)
	_, count, err := processFile(path, "openclaw", "01234567-89ab-cdef", 0, true, "")
	restore()

	if err != nil {
		t.Fatalf(reachGuard+"processFile: %v", err)
	}
	if count != 1 {
		t.Fatalf(reachGuard+"processFile handled %d entries, want 1 — it never reached the cut", count)
	}

	got := logged.String()
	if !utf8.ValidString(got) {
		t.Errorf("dry-run log line is not valid UTF-8: %q", got)
	}
	if !strings.Contains(got, "...") {
		t.Errorf("dry-run log line was not truncated, so it is not the line under test: %q", got)
	}
}

// TestDryRunTextThatFitsIsUnchanged is the known-negative control for the 80-byte
// cut: multi-byte text that never reaches the cut must appear whole, and must do
// so against the unfixed byte cut too.
func TestDryRunTextThatFitsIsUnchanged(t *testing.T) {
	body := "日本語のメッセージ"
	line := `{"type":"message","id":"e1","timestamp":"2026-08-08T12:00:00Z",` +
		`"message":{"role":"assistant","model":"claude","content":[{"type":"text","text":"` + body + `"}]}}` + "\n"
	path := filepath.Join(t.TempDir(), "01234567-89ab-cdef.jsonl")
	if err := os.WriteFile(path, []byte(line), 0o644); err != nil {
		t.Fatalf(reachGuard+"write session file: %v", err)
	}

	var logged bytes.Buffer
	restore := captureLog(&logged)
	_, _, err := processFile(path, "openclaw", "01234567-89ab-cdef", 0, true, "")
	restore()
	if err != nil {
		t.Fatalf(reachGuard+"processFile: %v", err)
	}

	if got := logged.String(); !strings.Contains(got, body) {
		t.Errorf("text that fits was altered: %q", got)
	}
}
