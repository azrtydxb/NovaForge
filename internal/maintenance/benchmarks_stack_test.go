package maintenance_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/benchmark/parse"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/blobstore"
	"github.com/novaforge/novaforge/internal/ci"
)

// Store actual benchmark output for two code versions, not made-up regression
// numbers. A one-iteration benchmark can include runtime allocations, so the
// assertion retains the actual measurements and exact intended run identities,
// rather than assuming the requested slice size is the measured B/op value.
func seedBenchmarkHistory(t *testing.T, store *ci.Store, arts *ci.ArtifactStore, blobs *blobstore.Client, org, repo uuid.UUID) benchmarkEvidence {
	t.Helper()
	ctx := context.Background()
	version, err := exec.Command("go", "env", "GOVERSION").Output()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module probe.example/benchmark\ngo 1.24\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "bench_test.go")
	var evidence benchmarkEvidence
	for _, sample := range []struct {
		size                     int
		ref, status, environment string
	}{
		{16, "main", "success", "integration-fixture"},
		{32, "main", "success", "integration-fixture"},
		{64, "main", "success", "different-hardware-pool"},
		{128, "feature", "success", "integration-fixture"},
		{256, "main", "failure", "integration-fixture"},
		{4096, "main", "success", "integration-fixture"},
		{512, "main", "failure", "integration-fixture"},
		{1024, "feature", "success", "integration-fixture"},
	} {
		source := fmt.Sprintf("package probe\nimport \"testing\"\nvar sink []byte\nfunc BenchmarkAlloc(b *testing.B) { for i:=0;i<b.N;i++ { sink=make([]byte,%d) } }\n", sample.size)
		if err := os.WriteFile(file, []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("go", "test", "-run=^$", "-bench=BenchmarkAlloc", "-benchmem", "-benchtime=1x")
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("benchmark: %v: %s", err, out)
		}
		body := "go-version: " + strings.TrimSpace(string(version)) + "\nenvironment: " + sample.environment + "\n" + string(out)
		run, _, err := store.CreateRun(ctx, ci.Run{OrgID: org, RepoID: repo, CommitSHA: fmt.Sprintf("%040x", sample.size), Ref: "refs/heads/" + sample.ref, Status: sample.status})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.DeleteRun(context.Background(), run.ID) })
		if sample.size == 32 || sample.size == 4096 {
			measurement := allocationMeasurement(t, string(out))
			if sample.size == 32 {
				evidence.baseline, evidence.baselineRun = measurement, run.ID.String()
			} else {
				evidence.latest, evidence.latestRun = measurement, run.ID.String()
			}
		}
		job, err := store.CreateJob(ctx, ci.WorkflowJob{RunID: run.ID, Name: "benchmark", RunCmd: "go test -bench=.", Status: "success"})
		if err != nil {
			t.Fatal(err)
		}
		art, err := arts.Upload(ctx, job.ID, "benchmarks.txt", strings.NewReader(body), int64(len(body)))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = blobs.Delete(context.Background(), art.ObjectKey) })
	}
	return evidence
}

type benchmarkEvidence struct {
	baseline, latest       uint64
	baselineRun, latestRun string
}

func allocationMeasurement(t *testing.T, report string) uint64 {
	t.Helper()
	for _, line := range strings.Split(report, "\n") {
		b, err := parse.ParseLine(line)
		if err == nil && strings.HasPrefix(b.Name, "BenchmarkAlloc-") && b.Measured&parse.AllocedBytesPerOp != 0 {
			return b.AllocedBytesPerOp
		}
	}
	t.Fatalf("benchmark did not measure allocated bytes: %s", report)
	return 0
}
