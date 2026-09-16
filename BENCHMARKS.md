# Performance maintenance evidence

A successful default-branch CI run can publish `benchmarks.txt` as a declared
artifact of a successful shell job. Each report contains **one package's**
standard `go test -bench` output, including its `PASS` marker, plus these headers:

- `goos`, `goarch`, `cpu`, `pkg`: normally printed by Go. If Go omits CPU identity,
  supply the actual CPU model or a stable fingerprint of hardware identity fields.
- `go-version`: the producing toolchain (`go env GOVERSION`).
- `environment`: an explicit compatibility identifier covering the immutable
  image, hardware pool, resource limits and benchmark configuration. Change this
  identifier when those settings change; do not use a generic placeholder.

Declare the report path in the workflow's `artifacts` list. For example, from a
single-package module, with `NF_BENCHMARK_ENV` set to the actual configuration:

```sh
set -eu
go test -run='^$' -bench=. -benchmem -count=3 >bench.out
{
  printf 'go-version: %s\n' "$(go env GOVERSION)"
  printf 'environment: %s\n' "${NF_BENCHMARK_ENV:?set the environment identity}"
  cat bench.out
} >benchmarks.txt
```

A missing CPU header must be supplied from actual hardware metadata before
publishing. Do not hide a failed benchmark command behind a successful pipeline.
`tests/e2e/benchmark_probe.sh` shows the ARM CPU-field fingerprint fallback.

## Comparison policy

The newest successful default-branch run is the latest eligible run. Missing
reports in that run are **unavailable**, not permission to reuse stale results.
For each latest measurement, maintenance selects the nearest earlier successful
default-branch run with matching job, package, benchmark name (including CPU-count
suffix), unit and all six metadata fields. Incompatible earlier runs are skipped.
Repeated observations within one report use their median.

Only `ns/op`, `B/op` and `allocs/op` are compared as lower-is-better. Throughput
and custom units are not assigned guessed direction semantics. Missing evidence,
malformed reports, unreadable artifacts and zero percentage baselines produce
scanner errors, not zero measurements or a clean bill of health. Independently
comparable measurements may still produce proposals when another measurement's
baseline is unavailable. A read failure does not fall back to a more distant run.

Reports are limited to 1 MiB each, 64 reports and 16 MiB per run. History inspection
has a 30-second deadline and a 100-run ceiling. CI metadata itself is not yet
paginated. Reports with conflicting package/environment metadata are refused;
use separate CI jobs for separate packages or configurations.

The existing default regression threshold is 10%. A finding retains both CI run
ids and measured values and creates an unassigned Work Item awaiting approval;
it never starts an agent or deploys a change. Metadata is producer-declared
compatibility evidence, not hardware attestation or a trusted merge gate.
Coverage comparisons and non-Go benchmark formats are separate work.
