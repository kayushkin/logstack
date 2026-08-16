#!/usr/bin/env python3
"""Score internal/api/group_field_guard_test.go by breaking the code it claims to hold.

Each mutation is applied to a clean tree, `go test ./internal/api/...` is run, and the
tree is restored with `git checkout --`. CAUGHT means the suite went red.

Two rules this script obeys, both learned the expensive way on this box:

  * A mutation that breaks the BUILD is not a mutation. `go test` runs `go vet`,
    so deleting a `%s` verb from a format string leaves an orphaned argument and
    vet rejects it -- that scores as "caught" while testing nothing but vet.
    Delete a verb and its argument together, or do not touch the printf.

  * A suite that cannot report success is not a control. C1 is a no-op edit that
    MUST stay green; if it goes red the harness is broken and every CAUGHT above
    it is meaningless.

Run from the repo root:  python3 scripts/sabotage-group-field-guard.py
"""

import subprocess
import sys

HANDLER = "internal/api/handler.go"

GUARD = '''	if !store.IsGroupField(groupBy) {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "invalid group field",
			"valid": store.GroupFields,
		})
		return
	}
'''

MUTATIONS = [
    # M1 is the whole point of the test file: the defect it was written to catch.
    ("M1", "the guard is deleted -- an unknown field reaches the store",
     HANDLER, GUARD, ""),

    ("M2", "the guard is inverted -- only valid fields are refused",
     HANDLER, "if !store.IsGroupField(groupBy) {", "if store.IsGroupField(groupBy) {"),

    ("M3", "the guard consults a second, drifted copy of the field list",
     HANDLER, "if !store.IsGroupField(groupBy) {",
     'if groupBy != "orchestrator" && groupBy != "agent" && groupBy != "channel" {'),

    ("M4", "the refusal advertises a hardcoded list instead of the store's",
     HANDLER, '"valid": store.GroupFields,',
     '"valid": []string{"orchestrator", "agent"},'),

    ("M5", "the refusal loses the message that separates it from the binder's 400",
     HANDLER, '"error": "invalid group field",', '"error": "bad request",'),

    ("M6", "the refusal answers 500 instead of 400",
     HANDLER, '''		c.JSON(http.StatusBadRequest, gin.H{
			"error": "invalid group field",''',
     '''		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "invalid group field",'''),

    ("M7", "the handler groups by a fixed field, ignoring the path",
     HANDLER, "groups, err := h.store.Group(params, groupBy)",
     'groups, err := h.store.Group(params, "orchestrator")'),

    ("M8", "the response reports a group_by other than the one requested",
     HANDLER, '"group_by": groupBy,', '"group_by": "orchestrator",'),

    ("M9", "the route is not registered at all",
     HANDLER, 'api.GET("/logs/group/:field", h.GroupLogs)', ""),

    ("M10", "the route is registered at a different path",
     HANDLER, 'api.GET("/logs/group/:field", h.GroupLogs)',
     'api.GET("/logs/groupby/:field", h.GroupLogs)'),

    # Controls.
    ("C1", "CONTROL: a comment changes and nothing else -- MUST stay green",
     HANDLER, "// GroupLogs handles GET /api/v1/logs/group/:field",
     "// GroupLogs handles GET /api/v1/logs/group/:field (aggregation)"),
]


def run_suite():
    p = subprocess.run(["go", "test", "./internal/api/..."],
                       capture_output=True, text=True)
    return p.returncode == 0, (p.stdout + p.stderr)


def restore():
    subprocess.run(["git", "checkout", "--", HANDLER], check=True)


def main():
    if subprocess.run(["git", "status", "--porcelain", HANDLER],
                      capture_output=True, text=True).stdout.strip():
        sys.exit("%s is dirty -- commit first; this harness restores with git checkout." % HANDLER)

    ok, out = run_suite()
    if not ok:
        sys.exit("BASELINE IS RED. A red suite answers CAUGHT for everything.\n" + out)
    print("baseline: green\n")

    rows = []
    for tag, desc, path, old, new in MUTATIONS:
        src = open(path).read()
        if src.count(old) != 1:
            rows.append((tag, "ANCHOR MISSED (%d matches)" % src.count(old), desc))
            continue
        open(path, "w").write(src.replace(old, new, 1))
        ok, out = run_suite()
        restore()

        if "build failed" in out or "vet:" in out:
            verdict = "BUILD BROKEN -- not a mutation, re-aim it"
        elif tag.startswith("C"):
            verdict = "GREEN (control behaving)" if ok else "RED -- CONTROL FAILED"
        else:
            verdict = "CAUGHT" if not ok else "SURVIVED"
        rows.append((tag, verdict, desc))
        print("%-4s %-34s %s" % (tag, verdict, desc))

    caught = sum(1 for t, v, _ in rows if v == "CAUGHT")
    aimed = sum(1 for t, _, _ in rows if not t.startswith("C"))
    print("\n%d/%d aimed mutations caught" % (caught, aimed))
    bad = [r for r in rows if r[1] not in ("CAUGHT", "GREEN (control behaving)")]
    if bad:
        print("needs attention:")
        for t, v, d in bad:
            print("  %-4s %-34s %s" % (t, v, d))
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
