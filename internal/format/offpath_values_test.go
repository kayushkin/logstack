package format

import (
	"strings"
	"testing"

	"github.com/kayushkin/logstack/models"
)

// The numbers in this file that are NOT truncation budgets.
//
// boundary_values_test.go next door pins the three cuts — 50, 50 and 40 — and
// that is the whole of its remit. Everything else format.go compares against or
// formats with was enumerated by the 186th nightly pass and scored by nothing:
// six metric zero-guards, the table's separator width, and the column widths,
// which are spelled twice and have to agree.
//
// Measured 2026-08-15 by scripts/sabotage-offpath.py: this package caught 1 of
// its 11 off-path rows before this file, and the one it caught — the empty-table
// guard — was held by a test written to show a table renders at all.
//
// The shape they share is why a fixture misses them. Every one of these numbers
// is a ZERO or a WIDTH, and the inputs that distinguish a correct value from a
// value one unit off are the boring ones: a metric of exactly 1, a table of
// exactly one row, a field of exactly the column width. A fixture built to show
// the formatter working uses round numbers and long strings, and neither can
// see any of this.

// metricEntry returns an entry carrying exactly the three metrics given, so a
// test can set one to 1 and the others to 0 and name which guard it moved.
// Round numbers are what hid these guards; 1 is the only value that
// distinguishes `> 0` from `> 1`.
func metricEntry(tokensIn, tokensOut int, latencyMs int64) *models.LogEntry {
	e := entry("body")
	e.TokensIn = tokensIn
	e.TokensOut = tokensOut
	e.LatencyMs = latencyMs
	return e
}

// TestTextRendersATokenCountOfExactlyOne pins both halves of Text's token
// guard. It is one condition and TWO literals — `entry.TokensIn > 0 ||
// entry.TokensOut > 0` — so it needs two fixtures: an entry with only TokensIn
// set cannot tell you anything about the TokensOut half, because the `||`
// carries the clause either way.
func TestTextRendersATokenCountOfExactlyOne(t *testing.T) {
	f := NewFormatter()

	onlyIn := f.Text(metricEntry(1, 0, 0))
	if !strings.Contains(onlyIn, "[tokens: in=1 out=0]") {
		t.Errorf("Text with TokensIn=1 = %q, want the token clause in it — the tokens-in guard no longer admits 1", onlyIn)
	}

	onlyOut := f.Text(metricEntry(0, 1, 0))
	if !strings.Contains(onlyOut, "[tokens: in=0 out=1]") {
		t.Errorf("Text with TokensOut=1 = %q, want the token clause in it — the tokens-out guard no longer admits 1", onlyOut)
	}
}

// TestTextOmitsTheTokenClauseWhenBothCountsAreZero is the other side of the
// same boundary. Without it the guard could be deleted outright and the test
// above would still pass.
func TestTextOmitsTheTokenClauseWhenBothCountsAreZero(t *testing.T) {
	got := NewFormatter().Text(metricEntry(0, 0, 0))
	if strings.Contains(got, "tokens:") {
		t.Errorf("Text with no tokens = %q, want no token clause at all", got)
	}
}

// TestTextRendersALatencyOfExactlyOneMillisecond pins Text's latency guard,
// which is a separate literal from Logfmt's identical-looking one four lines of
// source later.
func TestTextRendersALatencyOfExactlyOneMillisecond(t *testing.T) {
	got := NewFormatter().Text(metricEntry(0, 0, 1))
	if !strings.Contains(got, "[latency: 1ms]") {
		t.Errorf("Text with LatencyMs=1 = %q, want the latency clause in it", got)
	}

	none := NewFormatter().Text(metricEntry(0, 0, 0))
	if strings.Contains(none, "latency:") {
		t.Errorf("Text with LatencyMs=0 = %q, want no latency clause", none)
	}
}

