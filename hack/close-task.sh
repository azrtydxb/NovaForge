#!/usr/bin/env bash
# close-task.sh <task-id>  — evidence on stdin.
# Checks every acceptance box and writes the evidence block, then invokes the
# procoder quality controller. Only run after verifying each criterion.
set -euo pipefail
ID="$1"
F=".procoder/todo/${ID}.md"
[ -f "$F" ] || {
	echo "no such task file: $F" >&2
	exit 1
}
TMP="$(mktemp)"
cat >"$TMP"
EVFILE="$TMP" python3 - "$F" <<'PY'
import sys, os
p = sys.argv[1]
s = open(p).read()
s = s.replace("- [ ] ", "- [x] ")
ev = open(os.environ["EVFILE"]).read().strip()
head, _, _ = s.partition("## Evidence")
open(p, "w").write(head + "## Evidence\n\n" + ev + "\n")
PY
rm -f "$TMP"
prettier --write "$F" >/dev/null 2>&1 || true
"/Users/pascal/.claude/plugins/cache/procoder/procoder/3.6.0/hooks/launcher.sh" todo close "$ID"
