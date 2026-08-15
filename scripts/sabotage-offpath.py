#!/usr/bin/env python3
"""Score the literals in logstack that are NOT on the truncation path.

scripts/sabotage-truncation.py scores 22 rows and every one of them is a
truncation boundary. That is the whole of its remit. The numbers next to those
cuts -- the chunk size, the status threshold, the scanner's line ceiling, the
file modes, the zero-guards on every metric, the table's column widths -- were
enumerated by the 186th nightly pass and never scored by anything.

This is their scorer. Same instrument, different targets: put each defect back
one at a time and record whether the suite goes red.

Two of the enumerated numbers cannot be reached at all. They are flag defaults
registered inside main(), and nothing in a test can call main(). They are
printed by name at the end of every run rather than dropped, because a scorer
that silently omits what it cannot measure reads exactly like one that measured
everything.

Verdicts
--------
CAUGHT      an assertion fired, or the tests drove production code into a panic
GUARD ONLY  only a reach-guard fired: the fixture broke, not the code under
            test. Scored as NOT caught -- crediting it would inflate the score
            by counting the suite detecting itself.
UNNOTICED   the suite stayed green. For a known-negative row that is the
            required result; for a real mutation it is a coverage gap.
VOID        a package did not build, so the run measured nothing. Never a pass:
            a compile error hides whether any test would have caught the
            behaviour.

Run from the repo root:  python3 scripts/sabotage-offpath.py
"""

import os
import re
import signal
import subprocess
import sys

FORMAT = "internal/format/format.go"
LOGPUSH = "cmd/openclaw-logpush/main.go"

TARGETS = (FORMAT, LOGPUSH)

PACKAGES = [
    "./internal/format/",
    "./cmd/openclaw-logpush/",
]

# The marker the test files prefix onto every reach-guard failure. Keep in sync
# with the reachGuard consts in the test files.
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


class Unscorable:
    """A literal that was enumerated and CANNOT be mutated into a verdict.

    Named rather than omitted. The whole argument of this family is that a
    number nothing moves is a number nothing holds; a scorer that quietly skips
    the ones it finds inconvenient makes exactly that claim about itself.
    """

    def __init__(self, name, path, reason):
        self.name = name
        self.path = path
        self.reason = reason


UNSCORABLE = [
    Unscorable(
        "logpush: -interval default (5s)",
        LOGPUSH,
        "flag.Duration registered inside main(). No test can call main(), so "
        "the value that ships is reachable only by running the binary. Pinning "
        "it needs a seam -- lift the flag set into a function that returns the "
        "parsed config -- which is a production change this sweep does not make.",
    ),
    Unscorable(
        "logpush: -logstack-url default (http://localhost:8088)",
        LOGPUSH,
        "Same shape as the interval. Worth more than the interval, because "
        "every other test supplies this URL explicitly: the port that ships is "
        "the one value in the file no test has ever used.",
    ),
    Unscorable(
        "logpush: the f.Seek error branch itself",
        LOGPUSH,
        "The fix stopped discarding this error, and the branch it added cannot "
        "be driven from a test. Seek(0, io.SeekCurrent) on an open, healthy "
        "file does not fail, and processFile opens the file itself, so a test "
        "has no way to hand it a descriptor that would. The WHENCE argument is "
        "scored as a real row; the error check guarding it is reachable only "
        "with a seam -- an injectable opener -- which this row does not "
        "assume is worth adding. Named so the gap is not read as coverage.",
    ),
]


