#!/usr/bin/env python3
"""Score the rune-boundary truncation tests by sabotage.

A test suite is a claim that it would notice a defect. Running it against
correct code does not test that claim. This puts each defect back, one at a
time, and records whether the suite went red.

There are two kinds of row. Helper rows break
internal/textutil.TruncateAtRuneBoundary. Call-site rows put the plain byte cut
back at ONE of the five sites that use it. Both are needed: a helper can score
full marks while a call site is fixed in appearance only, covered by the test
for the cut in the function next to it.

Verdicts
--------
CAUGHT      an assertion fired, or the tests drove production code into a panic
GUARD ONLY  only a reach-guard fired: the fixture broke, not the code under
            test. Scored as NOT caught — crediting it would inflate the score
            by counting the suite detecting itself.
UNNOTICED   the suite stayed green. For a known-negative row that is the
            required result; for a real mutation it is a coverage gap.
VOID        a package did not build, so the run measured nothing. Never a pass:
            a compile error hides whether any test would have caught the
            behaviour.

Run from the repo root:  python3 scripts/sabotage-truncation.py
"""

import os
import re
import signal
import subprocess
import sys

HELPER = "internal/textutil/textutil.go"
FORMAT = "internal/format/format.go"
LOGPUSH = "cmd/openclaw-logpush/main.go"

PACKAGES = [
    "./internal/textutil/",
    "./internal/format/",
    "./cmd/openclaw-logpush/",
]

# The marker the test files prefix onto every reach-guard failure. Keep in sync
# with the reachGuard consts in the three test files.
REACH_GUARD = "REACH-GUARD: "


class Mutation:
    """One defect put back into one file.

    `real` rows are defects the suite must catch. `real=False` rows are
    known-negatives: behaviour-preserving rewrites that must go UNNOTICED. A
    scorer with no known-negatives cannot tell a suite that detects the defect
    from one that fails whenever the source changes at all.
    """

    def __init__(self, name, path, old, new, real, expect, note):
        self.name = name
        self.path = path
        self.old = old
        self.new = new
        self.real = real
        self.expect = expect  # test this row should redden, or None
        self.note = note


MUTATIONS = [
    # ---- the shared helper -------------------------------------------------
    Mutation(
        "helper: walk-back never runs",
        HELPER,
        "for cut > 0 && !utf8.RuneStart(s[cut]) {",
        "for cut > len(s) && !utf8.RuneStart(s[cut]) {",
        True,
        None,
        "The original defect, written as a drifted comparison rather than a "
        "deletion. Deleting the loop would orphan the utf8 import, and go test "
        "runs vet, so the row would report a compile error instead of a score.",
    ),
    Mutation(
        "helper: walk-back runs the wrong way",
        HELPER,
        "for cut > 0 && !utf8.RuneStart(s[cut]) {",
        "for cut > 0 && utf8.RuneStart(s[cut]) {",
        True,
        None,
        "Inverted test: walks off a boundary onto one, i.e. cuts mid-rune "
        "exactly when the plain byte cut would not have.",
    ),
    Mutation(
        "helper: trims to nothing",
        HELPER,
        "return s[:cut]",
        "return s[:cut*0]",
        True,
        None,
        "Returns the empty string, which is valid UTF-8 and within budget, so "
        "only the length pin can catch it. cut stays referenced, so nothing is "
        "orphaned.",
    ),
    Mutation(
        "helper: short strings panic again",
        HELPER,
        "if len(s) <= maxBytes {",
        "if len(s) <= -1 {",
        True,
        "TestDryRunLogLineSurvivesAShortOrMultibyteSessionID",
        "Removes the protection the unguarded sessionID[:8] site depends on. "
        "That site has no length guard of its own, so this is the row proving "
        "the panic is fixed in the helper rather than worked around at the call "
        "site.",
    ),
    Mutation(
        "helper: KNOWN NEGATIVE, clamp boundary rewritten",
        HELPER,
        "if maxBytes <= 0 {",
        "if maxBytes < 1 {",
        False,
        None,
        "Identical for every integer. Must stay UNNOTICED, or the suite is "
        "reacting to the source changing rather than to behaviour changing.",
    ),
    Mutation(
        "helper: KNOWN NEGATIVE, fits-in-budget check rewritten",
        HELPER,
        "if len(s) <= maxBytes {",
        "if len(s) < maxBytes+1 {",
        False,
        None,
        "Also identical for every integer. Second known-negative, because one "
        "can pass by luck.",
    ),
    # ---- the five call sites ----------------------------------------------
    Mutation(
        "site: Summary, string content (50)",
        FORMAT,
        'content = textutil.TruncateAtRuneBoundary(v, 50) + "..."',
        'content = v[:50] + "..."',
        True,
        "TestSummaryCutsStringContentOnARuneBoundary",
        "One of three cuts in format.go. The other two keep the textutil "
        "import live, so this row scores behaviour rather than a build error.",
    ),
    Mutation(
        "site: Summary, map message (50)",
        FORMAT,
        'content = textutil.TruncateAtRuneBoundary(msg, 50) + "..."',
        'content = msg[:50] + "..."',
        True,
        "TestSummaryCutsMapMessageOnARuneBoundary",
        "The second cut inside the same function as the row above. Same budget, "
        "different content shape — and a single test would have covered it in "
        "appearance only.",
    ),
    Mutation(
        "site: Table, string content (40)",
        FORMAT,
        'content = textutil.TruncateAtRuneBoundary(v, 40) + "..."',
        'content = v[:40] + "..."',
        True,
        "TestTableCutsContentOnARuneBoundary",
        "A third site with a different budget, so a test hard-coded to 50 would "
        "miss it.",
    ),
    Mutation(
        "site: dry-run message text (80)",
        LOGPUSH,
        't = textutil.TruncateAtRuneBoundary(t, 80) + "..."',
        't = t[:80] + "..."',
        True,
        "TestDryRunTextCutStaysValidUTF8",
        "The card's logpush site. Driven through processFile, not the helper: "
        "the first draft of this test called the helper and passed against the "
        "unfixed cut.",
    ),
    Mutation(
        "site: dry-run session id (8, unguarded)",
        LOGPUSH,
        "agent, textutil.TruncateAtRuneBoundary(sessionID, 8), role, text",
        "agent, sessionID[:8], role, text",
        True,
        "TestDryRunLogLineSurvivesAShortOrMultibyteSessionID",
        "The site no scan in this sweep could find, because it has no length "
        "guard to match on. Reverting it restores both the panic on a short "
        "filename stem and the corruption on a multi-byte one.",
    ),
]


