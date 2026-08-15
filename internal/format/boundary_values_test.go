package format

import (
	"strings"
	"testing"

	"github.com/kayushkin/logstack/models"
)

// Every cut in format.go is spelled TWICE:
//
//	if len(v) > 50 {
//	    content = textutil.TruncateAtRuneBoundary(v, 50) + "..."
//	}
//
// The guard decides whether to cut and the budget decides where. They are
// separate literals that have to agree, and no single test sees both: move only
// the budget and content is cut one byte early past a guard that still fires at
// 51; move only the guard and a 51-byte string is emitted whole.
//
// So each of the three cuts gets two tests, not one — six numbers, six pins.
// Measured 2026-08-15 by sabotage: this package caught 0 of these 6 before this
// file, while holding all three rune-boundary rows. Its existing fixtures vary
// the RUNE PADDING across a fixed cut, which is the right axis for the
// mechanism and orthogonal to every number in the file.

// The last byte inside a budget and the first byte outside it. Marking the two
// bytes lets an assertion name which side of the cut the output stopped on. A
// length assertion would be equally true of a cut one byte early on a different
// input.
const (
	budgetMarker = "~"
	pastBudget   = "!"
)

// atBudget returns n bytes of ASCII ending in the marker, i.e. content that
// exactly fills a budget of n.
func atBudget(n int) string {
	return strings.Repeat("a", n-1) + budgetMarker
}

// TestSummaryCutsStringContentAtExactlyFiftyBytes pins Summary's string budget.
func TestSummaryCutsStringContentAtExactlyFiftyBytes(t *testing.T) {
	const budget = 50
	content := atBudget(budget) + pastBudget + strings.Repeat("b", 20)

	got := NewFormatter().Summary(entry(content))

	if want := atBudget(budget) + "..."; !strings.HasSuffix(got, want) {
		t.Errorf("summary = %q, want it to end %q — the cut did not land on the budget", got, want)
	}
	if strings.Contains(got, pastBudget) {
		t.Errorf("summary = %q, kept the byte one past the budget", got)
	}
}

// TestSummaryStringContentIsCutOneByteOverItsBudget straddles Summary's string
// guard: content of exactly the budget is emitted whole, one byte more is cut.
func TestSummaryStringContentIsCutOneByteOverItsBudget(t *testing.T) {
	const budget = 50
	fits := atBudget(budget)
	over := fits + pastBudget

	f := NewFormatter()

	whole := f.Summary(entry(fits))
	if !strings.HasSuffix(whole, fits) {
		t.Errorf("summary of content that exactly fills the budget = %q, want it to end %q", whole, fits)
	}
	if strings.Contains(whole, "...") {
		t.Errorf("summary = %q — content of exactly the budget was cut", whole)
	}

	cut := f.Summary(entry(over))
	if want := fits + "..."; !strings.HasSuffix(cut, want) {
		t.Errorf("summary of content one byte over the budget = %q, want it to end %q", cut, want)
	}
	if strings.Contains(cut, pastBudget) {
		t.Errorf("summary = %q — content one byte over the budget was emitted whole", cut)
	}
}

// TestSummaryCutsMapMessageAtExactlyFiftyBytes pins the budget of Summary's
// second cut, the one behind a map content shape. It is a separate test because
// it is a separate literal: the two spellings of 50 in one function can drift
// apart, and a test for either branch covers the other in appearance only.
func TestSummaryCutsMapMessageAtExactlyFiftyBytes(t *testing.T) {
	const budget = 50
	content := atBudget(budget) + pastBudget + strings.Repeat("b", 20)

	got := NewFormatter().Summary(entry(map[string]interface{}{"message": content}))

	if want := atBudget(budget) + "..."; !strings.HasSuffix(got, want) {
		t.Errorf("summary = %q, want it to end %q — the cut did not land on the budget", got, want)
	}
	if strings.Contains(got, pastBudget) {
		t.Errorf("summary = %q, kept the byte one past the budget", got)
	}
}

// TestSummaryMapMessageIsCutOneByteOverItsBudget straddles the map branch's
// guard.
func TestSummaryMapMessageIsCutOneByteOverItsBudget(t *testing.T) {
	const budget = 50
	fits := atBudget(budget)
	over := fits + pastBudget

	f := NewFormatter()

	whole := f.Summary(entry(map[string]interface{}{"message": fits}))
	if !strings.HasSuffix(whole, fits) {
		t.Errorf("summary of a message that exactly fills the budget = %q, want it to end %q", whole, fits)
	}
	if strings.Contains(whole, "...") {
		t.Errorf("summary = %q — a message of exactly the budget was cut", whole)
	}

	cut := f.Summary(entry(map[string]interface{}{"message": over}))
	if want := fits + "..."; !strings.HasSuffix(cut, want) {
		t.Errorf("summary of a message one byte over the budget = %q, want it to end %q", cut, want)
	}
	if strings.Contains(cut, pastBudget) {
		t.Errorf("summary = %q — a message one byte over the budget was emitted whole", cut)
	}
}

// TestTableCutsContentAtExactlyFortyBytes pins Table's budget, which is 40 and
// not 50: a fixture built for Summary passes here whatever this number says.
func TestTableCutsContentAtExactlyFortyBytes(t *testing.T) {
	const budget = 40
	content := atBudget(budget) + pastBudget + strings.Repeat("b", 20)

	got := NewFormatter().Table([]models.LogEntry{*entry(content)})

	if want := atBudget(budget) + "..."; !strings.Contains(got, want) {
		t.Errorf("table = %q, want %q in it — the cut did not land on the budget", got, want)
	}
	if strings.Contains(got, pastBudget) {
		t.Errorf("table = %q, kept the byte one past the budget", got)
	}
}

// TestTableContentIsCutOneByteOverItsBudget straddles Table's guard.
func TestTableContentIsCutOneByteOverItsBudget(t *testing.T) {
	const budget = 40
	fits := atBudget(budget)
	over := fits + pastBudget

	f := NewFormatter()

	whole := f.Table([]models.LogEntry{*entry(fits)})
	if !strings.Contains(whole, fits) {
		t.Errorf("table row for content that exactly fills the budget = %q, want %q in it", whole, fits)
	}
	if strings.Contains(whole, "...") {
		t.Errorf("table = %q — content of exactly the budget was cut", whole)
	}

	cut := f.Table([]models.LogEntry{*entry(over)})
	if want := fits + "..."; !strings.Contains(cut, want) {
		t.Errorf("table row for content one byte over the budget = %q, want %q in it", cut, want)
	}
	if strings.Contains(cut, pastBudget) {
		t.Errorf("table = %q — content one byte over the budget was emitted whole", cut)
	}
}
