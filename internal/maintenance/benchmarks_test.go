package maintenance

import (
	"context"
	"math"
	"strings"
	"testing"
)

func TestPerformanceRejectsNonfiniteComparisons(t *testing.T) {
	for _, result := range []BenchmarkResult{
		{Baseline: math.NaN(), Latest: 1},
		{Baseline: 1, Latest: math.Inf(1)},
		{Baseline: 1, Latest: -1},
		{Baseline: math.SmallestNonzeroFloat64, Latest: math.MaxFloat64},
	} {
		if findings, err := scanPerf(context.Background(), ScanInput{Benchmarks: []BenchmarkResult{result}}); err == nil || len(findings) != 0 {
			t.Fatalf("invalid comparison produced findings=%v err=%v", findings, err)
		}
	}
}

const benchmarkHeader = "goos: linux\ngoarch: arm64\ncpu: TestCPU\ngo-version: go1.26.1\nenvironment: benchmark-pool/image-digest/config-v1\npkg: example/bench\n"

func TestBenchmarkEvidenceValidation(t *testing.T) {
	valid := benchmarkHeader + "BenchmarkWork-2 100 10 ns/op 4 B/op 1 allocs/op 200 MB/s\nBenchmarkWork-2 100 30 ns/op 8 B/op 3 allocs/op 300 MB/s\nPASS\n"
	report, err := parseBenchmarkReport(valid)
	if err != nil || len(report.Values) != 3 || report.Values["BenchmarkWork-2 ns/op"] != 20 || report.Values["BenchmarkWork-2 B/op"] != 6 {
		t.Fatalf("median measurements: %+v, %v", report, err)
	}
	cases := map[string]string{
		"missing metadata":         strings.ReplaceAll(valid, "cpu: TestCPU\n", ""),
		"mixed packages":           valid + "pkg: example/other\n",
		"mixed environments":       valid + "environment: another-pool\n",
		"no successful completion": strings.ReplaceAll(valid, "PASS\n", ""),
		"failed invocation":        valid + "FAIL\n",
		"zero iterations":          strings.ReplaceAll(valid, "100 10", "0 10"),
		"nan":                      strings.ReplaceAll(valid, "10 ns/op", "NaN ns/op"),
		"infinity":                 strings.ReplaceAll(valid, "10 ns/op", "+Inf ns/op"),
		"negative":                 strings.ReplaceAll(valid, "10 ns/op", "-1 ns/op"),
		"truncated measurement":    strings.ReplaceAll(valid, "200 MB/s", "200"),
		"duplicate unit":           strings.ReplaceAll(valid, "200 MB/s", "200 ns/op"),
		"nonnumeric":               strings.ReplaceAll(valid, "10 ns/op", "unknown ns/op"),
		"unsupported direction":    benchmarkHeader + "BenchmarkWork-2 100 200 MB/s\nPASS\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseBenchmarkReport(body); err == nil {
				t.Fatal("invalid evidence accepted")
			}
		})
	}
}
