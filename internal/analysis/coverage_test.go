package analysis_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/novaforge/novaforge/internal/analysis"
)

func TestCoveragePreservesMeasuredPrecision(t *testing.T) {
	dir := probe(t, map[string]string{
		"go.mod":       goMod,
		"calc.go":      "package probe\nfunc A() int { return 1 }; func B() int { return 2 }; func C() int { return 3 }\n",
		"calc_test.go": "package probe\nimport \"testing\"\nfunc TestA(t *testing.T) { if A()!=1 { t.Fatal() } }\n",
	})
	res, err := analysis.Tests(context.Background(), analysis.DefaultExec, dir)
	if err != nil || res.Coverage != 100.0/3 {
		t.Fatalf("want measured 1/3 coverage, got %+v: %v", res, err)
	}
}

func TestCoverageDistinguishesZeroFromUnavailable(t *testing.T) {
	for _, code := range []string{"package probe\n", "package probe\nfunc A() int { return 1 }\n"} {
		dir := probe(t, map[string]string{"go.mod": goMod, "calc.go": code,
			"calc_test.go": "package probe\nimport \"testing\"\nfunc TestNothing(t *testing.T) {}\n"})
		res, err := analysis.Tests(context.Background(), analysis.DefaultExec, dir)
		if err != nil || !res.Passed || res.Coverage != 0 || res.CoverageAvailable != strings.Contains(code, "func A") {
			t.Fatalf("absence confused with measured zero: %+v %v", res, err)
		}
	}
}

func TestLostCoverageProfileIsNotZero(t *testing.T) {
	dir := probe(t, map[string]string{
		"go.mod":       goMod,
		"calc.go":      "package probe\nfunc A() int { return 1 }\n",
		"calc_test.go": "package probe\nimport \"testing\"\nfunc TestA(t *testing.T) { A() }\n",
	})
	for _, damage := range []func(string) error{os.Remove, func(path string) error { return os.Truncate(path, 0) }} {
		run := func(ctx context.Context, dir, name string, args ...string) ([]byte, int, error) {
			out, exit, err := analysis.DefaultExec(ctx, dir, name, args...)
			for _, arg := range args {
				if path, ok := strings.CutPrefix(arg, "-coverprofile="); ok {
					if err := damage(path); err != nil {
						t.Fatal(err)
					}
				}
			}
			return out, exit, err
		}
		if res, err := analysis.Tests(context.Background(), run, dir); err == nil {
			t.Fatalf("lost coverage reported as valid: %+v", res)
		}
	}
}
