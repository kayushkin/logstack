package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The dry-run log line carries two cuts. The 8-byte session-id cut is already
// pinned to its value — TestDryRunLogLineSurvivesAShortOrMultibyteSessionID
// asserts the exact prefix with its trailing space, so any move of that number
// reddens it, which was luck rather than intent. The 80-byte text cut is not,
// and it is spelled twice:
//
//	if len(t) > 80 {
//	    t = textutil.TruncateAtRuneBoundary(t, 80) + "..."
//	}
//
// Measured 2026-08-15 by sabotage: both numbers moved by one and the suite
// stayed green. The rune-boundary fixture next door cannot see either. Its
// four-byte rune spans offsets 78..81, so a cut at 79 and a cut at 80 both walk
// back to 78 and produce the same string — the fixture varies rune padding
// across a fixed cut, and that axis is orthogonal to the number.

// The last byte inside the budget and the first byte outside it, so an
// assertion can name which side of the cut the line stopped on.
const (
	budgetMarker = "~"
	pastBudget   = "!"
)

// dryRunLog pushes one message with the given body through processFile in
// dry-run mode and returns what it logged. Driving processFile rather than the
// helper is deliberate: a call site can be correct in appearance only, and only
// a test that goes through it can tell.
func dryRunLog(t *testing.T, body string) string {
	t.Helper()

	line := `{"type":"message","id":"e1","timestamp":"2026-08-08T12:00:00Z",` +
		`"message":{"role":"assistant","model":"claude","content":[{"type":"text","text":"` +
		body + `"}]}}` + "\n"
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
	return logged.String()
}

// atBudget returns n bytes of ASCII ending in the marker, i.e. a message body
// that exactly fills a budget of n.
func atBudget(n int) string {
	return strings.Repeat("a", n-1) + budgetMarker
}

// TestDryRunTextIsCutAtExactlyEightyBytes pins where the message cut lands.
func TestDryRunTextIsCutAtExactlyEightyBytes(t *testing.T) {
	const budget = 80
	got := dryRunLog(t, atBudget(budget)+pastBudget+strings.Repeat("b", 30))

	if want := atBudget(budget) + "..."; !strings.Contains(got, want) {
		t.Errorf("dry-run log line = %q, want %q in it — the cut did not land on the budget", got, want)
	}
	if strings.Contains(got, pastBudget) {
		t.Errorf("dry-run log line = %q, kept the byte one past the budget", got)
	}
}

// TestDryRunTextIsCutOneByteOverItsBudget straddles the guard: a message of
// exactly 80 bytes is logged whole, one byte more is cut.
func TestDryRunTextIsCutOneByteOverItsBudget(t *testing.T) {
	const budget = 80
	fits := atBudget(budget)

	whole := dryRunLog(t, fits)
	if !strings.Contains(whole, fits) {
		t.Errorf("dry-run log line for a message that exactly fills the budget = %q, want %q in it", whole, fits)
	}
	if strings.Contains(whole, "...") {
		t.Errorf("dry-run log line = %q — a message of exactly the budget was cut", whole)
	}

	cut := dryRunLog(t, fits+pastBudget)
	if want := fits + "..."; !strings.Contains(cut, want) {
		t.Errorf("dry-run log line for a message one byte over the budget = %q, want %q in it", cut, want)
	}
	if strings.Contains(cut, pastBudget) {
		t.Errorf("dry-run log line = %q — a message one byte over the budget was logged whole", cut)
	}
}
