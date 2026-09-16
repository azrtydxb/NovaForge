package maintenance

import (
	"bufio"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/tools/benchmark/parse"
)

var benchmarkContextKeys = [...]string{"goos", "goarch", "cpu", "go-version", "environment", "pkg"}

type benchmarkReport struct {
	Context [6]string
	Values  map[string]float64
}

// parseBenchmarkReport accepts one package's standard Go benchmark output,
// plus explicit toolchain/environment headers. Missing context is not a match:
// comparing unlike hosts or toolchains would manufacture regressions.
func parseBenchmarkReport(body string) (benchmarkReport, error) {
	var report benchmarkReport
	samples := map[string][]float64{}
	passed := false
	scanner := bufio.NewScanner(strings.NewReader(body))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "FAIL" || strings.HasPrefix(line, "FAIL\t") || strings.HasPrefix(line, "--- FAIL:") {
			return report, fmt.Errorf("benchmark invocation failed")
		}
		if line == "PASS" {
			passed = true
		}
		for i, key := range benchmarkContextKeys {
			if value, ok := strings.CutPrefix(line, key+":"); ok {
				value = strings.TrimSpace(value)
				if report.Context[i] != "" && report.Context[i] != value {
					return report, fmt.Errorf("conflicting benchmark %s", key)
				}
				report.Context[i] = value
			}
		}
		if !strings.HasPrefix(line, "Benchmark") {
			continue
		}
		bench, err := parse.ParseLine(line)
		fields := strings.Fields(line)
		if err != nil || bench.N <= 0 || len(fields) < 4 || len(fields)%2 != 0 {
			return report, fmt.Errorf("malformed benchmark observation")
		}
		seen := map[string]bool{}
		for i := 2; i < len(fields); i += 2 {
			value, err := strconv.ParseFloat(fields[i], 64)
			unit := fields[i+1]
			if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || seen[unit] {
				return report, fmt.Errorf("invalid benchmark measurement for %s", unit)
			}
			seen[unit] = true
			// Throughput (MB/s) is higher-is-better; never feed it to the
			// lower-is-better detector. Custom units need explicit semantics.
			if unit == "ns/op" || unit == "B/op" || unit == "allocs/op" {
				key := bench.Name + " " + unit
				samples[key] = append(samples[key], value)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return report, fmt.Errorf("read benchmark report: %w", err)
	}
	for i, value := range report.Context {
		if value == "" {
			return report, fmt.Errorf("benchmark metadata %s unavailable", benchmarkContextKeys[i])
		}
	}
	if !passed || len(samples) == 0 {
		return report, fmt.Errorf("successful benchmark measurements unavailable")
	}
	report.Values = make(map[string]float64, len(samples))
	for name, values := range samples {
		sort.Float64s(values)
		n := len(values)
		median := values[n/2]
		if n%2 == 0 {
			median = values[n/2-1]/2 + median/2
		}
		report.Values[name] = median
	}
	return report, nil
}
