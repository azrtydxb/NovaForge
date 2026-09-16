#!/usr/bin/env bash
# Sourced by work_ci_test.sh; shares its isolated repository and runner.
create_benchmark_fixture() {
	mkdir -p benchmark-fixture
	printf 'module example.com/benchmark\n\ngo 1.24\n' >benchmark-fixture/go.mod
	printf 'package benchmark\nimport "testing"\nvar sink []byte\nfunc BenchmarkAlloc(b *testing.B) { for i:=0;i<b.N;i++ { sink=make([]byte,32) } }\n' >benchmark-fixture/bench_test.go
	cat >benchmark-fixture/run.sh <<'SH'
set -eu
cd benchmark-fixture
go test -run='^$' -bench=BenchmarkAlloc -benchmem -benchtime=1x >bench.out
printf 'go-version: %s\nenvironment: analysis-image/%s/default-resources/go-benchmem-1x\n' "$(go env GOVERSION)" "$1" >benchmarks.txt
# Go omits cpu on some ARM hosts. Fingerprint actual implementer/part/revision
# fields, never an invented generic CPU name that would match unlike machines.
if ! grep -q '^cpu:' bench.out; then
    awk -F: '/CPU implementer|CPU architecture|CPU variant|CPU part|CPU revision/ {print $1 ":" $2}' /proc/cpuinfo | sort -u >cpu.txt
    test -s cpu.txt
    printf 'cpu: arm-identity-sha256/%s\n' "$(sha256sum cpu.txt | cut -d' ' -f1)" >>benchmarks.txt
fi
cat bench.out >>benchmarks.txt
SH
}

verify_benchmark_regression() {
	local baseline="$RUN_ID" latest="" state="" result proposal
	# Change the actual allocation, not a hand-written benchmark measurement.
	python3 - "$WORK/repo/benchmark-fixture/bench_test.go" <<'PY'
import pathlib,sys
p=pathlib.Path(sys.argv[1])
p.write_text(p.read_text().replace("make([]byte,32)", "make([]byte,4096)"))
PY
	git -C "$WORK/repo" add benchmark-fixture/bench_test.go
	git -C "$WORK/repo" commit -q -m 'perf: exercise allocation regression'
	git -C "$WORK/repo" push -q origin HEAD:main || fail "benchmark regression push failed"
	for _ in $(seq 1 90); do
		result="$(curl -fsS -H "$AUTH" "$API/runs" | python3 -c 'import json,sys;r=json.load(sys.stdin)["runs"];print(r[0]["id"]+" "+r[0]["status"] if r else "")')"
		latest="${result%% *}"
		state="${result##* }"
		if [ -n "$latest" ] && [ "$latest" != "$baseline" ]; then
			case "$state" in success | failure) break ;; esac
		fi
		sleep 3
	done
	[ -n "$latest" ] && [ "$latest" != "$baseline" ] && [ "$state" = success ] || fail "second benchmark run did not succeed: $result"
	result="$(curl -fsS -X POST -H "$AUTH" "$MAINT/scan")" || fail "benchmark maintenance scan failed"
	proposal="$(curl -fsS -H "$AUTH" "$MAINT" | python3 -c '
import json,sys
for p in json.load(sys.stdin)["proposals"]:
    if "BenchmarkAlloc" in p["work_item_goal"] and "B/op regressed" in p["work_item_goal"]:
        assert "(32 -> 4096)" in p["work_item_goal"], "wrong measured allocation comparison"
        assert not p["assignee_id"] and not p["decision"], "proposal must await approval"
        print(p["work_item_key"])
        break
')"
	[ -n "$proposal" ] || fail "CI benchmarks produced no unapproved performance proposal: $result"
	ok "performance proposal $proposal came from two real benchmark runs"
}
