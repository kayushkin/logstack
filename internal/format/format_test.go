package format

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/kayushkin/logstack/models"
)

// gClef is four bytes in UTF-8. A two-byte rune is not enough: only one of its
// two interior offsets is wrong, so a test that happens to pick the other one
// passes against a plain byte cut.
const gClef = "\U0001D11E"

// reachGuard marks a failure meaning the TEST could not reach the code it claims
// to exercise. The sabotage scorer keys on it to tell a broken fixture from a
// defect in the code under test.
const reachGuard = "REACH-GUARD: "

// straddling returns a string with a four-byte rune spanning byte offset n.
func straddling(t *testing.T, n int) string {
	t.Helper()
	s := strings.Repeat("x", n-2) + gClef + strings.Repeat("y", 30)
	if utf8.RuneStart(s[n]) {
		t.Fatalf(reachGuard+"fixture does not straddle offset %d — the test would prove nothing", n)
	}
	return s
}

func entry(content any) *models.LogEntry {
	return &models.LogEntry{
		Timestamp:    time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC),
		Level:        "info",
		Orchestrator: "inber",
		Type:         "message",
		Content:      content,
	}
}

// TestSummaryCutsStringContentOnARuneBoundary drives the 50-byte cut Summary
// applies to string content.
func TestSummaryCutsStringContentOnARuneBoundary(t *testing.T) {
	got := NewFormatter().Summary(entry(straddling(t, 50)))

	if !utf8.ValidString(got) {
		t.Errorf("summary is not valid UTF-8: %q", got)
	}
	if !strings.Contains(got, "inber") {
		t.Errorf("summary lost its orchestrator, so it is not the line under test: %q", got)
	}
}

// TestSummaryCutsMapMessageOnARuneBoundary drives the second 50-byte cut in
// Summary, the one behind a map[string]interface{} content shape. It is a
// separate test because it is a separate cut: a helper can score full marks
// while one of two call sites is fixed in appearance only.
func TestSummaryCutsMapMessageOnARuneBoundary(t *testing.T) {
	got := NewFormatter().Summary(entry(map[string]interface{}{
		"message": straddling(t, 50),
	}))

	if !utf8.ValidString(got) {
		t.Errorf("summary is not valid UTF-8: %q", got)
	}
	if !strings.Contains(got, "inber") {
		t.Errorf("summary lost its orchestrator, so it is not the line under test: %q", got)
	}
}

// TestTableCutsContentOnARuneBoundary drives Table's 40-byte cut, which is a
// third independent site with a different budget.
func TestTableCutsContentOnARuneBoundary(t *testing.T) {
	got := NewFormatter().Table([]models.LogEntry{*entry(straddling(t, 40))})

	if !utf8.ValidString(got) {
		t.Errorf("table is not valid UTF-8: %q", got)
	}
	if !strings.Contains(got, "TIMESTAMP") {
		t.Errorf("table lost its header, so it is not the output under test: %q", got)
	}
}

// TestFormatterLeavesShortContentAlone is the known-negative control: content
// that never reaches a cut must render byte-for-byte, and must do so against the
// unfixed byte cut too. Without it there is no way to tell "detects a straddle"
// from "reacts to non-ASCII input at all".
func TestFormatterLeavesShortContentAlone(t *testing.T) {
	short := "日本語"
	f := NewFormatter()

	if got := f.Summary(entry(short)); !strings.Contains(got, short) {
		t.Errorf("Summary dropped content that fits: %q", got)
	}
	if got := f.Table([]models.LogEntry{*entry(short)}); !strings.Contains(got, short) {
		t.Errorf("Table dropped content that fits: %q", got)
	}
}
