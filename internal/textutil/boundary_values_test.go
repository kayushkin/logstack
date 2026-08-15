package textutil

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// The tests in textutil_test.go pin the MECHANISM: that a cut lands on a rune
// boundary. Proving a cut is aligned says nothing about WHERE it landed, so
// this file pins the numbers.
//
// Three of the four values here were already held, and by the control rather
// than by the sweep. TestTruncateAtRuneBoundaryLeavesAnAlignedCutAlone asserts
// equality against input[:budget] at every aligned budget, and an equality
// assertion pins a value as a side effect of pinning behaviour. The sweep next
// to it asserts properties — valid UTF-8, within budget, a prefix of the input,
// no more than a rune lost — and every one of those survives a budget that
// drifts by one. Measured 2026-08-15 by sabotage: the value rows this file
// closes went 3/4 in this package and 0/6 in internal/format, which has no
// equality control.
//
// So the rule the sweep leans on: a property assertion pins a mechanism, an
// equality assertion pins a value, and a suite made only of the first can hold
// every budget in the file wrong.

// budgetMarker is the last byte inside a budget and pastBudget the first byte
// outside it. Naming the two bytes lets an assertion say which side of the cut
// the result stopped on, instead of asserting a length — a length is equally
// true of a cut that landed one byte early on a different input.
const (
	budgetMarker = "~"
	pastBudget   = "!"
)

// TestTruncateAtRuneBoundaryKeepsOneByteAtABudgetOfOne separates `maxBytes <= 0`
// from `maxBytes <= 1`.
//
// The budget sweep runs over a budget of 1 and cannot tell them apart: its input
// is built from multi-byte runes, and for those a 1-byte budget correctly yields
// "" either way, because the walk-back eats the straddling rune. It takes a
// single-byte rune at that budget to separate them — which is why a value can
// sit inside a swept range and still be unpinned.
func TestTruncateAtRuneBoundaryKeepsOneByteAtABudgetOfOne(t *testing.T) {
	if got, want := TruncateAtRuneBoundary("hello", 1), "h"; got != want {
		t.Errorf("TruncateAtRuneBoundary(%q, 1) = %q, want %q — a budget of one byte "+
			"fits one ASCII byte, so the empty-budget guard has drifted", "hello", got, want)
	}
	// The other side of the same guard, so a fix cannot satisfy this test by
	// widening the guard the other way.
	if got, want := TruncateAtRuneBoundary("hello", 0), ""; got != want {
		t.Errorf("TruncateAtRuneBoundary(%q, 0) = %q, want %q", "hello", got, want)
	}
}

// TestTruncateAtRuneBoundaryDropsARuneWiderThanTheBudget separates `cut > 0`
// from `cut > 1`: the walk-back has to be able to reach 0.
//
// Only a string whose FIRST rune is wider than the budget makes the difference
// observable. Every other input has an earlier boundary for the walk-back to
// stop on, so it never gets near the loop bound. Stopping at 1 returns the lead
// byte of that rune on its own, which is not valid UTF-8 — the exact defect this
// package exists to prevent, restored by a number rather than by the loop.
func TestTruncateAtRuneBoundaryDropsARuneWiderThanTheBudget(t *testing.T) {
	// gClef is four bytes, declared in textutil_test.go.
	input := gClef + "abc"

	for budget := 1; budget < len(gClef); budget++ {
		got := TruncateAtRuneBoundary(input, budget)
		if !utf8.ValidString(got) {
			t.Errorf("budget=%d: result is not valid UTF-8: %q — the walk-back stopped "+
				"before it reached the start of the string", budget, got)
		}
		if got != "" {
			t.Errorf("budget=%d: got %q, want \"\" — no whole rune fits in this budget", budget, got)
		}
	}
}

// TestTruncateAtRuneBoundaryCutsExactlyAtAnAlignedBudget pins where an aligned
// cut lands, by marking the byte at the budget rather than counting bytes.
func TestTruncateAtRuneBoundaryCutsExactlyAtAnAlignedBudget(t *testing.T) {
	const budget = 10
	input := strings.Repeat("a", budget-1) + budgetMarker + pastBudget + strings.Repeat("b", 5)

	want := strings.Repeat("a", budget-1) + budgetMarker
	if got := TruncateAtRuneBoundary(input, budget); got != want {
		t.Errorf("TruncateAtRuneBoundary(_, %d) = %q, want %q — the cut did not land on "+
			"the budget", budget, got, want)
	}
}

// TestTruncateAtRuneBoundaryCutsAStringOneByteOverTheBudget straddles the
// fits-in-budget guard: N present, N+1 absent.
//
// A guard of `len(s) <= maxBytes+1` returns a string one byte over its budget
// whole, so the helper ships a value past the limit it exists to enforce. Only a
// pair of inputs sitting either side of the guard can see it; one input on
// either side alone passes against both spellings.
func TestTruncateAtRuneBoundaryCutsAStringOneByteOverTheBudget(t *testing.T) {
	const budget = 10
	fits := strings.Repeat("a", budget-1) + budgetMarker // exactly budget bytes
	over := fits + pastBudget                            // exactly one byte more

	if len(fits) != budget || len(over) != budget+1 {
		t.Fatalf(reachGuard+"fixture is %d and %d bytes, want %d and %d — the pair does "+
			"not straddle the guard", len(fits), len(over), budget, budget+1)
	}

	if got := TruncateAtRuneBoundary(fits, budget); got != fits {
		t.Errorf("a string of exactly the budget was cut: got %q, want %q", got, fits)
	}
	if got := TruncateAtRuneBoundary(over, budget); got != fits {
		t.Errorf("a string one byte over the budget: got %q, want %q — one byte over "+
			"must be cut, or the budget is not a budget", got, fits)
	}
}
