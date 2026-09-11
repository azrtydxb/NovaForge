#!/usr/bin/env bash
# merge-agent.sh <branch> — merge a parallel agent's worktree branch.
#
# go.mod and go.sum conflict on essentially every agent merge, because each
# agent adds dependencies independently. Resolving them by hand is error-prone;
# regenerating them from the merged source is both correct and deterministic.
set -euo pipefail
BRANCH="$1"
git merge --no-commit --no-ff "$BRANCH" || true
if git diff --name-only --diff-filter=U | grep -qE '^go\.(mod|sum)$'; then
	echo "resolving go.mod/go.sum by regeneration"
	git checkout --theirs go.mod 2>/dev/null || true
	git show HEAD:go.mod >go.mod
	rm -f go.sum
	go mod tidy
fi
if git diff --name-only --diff-filter=U | grep -q .; then
	echo "UNRESOLVED conflicts remain:" >&2
	git diff --name-only --diff-filter=U >&2
	exit 1
fi
go mod tidy
go build ./... || {
	echo "BUILD FAILED after merge" >&2
	exit 1
}
go vet ./... || {
	echo "VET FAILED after merge" >&2
	exit 1
}
git add -A
git commit --no-edit -m "Merge $BRANCH"
echo "merged $BRANCH cleanly"
