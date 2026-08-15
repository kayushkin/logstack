package main

import (
	"errors"
	"flag"
	"path/filepath"
	"testing"
	"time"
)

// The flag defaults — the last two literals in main.go that nothing could move.
//
// scripts/sabotage-offpath.py printed them under NOT SCORED on every run: they
// were registered inside main(), and nothing in a test can call main(). The
// scorer named them rather than dropping them, which is why they were still
// findable, but printing a number is not pinning it.
//
// The -logstack-url default is the one worth the seam. Every other test in this
// package supplies that URL explicitly — processFile takes it as a parameter,
// and the httptest fixtures next door all pass their own server's address — so
// the port that actually ships was the single value in the file no test had
// ever used. That is the 184th pass's rule exactly: a default every test
// supplies is not the value that ships.
//
// Note what is deliberately NOT reach-guarded below. parseFlags returning an
// error for an empty argument list is not a precondition of these tests, it is
// the behaviour under test; a mutation that made it fail must score as CAUGHT,
// and prefixing reachGuard onto that check would have classified the suite's
// one real catch as a broken fixture instead.

func TestFlagDefaultsAreTheValuesThatShip(t *testing.T) {
	c, err := parseFlags(nil)
	if err != nil {
		t.Fatalf("parseFlags(nil) returned %v, want no error — an empty command line is the ordinary way this binary starts", err)
	}

	// The port openclaw-logpush pushes to when the unit file names no URL.
	if want := "http://localhost:8088"; c.logstackURL != want {
		t.Errorf("-logstack-url default = %q, want %q", c.logstackURL, want)
	}
	// Every poll of every session file waits this long.
	if want := 5 * time.Second; c.interval != want {
		t.Errorf("-interval default = %v, want %v", c.interval, want)
	}
	// Empty on purpose: resolveOpenclawDirectory fills it from the home
	// directory, and TestOpenclawDirectoryDefaultsToOpenclawUnderHome pins
	// what it fills it with.
	if c.openclawDir != "" {
		t.Errorf("-openclaw-dir default = %q, want empty so the home-relative default applies", c.openclawDir)
	}
	// Both off: a first run pushes nothing historical and really does push.
	if c.backfill {
		t.Error("-backfill default = true, want false — the default run must not replay every session on disk")
	}
	if c.dryRun {
		t.Error("-dry-run default = true, want false — the default run must actually push")
	}
}

func TestFlagsOverrideTheirDefaults(t *testing.T) {
	c, err := parseFlags([]string{
		"-logstack-url", "http://example.invalid:9999",
		"-openclaw-dir", "/tmp/oc",
		"-interval", "90s",
		"-backfill",
		"-dry-run",
	})
	if err != nil {
		t.Fatalf("parseFlags returned %v, want no error", err)
	}

	if want := "http://example.invalid:9999"; c.logstackURL != want {
		t.Errorf("-logstack-url = %q, want %q", c.logstackURL, want)
	}
	if want := "/tmp/oc"; c.openclawDir != want {
		t.Errorf("-openclaw-dir = %q, want %q", c.openclawDir, want)
	}
	if want := 90 * time.Second; c.interval != want {
		t.Errorf("-interval = %v, want %v", c.interval, want)
	}
	if !c.backfill {
		t.Error("-backfill = false, want true")
	}
	if !c.dryRun {
		t.Error("-dry-run = false, want true")
	}
}

func TestOpenclawDirectoryDefaultsToOpenclawUnderHome(t *testing.T) {
	c, err := parseFlags(nil)
	if err != nil {
		t.Fatalf("parseFlags(nil) returned %v, want no error", err)
	}

	c = resolveOpenclawDirectory(c, "/home/someone")
	if want := filepath.Join("/home/someone", ".openclaw"); c.openclawDir != want {
		t.Errorf("openclawDir = %q, want %q", c.openclawDir, want)
	}
}

func TestAnExplicitOpenclawDirectoryIsNotOverwrittenByTheHomeDefault(t *testing.T) {
	c, err := parseFlags([]string{"-openclaw-dir", "/srv/openclaw"})
	if err != nil {
		t.Fatalf("parseFlags returned %v, want no error", err)
	}

	c = resolveOpenclawDirectory(c, "/home/someone")
	if want := "/srv/openclaw"; c.openclawDir != want {
		t.Errorf("openclawDir = %q, want %q — the flag was supplied and must win over the home-relative default", c.openclawDir, want)
	}
}

func TestCursorFilePathIsUnderTheHomeConfigDirectory(t *testing.T) {
	// The offsets that decide what has already been pushed. Move this path and
	// a restarted daemon finds no cursors, so every session re-pushes from
	// wherever the missing file leaves it.
	got := cursorFilePath("/home/someone")
	want := filepath.Join("/home/someone", ".config", "openclaw-logpush", "cursors.json")
	if got != want {
		t.Errorf("cursorFilePath = %q, want %q", got, want)
	}
}

func TestParseFlagsReturnsAnUnknownFlagRatherThanExiting(t *testing.T) {
	// This is what makes the seam usable at all. flag.ExitOnError — what the
	// package-level flag.CommandLine uses — would take this test binary down
	// with it here instead of returning, so the check is on the mechanism the
	// other tests in this file depend on, not on cosmetics.
	_, err := parseFlags([]string{"-no-such-flag"})
	if err == nil {
		t.Fatal("parseFlags accepted -no-such-flag, want an error returned to the caller")
	}
	if errors.Is(err, flag.ErrHelp) {
		t.Errorf("parseFlags returned ErrHelp for an unknown flag, want a parse error: %v", err)
	}
}