def run_tests(only=None, packages=None):
    """Run the tests. Returns (built, output)."""
    cmd = ["go", "test", "-count=1"]
    if only:
        cmd += ["-run", "^" + only + "$"]
    cmd += packages or PACKAGES
    proc = subprocess.run(cmd, capture_output=True, text=True)
    out = proc.stdout + proc.stderr
    built = "[build failed]" not in out and not re.search(r"^\S+\.go:\d+:\d+: ", out, re.M)
    return built, out


def failing_tests(output):
    """Test names that went red, so a row reports which test did the catching."""
    return sorted(set(re.findall(r"^\s*--- FAIL: (\S+)", output, re.M)))


def top_level_tests():
    """Every Test function in the packages, from the toolchain rather than a grep."""
    names = []
    for pkg in PACKAGES:
        proc = subprocess.run(
            ["go", "test", "-list", ".*", pkg], capture_output=True, text=True
        )
        for line in (proc.stdout + proc.stderr).splitlines():
            if line.startswith("Test"):
                names.append((line.strip(), pkg))
    return names


def complete_red_set(output):
    """The tests a mutation reddens, with the panic blind spot closed.

    A panic aborts the whole test binary, so every test ordered after the
    panicking one never runs and cannot appear in a red set read off a single
    package run. That understates coverage: the row prints a short list, and any
    claim read off it says "these tests do not catch this", which is wrong and
    wrong in the direction that sends the next reader off to write tests that
    already exist.

    When a panic is seen, each test is run on its own so one crash cannot hide
    the others. Returns (names, was_truncated).
    """
    reds = failing_tests(output)
    if "panic:" not in output:
        return reds, False

    complete = set(reds)
    for name, pkg in top_level_tests():
        _, out = run_tests(only=name, packages=[pkg])
        if failing_tests(out) or "panic:" in out:
            complete.add(name)
    return sorted(complete), True


def panic_frames(output):
    """Full paths of .go files named in a panic traceback, in order."""
    if "panic:" not in output:
        return []
    return re.findall(r"^\s+(/\S+\.go):\d+", output, re.M)


