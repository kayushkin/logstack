package textutil

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// gClef is four bytes in UTF-8 (f0 9d 84 9e). A two-byte rune is not enough:
// only one of its two interior offsets is wrong, so a test that happens to pick
// the other one passes against a plain byte cut.
const gClef = "\U0001D11E"

// reachGuard marks a failure meaning the TEST could not reach the code it
// claims to exercise — a broken fixture, not a defect in the code under test.
//
// The sabotage scorer keys on this prefix to tell a reach-guard firing from an
// assertion firing. Without the distinction a mutation that merely breaks the
// fixture scores as "caught", which inflates the score: the suite would be
// credited with detecting itself.
const reachGuard = "REACH-GUARD: "

func straddleInput() string {
	return strings.Repeat("a", 40) + strings.Repeat(gClef, 20) + strings.Repeat("b", 40)
}

func cutStraddlesARune(s string, n int) bool {
	return n < len(s) && !utf8.RuneStart(s[n])
}

// TestTruncateAtRuneBoundarySlidesTheBudget slides the BUDGET across a fixed
// input.
//
// Sliding the input instead is the trap: trimming it from the front moves its
// start and its cut by the same amount, so the absolute cut position never
// moves and the loop runs one case N times. Trimming from the back moves the cut
// but splits a rune at the far end, so the test goes red on damage it caused
// itself.
func TestTruncateAtRuneBoundarySlidesTheBudget(t *testing.T) {
	input := straddleInput()
	straddled := 0

	for budget := 1; budget <= len(input); budget++ {
		if cutStraddlesARune(input, budget) {
			straddled++
		}
		got := TruncateAtRuneBoundary(input, budget)

		if !utf8.ValidString(got) {
			t.Errorf("budget=%d: result is not valid UTF-8: %q", budget, got)
			continue
		}
		// Validity alone is not falsifiable — a helper returning "" for
		// everything is valid UTF-8 within budget. Pin the length too.
		if len(got) > budget {
			t.Errorf("budget=%d: kept %d bytes, over budget", budget, len(got))
		}
		if len(input) > budget && len(got) <= budget-utf8.UTFMax {
			t.Errorf("budget=%d: kept only %d bytes, lost more than one rune's width", budget, len(got))
		}
		if !strings.HasPrefix(input, got) {
			t.Errorf("budget=%d: result is not a prefix of the input: %q", budget, got)
		}
	}

	// A loop is a claim that a range was covered, and nothing checks that claim
	// unless it is written down.
	if straddled == 0 {
		t.Fatalf(reachGuard+"no budget in 1..%d straddled a rune — the input or the loop "+
			"is wrong, so this test proved nothing", len(input))
	}
	t.Logf("%d of %d budgets straddled a rune", straddled, len(input))
}

// TestTruncateAtRuneBoundaryLeavesAnAlignedCutAlone is the known-negative
// control. It must pass against the unfixed byte cut too. Without it there is no
// way to tell "detects a straddle" from "detects non-ASCII input" — a test that
// is red at every offset is measuring the latter.
func TestTruncateAtRuneBoundaryLeavesAnAlignedCutAlone(t *testing.T) {
	input := straddleInput()
	checked := 0

	for budget := 1; budget < len(input); budget++ {
		if cutStraddlesARune(input, budget) {
			continue
		}
		checked++
		if got, want := TruncateAtRuneBoundary(input, budget), input[:budget]; got != want {
			t.Errorf("budget=%d: aligned cut changed: got %q want %q", budget, got, want)
		}
	}

	if checked == 0 {
		t.Fatal(reachGuard + "no aligned budget was exercised — the control proved nothing")
	}
	t.Logf("%d aligned budgets held", checked)
}

// TestTruncateAtRuneBoundaryNeverPanics covers the inputs a plain s[:n] is never
// given but this helper is: a budget wider than the string, and a negative one.
// Callers that cut to a fixed width with no length guard rely on the first.
func TestTruncateAtRuneBoundaryNeverPanics(t *testing.T) {
	cases := []struct {
		name string
		s    string
		n    int
		want string
	}{
		{"empty string", "", 10, ""},
		{"budget wider than the string", "abc", 8, "abc"},
		{"budget wider, multibyte", "日本", 8, "日本"},
		{"budget exactly the length", "abc", 3, "abc"},
		{"zero budget", "abc", 0, ""},
		{"negative budget", "abc", -1, ""},
		{"negative budget, multibyte", gClef + "abc", -3, ""},
		{"cut inside a three-byte rune", "日本語", 8, "日本"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := TruncateAtRuneBoundary(c.s, c.n)
			if got != c.want {
				t.Errorf("TruncateAtRuneBoundary(%q, %d) = %q, want %q", c.s, c.n, got, c.want)
			}
			if !utf8.ValidString(got) {
				t.Errorf("TruncateAtRuneBoundary(%q, %d) = %q, not valid UTF-8", c.s, c.n, got)
			}
		})
	}
}
