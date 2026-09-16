package maintenance

import (
	"context"
	"io"
	"testing"

	civ1 "github.com/novaforge/novaforge/gen/novaforge/ci/v1"
)

// Exercise wire framing without mocking any persistence. The stack regression
// separately exercises these frames over authenticated gRPC and real MinIO.
func TestBenchmarkDownloadRequiresCompleteEvidence(t *testing.T) {
	for _, size := range []int64{-1, 0, maxBenchmarkBytes + 1} {
		if _, err := downloadBenchmark(context.Background(), nil, &civ1.ArtifactSummary{SizeBytes: size}); err == nil {
			t.Fatalf("invalid advertised size %d accepted", size)
		}
	}
	for _, tc := range []struct {
		name     string
		chunks   []string
		declared int64
		end      error
		ok       bool
	}{
		{"complete", []string{"ab", "c"}, 3, io.EOF, true},
		{"truncated", []string{"ab"}, 3, io.EOF, false},
		{"excess", []string{"ab", "cd"}, 3, io.EOF, false},
		{"changed metadata", []string{"abc"}, 4, io.EOF, false},
		{"failed terminal read", []string{"abc"}, 3, io.ErrUnexpectedEOF, false},
		{"no frames", nil, 3, io.EOF, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := 0
			recv := func() (*civ1.DownloadArtifactResponse, error) {
				if i == len(tc.chunks) {
					return nil, tc.end
				}
				frame := &civ1.DownloadArtifactResponse{Data: []byte(tc.chunks[i])}
				if i == 0 {
					frame.Name, frame.SizeBytes = "benchmarks.txt", tc.declared
				}
				i++
				return frame, nil
			}
			body, err := receiveBenchmark(recv, &civ1.ArtifactSummary{Name: "benchmarks.txt", SizeBytes: 3})
			if (err == nil) != tc.ok || (tc.ok && body != "abc") {
				t.Fatalf("body=%q err=%v", body, err)
			}
		})
	}
}
