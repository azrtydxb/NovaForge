#!/usr/bin/env bash
# Sourced by merge_test.sh after proving the independent-review merge path.
verify_coverage_history() {
	local repo="coverage$RANDOM" dir="$WORK/coverage" result number phase proposal
	result="$(call POST "/orgs/$ORG/repos" "$A_TOKEN" "{\"name\":\"$repo\"}")"
	case "$result" in 2*) ;; *) fail "create coverage repository: $result" ;; esac
	git clone -q "http://$AUTHOR:$A_TOKEN@$GIT_IP:8081/$ORG/$repo.git" "$dir" 2>/dev/null || fail "clone coverage repository"
	git -C "$dir" config user.email "$AUTHOR@example.com"
	git -C "$dir" config user.name "$AUTHOR"
	mkdir -p "$dir/.novaforge/gates"
	printf 'name: tests\nrequired: true\n' >"$dir/.novaforge/gates/tests.yaml"
	printf 'module example.com/coverage\n\ngo 1.24\n' >"$dir/go.mod"
	printf 'package coverage\nfunc Add() int { return 1 }; func Sub() int { return 2 }\n' >"$dir/calc.go"
	printf 'package coverage\nimport "testing"\nfunc TestCalc(t *testing.T) { Add(); Sub() }\n' >"$dir/calc_test.go"
	git -C "$dir" add .
	git -C "$dir" commit -qm 'test: fully covered baseline'
	git -C "$dir" push -q origin HEAD:main || fail "push coverage baseline"
	git -C "$dir" checkout -qb coverage-probe
	git -C "$dir" push -q origin coverage-probe || fail "push coverage branch"
	result="$(call POST "/orgs/$ORG/repos/$repo/runs" "$A_TOKEN" '{"title":"Coverage evidence","source_ref":"coverage-probe","target_ref":"main"}')"
	case "$result" in 2*) ;; *) fail "open coverage run: $result" ;; esac
	number="$(printf '%s' "${result#* }" | python3 -c 'import json,sys; print(json.load(sys.stdin)["number"])')"
	for phase in baseline regression; do
		if [ "$phase" = regression ]; then
			printf 'package coverage\nimport "testing"\nfunc TestCalc(t *testing.T) { Add() }\n' >"$dir/calc_test.go"
			git -C "$dir" commit -qam 'test: exercise real coverage regression'
			git -C "$dir" push -q origin coverage-probe || fail "push lower coverage"
		fi
		result="$(call POST "/orgs/$ORG/repos/$repo/runs/$number/gates/evaluate" "$A_TOKEN" '{}')"
		case "$result" in 2*) ;; *) fail "evaluate $phase coverage: $result" ;; esac
		printf '%s' "${result#* }" | python3 -c '
import json,sys
expected="100.0%" if sys.argv[1]=="baseline" else "50.0%"
e=next(e for e in json.load(sys.stdin)["evaluations"] if e["gate"]=="tests")
assert e["status"]=="pass" and expected in e["detail"], e
' "$phase" || fail "tests gate did not measure $phase coverage"
	done
	result="$(call POST "/orgs/$ORG/repos/$repo/maintenance/scan" "$A_TOKEN" '{}')"
	case "$result" in 2*) ;; *) fail "coverage maintenance scan: $result" ;; esac
	proposal="$(curl -fsS -H "Authorization: Bearer $A_TOKEN" "$API/orgs/$ORG/repos/$repo/maintenance" | python3 -c '
import json,sys
for p in json.load(sys.stdin)["proposals"]:
    if "coverage dropped from 100.0% to 50.0%" in p["work_item_goal"]:
        assert not p["assignee_id"] and not p["decision"], "proposal bypassed approval"
        assert "previous evaluation" in p["work_item_goal"], "comparison lost its evidence"
        print(p["work_item_key"])
        break
')"
	[ -n "$proposal" ] || fail "tests-gate coverage produced no unapproved proposal: $result"
	ok "coverage proposal $proposal came from real tests-gate history"
}