// TestLogfmtRendersMetricsOfExactlyOne pins Logfmt's three guards. They are
// three separate literals in three separate conditions — unlike Text, Logfmt
// does not share a condition between the token counts — so each gets its own
// assertion off its own fixture.
func TestLogfmtRendersMetricsOfExactlyOne(t *testing.T) {
	f := NewFormatter()

	for _, tc := range []struct {
		name  string
		entry *models.LogEntry
		want  string
	}{
		{"tokens_in", metricEntry(1, 0, 0), "tokens_in=1"},
		{"tokens_out", metricEntry(0, 1, 0), "tokens_out=1"},
		{"latency_ms", metricEntry(0, 0, 1), "latency_ms=1"},
	} {
		got := f.Logfmt(tc.entry)
		if !strings.Contains(got, tc.want) {
			t.Errorf("Logfmt %s = %q, want %q in it — the guard no longer admits 1", tc.name, got, tc.want)
		}
	}

	none := f.Logfmt(metricEntry(0, 0, 0))
	for _, unwanted := range []string{"tokens_in=", "tokens_out=", "latency_ms="} {
		if strings.Contains(none, unwanted) {
			t.Errorf("Logfmt with no metrics = %q, want no %q in it", none, unwanted)
		}
	}
}

// TestTableSeparatorIsExactlyOneHundredDashes pins the separator width. It is
// the cheapest number in the file to hold — the separator's whole content is
// the number — and nothing held it.
func TestTableSeparatorIsExactlyOneHundredDashes(t *testing.T) {
	const width = 100

	got := NewFormatter().Table([]models.LogEntry{*entry("body")})

	var separator string
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, "---") {
			separator = line
			break
		}
	}
	if separator == "" {
		t.Fatalf("table = %q, has no separator row at all — the test cannot reach the width", got)
	}
	if separator != strings.Repeat("-", width) {
		t.Errorf("separator is %d dashes, want exactly %d", len(separator), width)
	}
}

// TestTableHeaderAndRowsUseTheSameColumnWidths pins BOTH spellings of the
// column widths at once, because what has to be true of them is a relation:
// they have to agree. A test that asserts the header alone holds the header's
// literal and says nothing about the rows', and vice versa — so an assertion
// about either one on its own is exactly the shape that let both drift.
//
// The fields are chosen to be SHORTER than their columns, so every column is
// padded and its width is observable. A field longer than its column overflows
// and the width vanishes from the output.
func TestTableHeaderAndRowsUseTheSameColumnWidths(t *testing.T) {
	e := entry("body")
	e.Level = "info"
	e.Orchestrator = "inber"
	e.Type = "message"

	got := NewFormatter().Table([]models.LogEntry{*e})

	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf(
			"table = %q, want exactly 3 lines (header, separator, one row) — the test cannot compare columns it cannot find",
			got,
		)
	}
	header, row := lines[0], lines[2]

	// Column starts, derived from the format string's widths plus one space
	// each: %-20s %-10s %-15s %-10s %s.
	for _, want := range []struct {
		column string
		offset int
	}{
		{"LEVEL / level", 21},
		{"ORCHESTRATOR / orchestrator", 32},
		{"TYPE / type", 48},
		{"CONTENT / content", 59},
	} {
		if len(header) <= want.offset || len(row) <= want.offset {
			t.Fatalf("header %q or row %q is too short to carry column %s", header, row, want.column)
		}
		if header[want.offset] == ' ' {
			t.Errorf("header column %s does not start at offset %d: %q", want.column, want.offset, header)
		}
		if row[want.offset] == ' ' {
			t.Errorf("row column %s does not start at offset %d: %q", want.column, want.offset, row)
		}
	}
}

// TestTableRendersASingleRow straddles the empty guard. `len(entries) == 0` is
// a boundary like any other and the input that distinguishes it from
// `len(entries) <= 1` is exactly one row — the input a fixture built to show a
// table least often uses.
func TestTableRendersASingleRow(t *testing.T) {
	f := NewFormatter()

	if got := f.Table(nil); got != "No logs found" {
		t.Errorf("empty table = %q, want %q", got, "No logs found")
	}

	one := f.Table([]models.LogEntry{*entry("the only row")})
	if strings.Contains(one, "No logs found") {
		t.Errorf("table of exactly one entry = %q — it was treated as empty", one)
	}
	if !strings.Contains(one, "the only row") {
		t.Errorf("table of exactly one entry = %q, want the row's content in it", one)
	}
}
