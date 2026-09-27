#!/usr/bin/env bash
# Sourced by merge_test.sh: use its real users, isolated org and API helpers.
verify_cross_fork_gates() {
	local parent="forkparent$RANDOM" fork="forkchild$RANDOM" dir="$WORK/fork-parent" child="$WORK/fork-child"
	local result fork_id number sha diff
	result="$(call POST "/orgs/$ORG/repos" "$A_TOKEN" "{\"name\":\"$parent\"}")"
	case "$result" in 2*) ;; *) fail "create fork parent: $result" ;; esac
	git clone -q "http://$AUTHOR:$A_TOKEN@$GIT_IP:8081/$ORG/$parent.git" "$dir" 2>/dev/null || fail "clone fork parent"
	git -C "$dir" config user.email "$AUTHOR@example.com"
	git -C "$dir" config user.name "$AUTHOR"
	mkdir -p "$dir/.novaforge/gates"
	printf 'name: tests\nrequired: true\n' >"$dir/.novaforge/gates/tests.yaml"
	printf 'module example.com/fork\n\ngo 1.24\n' >"$dir/go.mod"
	printf 'package fork\nfunc Add(a, b int) int { return a+b }\n' >"$dir/calc.go"
	printf 'package fork\nimport "testing"\nfunc TestAdd(t *testing.T) { if Add(2,3)!=5 { t.Fatal("wrong addition") } }\n' >"$dir/calc_test.go"
	git -C "$dir" add .
	git -C "$dir" commit -qm 'seed executable parent gate'
	git -C "$dir" push -q origin HEAD:main || fail "push fork parent"
	result="$(call POST "/orgs/$ORG/repos/$parent/forks" "$A_TOKEN" "{\"name\":\"$fork\"}")"
	case "$result" in 2*) ;; *) fail "fork repository: $result" ;; esac
	fork_id="$(printf '%s' "${result#* }" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')"
	git clone -q "http://$AUTHOR:$A_TOKEN@$GIT_IP:8081/$ORG/$fork.git" "$child" 2>/dev/null || fail "clone fork"
	git -C "$child" config user.email "$AUTHOR@example.com"
	git -C "$child" config user.name "$AUTHOR"
	printf 'package fork\nfunc Add(a, b int) int { return a-b }\n' >"$child/calc.go"
	printf 'name: tests\nrequired: false\n' >"$child/.novaforge/gates/tests.yaml"
	git -C "$child" commit -qam 'broken fork attempts to relax parent policy'
	git -C "$child" push -q origin HEAD:main || fail "push failing fork"
	result="$(call POST "/orgs/$ORG/repos/$parent/runs" "$A_TOKEN" "{\"title\":\"Fork gates\",\"source_ref\":\"main\",\"target_ref\":\"main\",\"source_repo\":\"$fork\"}")"
	case "$result" in 2*) ;; *) fail "open cross-fork run: $result" ;; esac
	number="$(printf '%s' "${result#* }" | python3 -c 'import json,sys; print(json.load(sys.stdin)["number"])')"
	result="$(call POST "/orgs/$ORG/repos/$parent/runs/$number/gates/evaluate" "$A_TOKEN" '{}')"
	case "$result" in 2*) ;; *) fail "evaluate failing fork: $result" ;; esac
	printf '%s' "${result#* }" | python3 -c 'import json,sys; e=next(e for e in json.load(sys.stdin)["evaluations"] if e["gate"]=="tests"); assert e["status"]=="fail",e' || fail "parent tests did not reject fork"
	diff="$(call GET "/orgs/$ORG/repos/$parent/diff?from=main&to=main&merge_base=true&source_repo=$fork_id" "$A_TOKEN")"
	case "$diff" in 2*) ;; *) fail "cross-fork diff: $diff" ;; esac
	printf '%s' "${diff#* }" | python3 -c 'import json,sys; assert "return a-b" in json.load(sys.stdin)["unified"]' || fail "cross-fork diff lost change"
	cp "$dir/calc.go" "$child/calc.go"
	cp "$dir/.novaforge/gates/tests.yaml" "$child/.novaforge/gates/tests.yaml"
	printf '\n// fixed contribution\n' >>"$child/calc.go"
	git -C "$child" commit -qam 'fix fork and restore policy'
	git -C "$child" push -q origin HEAD:main || fail "push fixed fork"
	sha="$(git -C "$child" rev-parse HEAD)"
	result="$(call POST "/orgs/$ORG/repos/$parent/runs/$number/reviews" "$B_TOKEN" "{\"verdict\":\"approve\",\"expected_source_sha\":\"$sha\"}")"
	case "$result" in 2*) ;; *) fail "approve fixed fork: $result" ;; esac
	result="$(call POST "/orgs/$ORG/repos/$parent/runs/$number/merge" "$A_TOKEN" '{"method":"merge"}')"
	case "$result" in 2*) ;; *) fail "passing fork could not merge: $result" ;; esac
	result="$(call GET "/orgs/$ORG/repos/$parent/blob/main/calc.go" "$A_TOKEN")"
	case "$result" in *"fixed contribution"*) ;; *) fail "fork merge did not land" ;; esac
	ok "cross-fork diff, parent executable gate, independent approval and main-to-main merge"
}
