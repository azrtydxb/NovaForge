#!/usr/bin/env bash
# Sourced by graph_test.sh: exercise the person-initiated production RPC chain.
verify_graph_maintenance() {
	local result proposals ready=""
	for _ in $(seq 1 12); do
		result="$(curl -fsS --max-time 120 -X POST -H "Authorization: Bearer $TOKEN" \
			-H 'Content-Type: application/json' -d '{}' \
			"$BASE/api/v1/orgs/$ORG/repos/$REPO/maintenance/scan")" || fail "graph maintenance scan failed"
		proposals="$(get maintenance)" || fail "read graph maintenance proposals"
		ready="$(printf '%s' "$proposals" | python3 -c '
import json,sys
items=json.load(sys.stdin)["proposals"]
dead=[p for p in items if "maintenance.go#unusedMaintenance" in p["work_item_goal"]]
docs=[p for p in items if "missing: maintenance.go#Removed" in p["work_item_goal"]]
for p in dead+docs:
    assert not p["assignee_id"] and not p["decision"], "graph proposal bypassed approval"
    assert "Source revision: "+sys.argv[1] in p["work_item_goal"], "lost pinned source evidence"
print("yes" if dead and docs else "no")
' "$MAIN_SHA")" || fail "invalid graph proposal evidence: $proposals"
		[ "$ready" = yes ] && break
		sleep 5
	done
	[ "$ready" = yes ] || fail "no unapproved graph/documentation proposals: $result"
	ok "pinned Go graph and explicit context references produce unapproved proposals"
}

verify_module_evidence_refresh() {
	local result ready=""
	# Changing only module metadata must refresh evidence for untouched Go
	# files. A new reference ensures an old deduplicated proposal cannot pass.
	printf '\n// maintenance evidence refresh\n' >>"$WORK/repo/go.mod"
	printf '[Second missing API](symbol:maintenance.go#RemovedAfterModuleChange)\n' >"$WORK/repo/.novaforge/context/refresh.md"
	git -C "$WORK/repo" add .
	git -C "$WORK/repo" commit -qm 'refresh root module context without changing Go source'
	git -C "$WORK/repo" push -q origin HEAD:main || fail "push module evidence refresh"
	local sha
	sha="$(git -C "$WORK/repo" rev-parse HEAD)"
	for _ in $(seq 1 12); do
		result="$(curl -fsS --max-time 120 -X POST -H "Authorization: Bearer $TOKEN" \
			-H 'Content-Type: application/json' -d '{}' \
			"$BASE/api/v1/orgs/$ORG/repos/$REPO/maintenance/scan")" || fail "module refresh scan failed"
		ready="$(get maintenance | python3 -c '
import json,sys
found=[p for p in json.load(sys.stdin)["proposals"] if "missing: maintenance.go#RemovedAfterModuleChange" in p["work_item_goal"]]
for p in found:
    assert not p["assignee_id"] and not p["decision"]
    assert "Source revision: "+sys.argv[1] in p["work_item_goal"]
print("yes" if found else "no")
' "$sha")" || fail "invalid refreshed graph evidence"
		[ "$ready" = yes ] && break
		sleep 5
	done
	[ "$ready" = yes ] || fail "module change left unchanged Go files stale: $result"
	ok "module-only change refreshes graph evidence for unchanged Go source"
}
