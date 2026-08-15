#!/usr/bin/env python3
"""Score the floor on TestBoundedQueryDoesNotRetainTheCorpus by sabotage.

The retention test compares two heap deltas and asserts the bounded one is the
smaller. Both figures are only meaningful if the queries that produced them read
the rows the test assumes. When they do not, the comparison is noise against
noise and lands on either side at random -- which is how this test behaved for
eleven days while the seed corpus sat outside the query window, reported as an
unexplained flake.

This scorer answers two questions that have to be kept apart:

  1. Does the FLOOR earn its keep? Each row runs against the test file with the
     floor (MINE) and without it (PRIOR, the file as at ddef139). A row where
     both columns agree is a row the floor did not buy.
  2. Is the row a real coverage gap for the REPO, or only a vacuity inside this
     one test? The PKG column runs the whole package against PRIOR. A row that
     PKG catches is already covered by a sibling test, so closing it adds
     confidence in this test's own wording and nothing to the suite's reach.

Reporting (2) is the point. A floor that turns a vacuous pass into a loud
failure is worth having, but calling it a closed coverage gap when a
neighbouring test already fails on the same mutation would be a claim this
scorer exists to refuse.

Verdicts
--------
CAUGHT         an assertion in the test under scrutiny fired
FIXTURE-FLOOR  seedCorpus's own visibility floor fired first, so the test never
               ran. Detection, and named separately because it is the fixture
               refusing to seed rather than the test asserting -- that
               distinction is the whole finding for the aged-fixture row
UNNOTICED      the run stayed green. For a known-negative row that is required;
               for a real mutation it is a gap or a vacuity
VOID           the package did not build, so nothing was measured. Never a pass

Run from the repo root:  python3 scripts/sabotage-retention-floor.py
"""

import os
import re
import shutil
import subprocess
import sys
import tempfile

STORE = "internal/store/store.go"
TEST = "internal/store/store_test.go"
PACKAGE = "./internal/store/"
TARGET = "TestBoundedQueryDoesNotRetainTheCorpus"

# The commit whose store_test.go is this change's baseline: the corpus fix, with
# no floor on the retention measurement itself.
PRIOR_REV = "ddef139"

# seedCorpus's visibility floor. Matched on its message so a fixture refusal is
# never scored as the test under scrutiny doing the catching.
FIXTURE_FLOOR_MARKER = "the corpus is outside the window Query scans"


class Mutation:
    def __init__(self, name, path, old, new, real, note):
        self.name = name
        self.path = path  # STORE, or TEST for a fixture row
        self.old = old
        self.new = new
        self.real = real  # False => known-negative, must go UNNOTICED
        self.note = note


MUTATIONS = [
    Mutation(
        "the bounded query keeps one row instead of Limit",
        STORE,
        "keep = params.Offset + params.Limit",
        "keep = params.Offset + 1",
        True,
        "The row this floor was written for. A bounded query that returns far "
        "fewer rows than asked retains almost nothing, so the ratio assertion "
        "passes more comfortably the more broken the query is -- the test "
        "reports the property holding over a query that did almost no work. "
        "Only an assertion on the row count can see it.\n"
        "        The selector, not the trailing slice, is the lever that moves "
        "this count. `results = results[:params.Limit]` looks like the obvious "
        "mutation and is an EQUIVALENT MUTANT: keep is already Offset+Limit, so "
        "the slice never fires for Offset=0. The 132nd pass measured that over "
        "378 (limit, offset, filter) triples and found 0 divergences; scored "
        "here first, it came back UNNOTICED in all three columns and agreed.",
    ),
    Mutation(
        "the seed corpus is anchored to an absolute date again",
        TEST,
        "base := time.Now().Add(-span)",
        "base := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)",
        True,
        "The defect that produced the flake. Scored to establish WHERE it is "
        "now caught: if seedCorpus's floor fires, the flake is already dead on "
        "this branch and a floor inside the test is a backstop, not the fix.",
    ),
    Mutation(
        "Query materialises the whole window again",
        STORE,
        "selector := newEntrySelector(keep)",
        "selector := newEntrySelector(0)",
        True,
        "The regression this test exists to catch, restored. It must stay "
        "CAUGHT under the floor -- a floor that added assertions while "
        "weakening the original ratio would be a net loss, and this row is the "
        "control that would show it.",
    ),
    Mutation(
        "known-negative: the ratio is spelled as a division rather than a shift",
        TEST,
        "if bounded > unbounded/8 {",
        "if bounded*8 > unbounded {",
        False,
        "Behaviour-preserving at these magnitudes (bounded ~70KB, unbounded "
        "~29MB, no overflow). A scorer with no known-negative cannot tell a "
        "suite that detects the DEFECT from one that reddens whenever the "
        "source changes at all, so this row must go UNNOTICED.",
    ),
]


def read(path):
    with open(path, encoding="utf-8") as fh:
        return fh.read()


def write(path, text):
    with open(path, "w", encoding="utf-8") as fh:
        fh.write(text)


def run_tests(only=None):
    cmd = ["go", "test", "-count=1"]
    if only:
        cmd += ["-run", "^" + only + "$"]
    cmd.append(PACKAGE)
    proc = subprocess.run(cmd, capture_output=True, text=True)
    out = proc.stdout + proc.stderr
    built = "[build failed]" not in out and not re.search(r"^\S+\.go:\d+:\d+: ", out, re.M)
    return built, out


