package maintenance

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestGoTestHistoryRequiresNamedOutcomes(t *testing.T) {
	lines := []string{
		`{"Action":"pass","Package":"example/a","Test":"TestOne"}`,
		`{"Action":"fail","Package":"example/b","Test":"TestOne"}`,
		`{"Action":"fail","Package":"example/a"}`,
		`{"Action":"skip","Package":"example/a","Test":"TestSkip"}`,
		`{"Action":"output","Package":"example/a","Test":"TestOne","Output":"FAIL"}`,
		`{"Action":"pass","Test":"MissingPackage"}`,
		`{"Action":"fail","Package":"example/a","Test":"unfinished"`,
		"--- FAIL: TestText (0.00s)",
	}
	results := goTestResults(lines, "unit", "abc")
	if len(results) != 2 {
		t.Fatalf("want only named terminal events, got %+v", results)
	}
	if results[0].TestName != "unit: example/a/TestOne" || !results[0].Passed || results[0].CommitSHA != "abc" ||
		results[1].TestName != "unit: example/b/TestOne" || results[1].Passed {
		t.Fatalf("lost job/package/test identity or outcome: %+v", results)
	}
	findings, err := scanFlaky(context.Background(), ScanInput{JobResults: results})
	if err != nil || len(findings) != 0 {
		t.Fatalf("different packages must not imply flakiness: %+v, %v", findings, err)
	}
	if _, err := ciJobResults(context.Background(), nil, uuid.New(), "main"); err == nil {
		t.Fatal("unconfigured CI must not read as a clean history")
	}
}