MUTATIONS = [
    # ---- internal/format: the metric zero-guards ---------------------------
    # Six literals across two functions. Widen any one to `> 1` and a metric of
    # exactly 1 vanishes from the rendered line while every other value keeps
    # working -- so a fixture built on round numbers cannot see any of them.
    Mutation(
        "format Text: tokens-in zero-guard",
        FORMAT,
        "if entry.TokensIn > 0 || entry.TokensOut > 0 {",
        "if entry.TokensIn > 1 || entry.TokensOut > 0 {",
        True,
        None,
        "An entry with TokensIn=1 and TokensOut=0 loses its whole token clause. "
        "The two halves of this condition are separate literals and a fixture "
        "that sets both at once cannot tell them apart.",
    ),
    Mutation(
        "format Text: tokens-out zero-guard",
        FORMAT,
        "if entry.TokensIn > 0 || entry.TokensOut > 0 {",
        "if entry.TokensIn > 0 || entry.TokensOut > 1 {",
        True,
        None,
        "The other half. Needs the mirrored fixture -- TokensIn=0, "
        "TokensOut=1 -- and nothing else reddens it.",
    ),
    Mutation(
        "format Text: latency zero-guard",
        FORMAT,
        "if entry.LatencyMs > 0 {\n\t\tsb.WriteString(fmt.Sprintf(\" [latency: %dms]\", entry.LatencyMs))",
        "if entry.LatencyMs > 1 {\n\t\tsb.WriteString(fmt.Sprintf(\" [latency: %dms]\", entry.LatencyMs))",
        True,
        None,
        "A 1ms request renders with no latency at all. Matched with its body "
        "because Logfmt has the identical guard four lines of source later, and "
        "a bare pattern would mutate whichever came first.",
    ),
    Mutation(
        "format Logfmt: tokens_in zero-guard",
        FORMAT,
        "if entry.TokensIn > 0 {",
        "if entry.TokensIn > 1 {",
        True,
        None,
        "Logfmt spells all three guards separately, so each is its own "
        "boundary. A test for the Text rendering covers this one in appearance "
        "only.",
    ),
    Mutation(
        "format Logfmt: tokens_out zero-guard",
        FORMAT,
        "if entry.TokensOut > 0 {",
        "if entry.TokensOut > 1 {",
        True,
        None,
        "Second of Logfmt's three.",
    ),
    Mutation(
        "format Logfmt: latency_ms zero-guard",
        FORMAT,
        "if entry.LatencyMs > 0 {\n\t\tpairs = append(pairs, fmt.Sprintf(\"latency_ms=%d\", entry.LatencyMs))",
        "if entry.LatencyMs > 1 {\n\t\tpairs = append(pairs, fmt.Sprintf(\"latency_ms=%d\", entry.LatencyMs))",
        True,
        None,
        "Third of Logfmt's three, matched with its body for the same reason as "
        "the Text one.",
    ),
    # ---- internal/format: the table ----------------------------------------
    Mutation(
        "format Table: empty guard",
        FORMAT,
        "if len(entries) == 0 {",
        "if len(entries) <= 1 {",
        True,
        None,
        "A single-entry table renders as 'No logs found'. The emptiness guard "
        "is a boundary like any other and the one input that distinguishes it "
        "-- exactly one row -- is the input a fixture built to show a table "
        "least often uses.",
    ),
    Mutation(
        "format Table: separator width",
        FORMAT,
        'strings.Repeat("-", 100)',
        'strings.Repeat("-", 101)',
        True,
        None,
        "Cosmetic on its own, and it is the cheapest row in the file to hold: "
        "the separator is the one line of the table whose whole content is the "
        "number.",
    ),
    Mutation(
        "format Table: HEADER column widths",
        FORMAT,
        'sb.WriteString(fmt.Sprintf("%-20s %-10s %-15s %-10s %s\\n",\n\t\t"TIMESTAMP"',
        'sb.WriteString(fmt.Sprintf("%-21s %-10s %-15s %-10s %s\\n",\n\t\t"TIMESTAMP"',
        True,
        None,
        "The column widths are spelled TWICE, header and rows, and they have to "
        "agree. Moving one desynchronises the table -- the 184th's 'a budget "
        "spelled twice is two boundaries' applied to a format string.",
    ),
    Mutation(
        "format Table: ROW column widths",
        FORMAT,
        'sb.WriteString(fmt.Sprintf("%-20s %-10s %-15s %-10s %s\\n",\n\t\t\tentry.Timestamp',
        'sb.WriteString(fmt.Sprintf("%-21s %-10s %-15s %-10s %s\\n",\n\t\t\tentry.Timestamp',
        True,
        None,
        "The other spelling. A test that asserts the header alone, or the rows "
        "alone, holds neither: alignment is a relation between the two.",
    ),
    Mutation(
        "KNOWN NEGATIVE, format: empty guard rewritten",
        FORMAT,
        "if len(entries) == 0 {",
        "if len(entries) < 1 {",
        False,
        None,
        "Identical for every length. FORMAT's control: without it a suite that "
        "reddens on any edit at all to this file scores the same as one that "
        "detects the defect.",
    ),
    # ---- cmd/openclaw-logpush: the chunking --------------------------------
    # The richest pair in the file. The chunk size is spelled twice and the two
    # spellings have to agree; nothing tests the chunking at all, and the
    # consequence is duplicated or missing log rows in production.
    Mutation(
        "logpush: chunk STEP moved",
        LOGPUSH,
        "for i := 0; i < len(batch); i += 50 {",
        "for i := 0; i < len(batch); i += 51 {",
        True,
        None,
        "The step advances past one entry the end never reached, so one entry "
        "per chunk is silently dropped -- a log line that existed on disk and "
        "never arrives.",
    ),
    Mutation(
        "logpush: chunk END moved",
        LOGPUSH,
        "end := i + 50",
        "end := i + 51",
        True,
        None,
        "The mirror defect. The window now overlaps the next step, so one entry "
        "per chunk is pushed twice. Both mutations keep the suite green and "
        "both corrupt the log; only a test that counts what each chunk carried "
        "can tell them apart.",
    ),
    Mutation(
        "logpush: batch status threshold",
        LOGPUSH,
        "if resp.StatusCode >= 300 {",
        "if resp.StatusCode >= 301 {",
        True,
        None,
        "A 300 response is accepted as a successful push. The cursor then "
        "advances past entries logstack never stored.",
    ),
    # The card enumerated this call as "the max line length, spelled twice in
    # one call" — the same shape as the chunk size. Measured, it is not. The two
    # spellings are the scanner's initial buffer and its maximum token size, and
    # bufio grows the buffer up to the maximum, so the ceiling that ships is the
    # LARGER of the two. Moving either one DOWN by one changes nothing at any
    # line length; only moving one UP moves the ceiling. So the pair yields two
    # real rows and two known-negatives, not two real rows.
    Mutation(
        "logpush: scanner max token size raised",
        LOGPUSH,
        "scanner.Buffer(make([]byte, 1024*1024), 1024*1024)",
        "scanner.Buffer(make([]byte, 1024*1024), 1024*1024+1)",
        True,
        None,
        "One byte up and bufio grows the buffer past its declared size, so a "
        "line the ceiling is meant to reject is scanned. This is the ceiling "
        "whose failure mode is filed separately as a swallowed scanner.Err(); "
        "scoring the number is worth more than the others here because the "
        "consequence is already known.",
    ),
    Mutation(
        "logpush: scanner initial buffer raised",
        LOGPUSH,
        "scanner.Buffer(make([]byte, 1024*1024), 1024*1024)",
        "scanner.Buffer(make([]byte, 1024*1024+1), 1024*1024)",
        True,
        None,
        "The other spelling, moved the only direction that is observable. The "
        "buffer starts one byte longer, so the same over-ceiling line now fits "
        "and is scanned.",
    ),
    Mutation(
        "KNOWN NEGATIVE, logpush: scanner max token size lowered",
        LOGPUSH,
        "scanner.Buffer(make([]byte, 1024*1024), 1024*1024)",
        "scanner.Buffer(make([]byte, 1024*1024), 1024*1024-1)",
        False,
        None,
        "Behaviour-preserving, and it is the row that corrects the card. bufio "
        "errors when the buffer is full AND its length has reached the maximum; "
        "the buffer here is already 1MB, so lowering the maximum to 1MB-1 "
        "leaves the same full buffer failing the same check. A scorer that "
        "filed this as a real defect would report a permanent hole nothing can "
        "close.",
    ),
    Mutation(
        "KNOWN NEGATIVE, logpush: scanner initial buffer lowered",
        LOGPUSH,
        "scanner.Buffer(make([]byte, 1024*1024), 1024*1024)",
        "scanner.Buffer(make([]byte, 1024*1024-1), 1024*1024)",
        False,
        None,
        "Behaviour-preserving for the mirrored reason: a buffer short of the "
        "maximum is grown to it on demand, so the ceiling is unchanged.",
    ),
    # ---- logpush: what happens when the ceiling above is HIT -----------------
    # The four rows above score the ceiling's VALUE. These score its failure
    # mode, which was filed as 603e3ded and unscored by anything until the fix
    # landed: scanner.Err() was never read, so an over-long line ended the scan
    # exactly as a clean EOF does and the dropped entry was invisible.
    #
    # Note what is NOT here: a row that propagates the error instead of logging
    # it. That is not a defect to catch, it is the open policy question 6fbf83b3
    # asks of three repos sharing this ceiling, so it is scored as a real row
    # below only because the SUITE must notice the change — not because the
    # answer is settled.
    Mutation(
        "logpush: scan error not reported at all",
        LOGPUSH,
        "\tif err := scanner.Err(); err != nil {",
        "\tif err := scanner.Err(); false {",
        True,
        "TestAnOverLongLineIsReportedInsteadOfPassingAsCleanEOF",
        "The original defect, put back. An over-long entry is dropped for good "
        "while processFile returns nil and poll() logs 'pushed N messages'. The "
        "run reports success and the loss is silent, which is the standing "
        "directive's first rule broken exactly.",
    ),
    Mutation(
        "logpush: scan error reported without naming the session",
        LOGPUSH,
        'log.Printf("openclaw-logpush: %s/%s: scan stopped at offset %d of %d after %d entries: %v — the over-long entry is skipped, later entries resume on the next poll",\n\t\t\tagent, sessionID, newOffset, info.Size(), count, err)',
        'log.Printf("openclaw-logpush: scan stopped: %v", err)',
        True,
        "TestAnOverLongLineIsReportedInsteadOfPassingAsCleanEOF",
        "Half a report is the failure mode worth scoring separately: the line "
        "appears, so a reader believes the case is handled, but it names "
        "neither the session that lost an entry nor where in the file it "
        "stopped. Nothing can be done with it.",
    ),
    Mutation(
        "logpush: scan error propagated instead of reported",
        LOGPUSH,
        "\tif err := scanner.Err(); err != nil {",
        "\tif err := scanner.Err(); err != nil {\n\t\treturn offset, count, err\n\t}\n\tif false {",
        True,
        "TestEntriesAfterAnOverLongLineArriveOnALaterPoll",
        "The change the card 603e3ded asked for literally, and the reason it "
        "was not made: poll() reads a non-nil error as 'do not advance the "
        "cursor', so the daemon re-reads the same over-long line every tick "
        "forever and every entry after it is lost permanently — converting a "
        "one-entry loss into a total stall. Whoever answers 6fbf83b3 must "
        "redden this row on purpose.",
    ),
    Mutation(
        "logpush: scan error reported on every file",
        LOGPUSH,
        "\tif err := scanner.Err(); err != nil {",
        "\tif err := scanner.Err(); true {",
        True,
        "TestACleanFileLogsNothing",
        "A report that fires on every ordinary poll reports nothing. Scored "
        "because the value of the fix is entirely in the line MEANING something "
        "when it appears, and a suite that only checks the error case cannot "
        "tell a working report from a stuck one.",
    ),
    Mutation(
        "logpush: cursor position read from the wrong whence",
        LOGPUSH,
        "newOffset, err := f.Seek(0, io.SeekCurrent)",
        "newOffset, err := f.Seek(0, io.SeekStart)",
        True,
        None,
        "The cursor resets to the top of the file on every poll, so the daemon "
        "re-pushes the whole session forever. Scored here rather than with the "
        "ceiling rows because it is the same Seek whose error the fix stopped "
        "discarding.",
    ),
    Mutation(
        "logpush: cursor DIRECTORY mode",
        LOGPUSH,
        "os.MkdirAll(filepath.Dir(path), 0755)",
        "os.MkdirAll(filepath.Dir(path), 0700)",
        True,
        None,
        "The mode the cursor directory ships with. Moved by one permission "
        "group rather than one bit: 0754 differs from 0755 only in a bit no "
        "directory listing distinguishes, and the point of the row is that the "
        "value is held, not that a mutation is minimal.",
    ),
    Mutation(
        "logpush: cursor FILE mode",
        LOGPUSH,
        "os.WriteFile(path, data, 0644)",
        "os.WriteFile(path, data, 0640)",
        True,
        None,
        "Same for the cursor file itself.",
    ),
    Mutation(
        "logpush: author capture-index guard",
        LOGPUSH,
        "if m := authorRe.FindStringSubmatch(text); len(m) > 1 {",
        "if m := authorRe.FindStringSubmatch(text); len(m) > 2 {",
        True,
        None,
        "The regexp has exactly one capture group, so a match is always length "
        "2 and this guard is the only thing that reads it. Widen it and every "
        "inbound message is attributed to 'user' regardless of its [name] "
        "prefix.",
    ),
    Mutation(
        "KNOWN NEGATIVE, logpush: status threshold rewritten",
        LOGPUSH,
        "if resp.StatusCode >= 300 {",
        "if resp.StatusCode > 299 {",
        False,
        None,
        "Identical for every integer status. LOGPUSH's control, and the package "
        "where one is worth most: its tests drive whole files through "
        "processFile and an HTTP server, so they have the most ways to redden "
        "for reasons of their own.",
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
    package run. When a panic is seen, each test is run on its own so one crash
    cannot hide the others. Returns (names, was_truncated).
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
    forever.
    """
    repo = os.path.realpath(".")
    src = os.path.join(repo, LOGPUSH)
    test = os.path.join(repo, "cmd/openclaw-logpush/main_test.go")

    cases = [
        ("build failure -> VOID", "internal/format/format.go:12:3: undefined: nope\n", False, "VOID"),
        ("green -> UNNOTICED", "ok  \tgithub.com/x\t0.005s\n", True, "UNNOTICED"),
        (
            "assertion -> CAUGHT",
            "--- FAIL: TestA (0.00s)\n    format_test.go:48: table is misaligned\nFAIL\n",
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
            "    format_test.go:48: misaligned\nFAIL\n",
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


def control_summary(rows):
    """Report, per target file, what its known-negatives actually did.

    An aggregate "N/N known-negatives correctly unnoticed" is true and still
    hides the thing worth knowing: WHICH targets have a control.

    Three states, and the third is the one an aggregate erases:
      behaved  a control ran and stayed UNNOTICED, as it must
      FIRED    a control went red, so this target's score means nothing
      NONE     nothing was measured here; the score is unqualified
    """
    lines = []
    for path in TARGETS:
        negs = [r for r in rows if not r[0].real and r[0].path == path]
        if not negs:
            state = "NONE     no known-negative for this target; its score is unqualified"
        elif all(r[1] == "UNNOTICED" for r in negs):
            state = f"behaved  {len(negs)} known-negative(s) stayed unnoticed"
        else:
            fired = [r[0].name for r in negs if r[1] != "UNNOTICED"]
            state = f"FIRED    {', '.join(fired)} — this target's score means nothing"
        lines.append(f"       {path:34s} {state}")
    return lines


def self_test_control_summary():
    """Drive all three control states from literals.

    control_summary() is a reporter of last resort: if it can only ever say
    "behaved" it prints a reassuring line forever and nothing checks it.
    """

    def row(path, real, verdict):
        return (Mutation("probe", path, "a", "b", real, None, ""), verdict, [])

    cases = [
        ("no control -> NONE", [row(FORMAT, True, "CAUGHT")], "NONE"),
        ("control held -> behaved", [row(FORMAT, False, "UNNOTICED")], "behaved"),
        ("control red -> FIRED", [row(FORMAT, False, "CAUGHT")], "FIRED"),
    ]

    print("control-summary self-test")
    bad = 0
    for name, rows, want in cases:
        got = control_summary(rows)[0]
        ok = want in got
        bad += 0 if ok else 1
        print(f"  {'ok ' if ok else 'BAD'} {name:42s} -> {got.strip()[:60]}")
    if bad:
        print("\ncontrol reporting is wrong; not scoring anything with a broken instrument")
        sys.exit(1)
    print("  all three control states reachable and correct\n")


def main():
    for p in TARGETS:
        if not os.path.exists(p):
            sys.exit(f"run me from the repo root; missing {p}")

    self_test()
    self_test_control_summary()

    originals = {p: open(p).read() for p in TARGETS}

    # The source above holds a deliberately broken version of itself from the
    # write in the loop below until the restore after the suite runs. The
    # try/finally covers the ways out this function chooses to take, and it
    # covers SIGINT, because Python raises KeyboardInterrupt for that one. It
    # covers nothing else -- SIGTERM and SIGHUP kill the process between the
    # write and the restore and leave a mutated tracked source file behind,
    # looking exactly like ordinary uncommitted work. Those two are handled
    # below. SIGKILL cannot be caught by the process that receives it; it is the
    # one gap left here, and it is named rather than papered over.
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

    # A clean tree is not the same claim as the sources being unchanged.
    for p, text in originals.items():
        if open(p).read() != text:
            sys.exit(f"FAILED TO RESTORE {p}")
    built, out = run_tests()
    if not built or "FAIL" in out:
        sys.exit("suite is not green after restore:\n" + out)
    print("restored, suite green\n")

    real = [r for r in rows if r[0].real]
    negs = [r for r in rows if not r[0].real]
    caught = [r for r in real if r[1] == "CAUGHT"]
    held = [r for r in negs if r[1] == "UNNOTICED"]
    guard_only = [r for r in rows if r[1] == "GUARD ONLY"]
    missed_site = [r for r in rows if r[0].expect and r[0].expect not in r[2]]

    print(f"SCORE  {len(caught)}/{len(real)} real defects caught")
    print(f"       {len(held)}/{len(negs)} known-negatives correctly unnoticed")
    for line in control_summary(rows):
        print(line)
    print(f"       {len(guard_only)} rows caught by a reach-guard only")
    print(f"       {len(missed_site)} sites covered only by a neighbouring test")
    bad = [r for r in rows if r[1] in ("STALE PATTERN", "AMBIGUOUS PATTERN")]
    if bad:
        print(f"       {len(bad)} rows scored nothing (stale/ambiguous pattern)")

    print(f"\nNOT SCORED  {len(UNSCORABLE)} enumerated literals no mutation here can reach:")
    for u in UNSCORABLE:
        print(f"       {u.name}")
        print(f"           in {u.path}: {u.reason}")

    missed = [r for r in real if r[1] != "CAUGHT"]
    if missed:
        print(f"\nUNHELD  {len(missed)} numbers nothing in the suite moves:")
        for r in missed:
            print(f"       {r[1]:10s} {r[0].name}")

    ok = (
        len(caught) == len(real)
        and len(held) == len(negs)
        and not bad
        and not missed_site
    )
    sys.exit(0 if ok else 1)


if __name__ == "__main__":
    main()