def classify(output, built):
    if not built:
        return "VOID"
    if "FAIL" not in output:
        return "UNNOTICED"
    if FIXTURE_FLOOR_MARKER in output:
        return "FIXTURE-FLOOR"
    return "CAUGHT"


def self_test():
    """Drive every verdict from a literal before trusting the classifier.

    A classifier that can never return FIXTURE-FLOOR would silently promote
    every fixture refusal to CAUGHT and report the aged-fixture row as proof
    the new floor works -- the exact conclusion this scorer is meant to test.
    """
    cases = [
        ("build failure -> VOID", "internal/store/store.go:12:3: undefined: nope\n", False, "VOID"),
        ("green -> UNNOTICED", "ok  \tgithub.com/kayushkin/logstack/internal/store\t7.4s\n", True, "UNNOTICED"),
        (
            "assertion -> CAUGHT",
            "--- FAIL: %s (7.20s)\n    store_test.go:470: Query(limit=100) returned 0 rows\nFAIL\n" % TARGET,
            True,
            "CAUGHT",
        ),
        (
            "fixture floor -> FIXTURE-FLOOR",
            "--- FAIL: %s (0.90s)\n    store_test.go:336: seeded 40000 entries but an unwindowed Query "
            "sees 0: %s, so every test built on this fixture would compare empty against empty\nFAIL\n"
            % (TARGET, FIXTURE_FLOOR_MARKER),
            True,
            "FIXTURE-FLOOR",
        ),
    ]
    bad = 0
    for name, output, built, want in cases:
        got = classify(output, built)
        if got != want:
            print("  classifier self-test FAILED: %s -> %s, want %s" % (name, got, want))
            bad += 1
    if bad:
        sys.exit("classifier is wrong; no score is trustworthy")
    print("  classifier self-test: %d/%d" % (len(cases), len(cases)))


def main():
    if not os.path.exists(STORE) or not os.path.exists(TEST):
        sys.exit("run from the repo root")

    print("Scoring the retention floor by sabotage")
    print("=" * 78)
    self_test()

    saved = tempfile.mkdtemp(prefix="sabotage-retention-")
    mine_test = read(TEST)
    store_src = read(STORE)
    write(os.path.join(saved, "store.go"), store_src)
    write(os.path.join(saved, "store_test.go"), mine_test)

    # PRIOR: the same file without the floor, taken from the baseline commit.
    prior = subprocess.run(
        ["git", "show", "%s:%s" % (PRIOR_REV, TEST)], capture_output=True, text=True
    )
    if prior.returncode != 0:
        sys.exit("could not read %s:%s -- %s" % (PRIOR_REV, TEST, prior.stderr.strip()))
    prior_test = prior.stdout
    if "unboundedRows" in prior_test:
        sys.exit("%s already contains the floor; PRIOR is not a baseline" % PRIOR_REV)

    variants = [("PRIOR", prior_test), ("MINE", mine_test)]
    rows = []

    try:
        for mut in MUTATIONS:
            verdicts = {}
            for label, test_src in variants:
                write(TEST, test_src)
                write(STORE, store_src)

                # A needle that matches zero times is indistinguishable from a
                # surviving mutation, and a needle that matches twice mutates
                # more than the row claims. Assert exactly one before writing.
                target_src = test_src if mut.path == TEST else store_src
                hits = target_src.count(mut.old)
                if hits != 1:
                    sys.exit(
                        "row %r: needle matches %d times in %s under %s; expected exactly 1"
                        % (mut.name, hits, mut.path, label)
                    )
                write(mut.path, target_src.replace(mut.old, mut.new))

                built, out = run_tests(only=TARGET)
                verdicts[label] = classify(out, built)

            # Is this a real gap for the repo, or a vacuity inside one test?
            write(TEST, prior_test)
            write(STORE, store_src)
            target_src = prior_test if mut.path == TEST else store_src
            write(mut.path, target_src.replace(mut.old, mut.new))
            built, out = run_tests()
            verdicts["PKG"] = classify(out, built)

            rows.append((mut, verdicts))
            print(
                "  %-52s PRIOR=%-14s MINE=%-14s PKG(prior)=%s"
                % (mut.name[:52], verdicts["PRIOR"], verdicts["MINE"], verdicts["PKG"])
            )
    finally:
        write(TEST, read(os.path.join(saved, "store_test.go")))
        write(STORE, read(os.path.join(saved, "store.go")))
        shutil.rmtree(saved, ignore_errors=True)

    print("=" * 78)

    bad = 0
    bought = 0
    for mut, v in rows:
        if not mut.real:
            if v["PRIOR"] != "UNNOTICED" or v["MINE"] != "UNNOTICED":
                print("  CONTROL FAILED: %s reddened a suite; it changes no behaviour" % mut.name)
                bad += 1
            continue
        if "VOID" in v.values():
            print("  VOID: %s -- a build error measured nothing" % mut.name)
            bad += 1
            continue
        if v["MINE"] == "UNNOTICED":
            print("  GAP: %s is unnoticed even with the floor" % mut.name)
            bad += 1
            continue
        if v["PRIOR"] == "UNNOTICED":
            bought += 1
            scope = "a vacuity inside this test" if v["PKG"] != "UNNOTICED" else "a real coverage gap"
            print("  the floor bought: %s (%s)" % (mut.name, scope))

    print("  rows the floor changed: %d of %d real rows" % (bought, sum(1 for m, _ in rows if m.real)))
    if bad:
        sys.exit("%d row(s) did not behave" % bad)
    print("  every row behaved")


if __name__ == "__main__":
    main()