def classify(output, built):
    """Reduce a test run to one verdict.

    Panics are split by WHERE they happen. A panic whose first frame inside this
    repo is non-test source is the tests driving production code into a crash,
    and that is detection. A panic in the fixture is the test breaking itself.

    Frames are matched by FULL PATH, not basename: the runtime's own
    runtime/panic.go ends in .go and is not _test.go, so a basename filter
    promotes every fixture panic to real coverage.
    """
    if not built:
        return "VOID"

    if "panic:" in output:
        repo = os.path.realpath(".")
        for frame in panic_frames(output):
            real = os.path.realpath(frame)
            if not real.startswith(repo + os.sep):
                continue  # stdlib or runtime frame, keep looking
            if real.endswith("_test.go"):
                return "GUARD ONLY"  # the fixture crashed, not the code
            return "CAUGHT"  # production code crashed
        return "GUARD ONLY"  # the panic never entered this repo's own code

    if "FAIL" not in output:
        return "UNNOTICED"

    guard_fired = REACH_GUARD in output
    assertion_fired = False
    for line in output.splitlines():
        if re.match(r"^\s+\S+\.go:\d+: ", line) and REACH_GUARD not in line:
            assertion_fired = True
            break

    if assertion_fired:
        return "CAUGHT"
    if guard_fired:
        return "GUARD ONLY"
    return "CAUGHT"


def self_test():
    """Drive every verdict from a literal before trusting the classifier.

    A classifier is a new instrument sitting exactly where a wrong answer is
    invisible: one that can never return GUARD ONLY prints a clean score
    forever. Exercising it directly is cheap and does not depend on the suite
    being able to reach each branch.
    """
    repo = os.path.realpath(".")
    src = os.path.join(repo, LOGPUSH)
    test = os.path.join(repo, "cmd/openclaw-logpush/main_test.go")

    cases = [
        ("build failure -> VOID", "internal/format/format.go:12:3: undefined: nope\n", False, "VOID"),
        ("green -> UNNOTICED", "ok  \tgithub.com/x\t0.005s\n", True, "UNNOTICED"),
        (
            "assertion -> CAUGHT",
            "--- FAIL: TestA (0.00s)\n    format_test.go:48: summary is not valid UTF-8\nFAIL\n",
            True,
            "CAUGHT",
        ),
        (
            "reach-guard alone -> GUARD ONLY",
            "--- FAIL: TestA (0.00s)\n    main_test.go:85: " + REACH_GUARD + "processFile: boom\nFAIL\n",
            True,
            "GUARD ONLY",
        ),
        (
            "guard + assertion -> CAUGHT",
            "--- FAIL: TestA (0.00s)\n    main_test.go:85: " + REACH_GUARD + "boom\n"
            "    format_test.go:48: not valid UTF-8\nFAIL\n",
            True,
            "CAUGHT",
        ),
        (
            "panic in production source -> CAUGHT",
            "panic: runtime error: slice bounds out of range [:8]\n"
            "\t/usr/lib/go/src/runtime/panic.go:860 +0x13a\n"
            "\t" + src + ":328 +0xb31\n"
            "\t" + test + ":78 +0x38\nFAIL\n",
            True,
            "CAUGHT",
        ),
        (
            "panic in the fixture -> GUARD ONLY",
            "panic: runtime error: index out of range\n"
            "\t/usr/lib/go/src/runtime/panic.go:860 +0x13a\n"
            "\t" + test + ":161 +0x38\nFAIL\n",
            True,
            "GUARD ONLY",
        ),
    ]

    print("classifier self-test")
    seen, bad = set(), 0
    for name, output, built, want in cases:
        got = classify(output, built)
        seen.add(got)
        if got != want:
            bad += 1
        print(f"  {'ok ' if got == want else 'BAD'} {name:42s} -> {got}")

    missing = {"CAUGHT", "GUARD ONLY", "UNNOTICED", "VOID"} - seen
    if missing:
        print(f"  BAD unreachable verdicts: {sorted(missing)}")
        bad += 1
    if bad:
        print("\nclassifier is wrong; not scoring anything with a broken instrument")
        sys.exit(1)
    print("  all four verdicts reachable and correct\n")


