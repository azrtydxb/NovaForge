package maintenance

import (
	"context"
	"fmt"
	"io"
	"strings"

	civ1 "github.com/novaforge/novaforge/gen/novaforge/ci/v1"
)

// Benchmark evidence is opt-in through one benchmarks.txt artifact per shell
// job. Bound both advertised and streamed sizes; a metadata limit alone does
// not stop a corrupt object from exhausting the maintenance service's memory.
const maxBenchmarkBytes = 1 << 20

func benchmarkArtifacts(ctx context.Context, client civ1.CIServiceClient, runID string) (map[string]benchmarkReport, error) {
	detail, err := client.GetRun(ctx, &civ1.GetRunRequest{Id: runID})
	if err != nil {
		return nil, err
	}
	jobs := map[string]string{}
	for _, job := range detail.GetJobs() {
		if job.GetStatus() == "success" && job.GetAgentRole() == "" {
			jobs[job.GetId()] = job.GetName()
		}
	}
	arts, err := client.ListArtifacts(ctx, &civ1.ListArtifactsRequest{RunId: runID})
	if err != nil {
		return nil, err
	}
	reports := map[string]benchmarkReport{}
	var total int64
	for _, art := range arts.GetArtifacts() {
		if art.GetName() != "benchmarks.txt" {
			continue
		}
		total += art.GetSizeBytes()
		if len(reports) >= 64 || total > 16*maxBenchmarkBytes {
			return nil, fmt.Errorf("benchmark run exceeds 64 reports or 16 MiB")
		}
		job, ok := jobs[art.GetJobId()]
		if !ok || job == "" {
			return nil, fmt.Errorf("benchmark artifact is not from a successful shell job")
		}
		if _, exists := reports[job]; exists {
			return nil, fmt.Errorf("duplicate benchmark job identity %q", job)
		}
		body, err := downloadBenchmark(ctx, client, art)
		if err != nil {
			return nil, err
		}
		report, err := parseBenchmarkReport(body)
		if err != nil {
			return nil, fmt.Errorf("job %s: %w", job, err)
		}
		reports[job] = report
	}
	return reports, nil
}

func downloadBenchmark(ctx context.Context, client civ1.CIServiceClient, art *civ1.ArtifactSummary) (string, error) {
	if art.GetSizeBytes() <= 0 || art.GetSizeBytes() > maxBenchmarkBytes {
		return "", fmt.Errorf("benchmark artifact size must be between 1 byte and 1 MiB")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := client.DownloadArtifact(ctx, &civ1.DownloadArtifactRequest{ArtifactId: art.GetId()})
	if err != nil {
		return "", fmt.Errorf("download benchmark: %w", err)
	}
	return receiveBenchmark(stream.Recv, art)
}

func receiveBenchmark(recv func() (*civ1.DownloadArtifactResponse, error), art *civ1.ArtifactSummary) (string, error) {
	var body strings.Builder
	first := true
	for {
		chunk, err := recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("read benchmark: %w", err)
		}
		if first && (chunk.GetName() != art.GetName() || chunk.GetSizeBytes() != art.GetSizeBytes()) {
			return "", fmt.Errorf("benchmark artifact metadata changed during download")
		}
		first = false
		if int64(body.Len()+len(chunk.GetData())) > art.GetSizeBytes() {
			return "", fmt.Errorf("benchmark artifact exceeds declared size")
		}
		body.Write(chunk.GetData())
	}
	if int64(body.Len()) != art.GetSizeBytes() {
		return "", fmt.Errorf("incomplete benchmark artifact")
	}
	return body.String(), nil
}