def main():
    for p in (HELPER, FORMAT, LOGPUSH):
        if not os.path.exists(p):
            sys.exit(f"run me from the repo root; missing {p}")

    self_test()

    originals = {p: open(p).read() for p in (HELPER, FORMAT, LOGPUSH)}

    # The source above holds a deliberately broken version of itself from the
    # write in the loop below until the restore after the suite runs. The
    # try/finally covers the ways out this function chooses to take -- and it
    # covers SIGINT too, because Python raises KeyboardInterrupt for that one and
    # the finally is on the way out. It covers nothing else. Measured across all
    # five scorers in this sweep: SIGTERM and SIGHUP kill the process between the
    # write and the restore, leaving the mutated file behind as ordinary-looking
    # uncommitted work -- a semantic edit to a tracked source file, which
    # `git status` reports the same way it reports real work in progress, and
    # which this box's standing rule tells the next agent not to throw away.
    #
    # So the one signal a finally covers is the one you press by hand while
    # watching, and the ones it misses are exactly what a wall-clock cap, systemd
    # and a process-group kill send at an unattended run. A finally reads as
    # covered and, checked the obvious way with Ctrl-C, tests as covered.
    #
    # SIGKILL cannot be caught by the process that receives it. It is the one gap
    # left here, and it is named rather than papered over.
    previous_handlers = {}

    def restore_and_reraise(signum, frame):
        for path, text in originals.items():
            open(path, "w").write(text)
        signal.signal(signum, previous_handlers[signum])
        os.kill(os.getpid(), signum)

    for sig in (signal.SIGINT, signal.SIGTERM, signal.SIGHUP):
        previous_handlers[sig] = signal.signal(sig, restore_and_reraise)

    built, out = run_tests()
    if not built:
        sys.exit("baseline does not build:\n" + out)
    if "FAIL" in out:
        sys.exit("baseline is already red; fix that before scoring:\n" + out)
    print("baseline green\n")

    rows = []
    try:
        for m in MUTATIONS:
            base = originals[m.path]
            if base.count(m.old) != 1:
                verdict = "STALE PATTERN" if m.old not in base else "AMBIGUOUS PATTERN"
                rows.append((m, verdict, []))
                print(f"{verdict:14s} {m.name}")
                print("               pattern not uniquely found — a stale pattern")
                print("               mutates nothing and scores a bogus UNNOTICED\n")
                continue

            open(m.path, "w").write(base.replace(m.old, m.new, 1))
            built, out = run_tests()
            verdict = classify(out, built)
            reds, truncated = complete_red_set(out)
            rows.append((m, verdict, reds))
            open(m.path, "w").write(base)

            want = "CAUGHT" if m.real else "UNNOTICED"
            ok = verdict == want
            print(f"{verdict:14s} {m.name}")
            print(f"               want {want}  {'ok' if ok else '<-- WRONG'}")
            if reds:
                note = " (completed per-test; a panic had hidden the rest)" if truncated else ""
                print(f"               red: {', '.join(reds)}{note}")
            if m.expect:
                hit = m.expect in reds
                print(f"               expected {m.expect} red: {'yes' if hit else 'NO <-- the site is covered only by its neighbour'}")
            print(f"               {m.note}\n")
    finally:
        for p, text in originals.items():
            open(p, "w").write(text)
        for sig, handler in previous_handlers.items():
            signal.signal(sig, handler)

    # A clean tree is not the same claim as the fix still being present.
    for p, text in originals.items():
        if open(p).read() != text:
            sys.exit(f"FAILED TO RESTORE {p}")
    helper = open(HELPER).read()
    if "utf8.RuneStart" not in helper:
        sys.exit("the fix is NOT present after the run")
    if open(FORMAT).read().count("textutil.TruncateAtRuneBoundary") != 3:
        sys.exit("format.go does not have its 3 fixed sites after the run")
    if open(LOGPUSH).read().count("textutil.TruncateAtRuneBoundary") != 2:
        sys.exit("main.go does not have its 2 fixed sites after the run")
    built, out = run_tests()
    if not built or "FAIL" in out:
        sys.exit("suite is not green after restore:\n" + out)
    print("restored, all 5 sites asserted present, suite green\n")

    real = [r for r in rows if r[0].real]
    negs = [r for r in rows if not r[0].real]
    caught = [r for r in real if r[1] == "CAUGHT"]
    held = [r for r in negs if r[1] == "UNNOTICED"]
    guard_only = [r for r in rows if r[1] == "GUARD ONLY"]
    missed_site = [r for r in rows if r[0].expect and r[0].expect not in r[2]]

    print(f"SCORE  {len(caught)}/{len(real)} real defects caught")
    print(f"       {len(held)}/{len(negs)} known-negatives correctly unnoticed")
    print(f"       {len(guard_only)} rows caught by a reach-guard only")
    print(f"       {len(missed_site)} sites covered only by a neighbouring test")
    bad = [r for r in rows if r[1] in ("STALE PATTERN", "AMBIGUOUS PATTERN")]
    if bad:
        print(f"       {len(bad)} rows scored nothing (stale/ambiguous pattern)")
    ok = (
        len(caught) == len(real)
        and len(held) == len(negs)
        and not bad
        and not missed_site
    )
    sys.exit(0 if ok else 1)


if __name__ == "__main__":
    main()
