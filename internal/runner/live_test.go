package runner_test

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"testing"
	"time"

	"github.com/novaforge/novaforge/internal/runner"
)

// TestJobOutputReachesTheLogWhileTheJobRuns pins the defect that made a pod
// job's log invisible until it ended: the pod executor collected every line of
// output, waited for the stream to close, and only then forwarded the lines —
// so a person watching a twenty-minute build saw nothing for twenty minutes,
// then all of it at once. The capture of artifacts only needs the lines
// between its markers held back; every other line must go out as it arrives.
func TestJobOutputReachesTheLogWhileTheJobRuns(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	raw := make(chan string)
	logs := make(chan string, 16)
	type result struct {
		arts []runner.Artifact
		err  error
	}
	done := make(chan result, 1)
	go func() {
		arts, err := runner.FilterOutputForTest(ctx, raw, logs)
		done <- result{arts, err}
	}()

	raw <- "compiling"
	select {
	case got := <-logs:
		if got != "compiling" {
			t.Fatalf("first forwarded line = %q, want compiling", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a line the job printed was not forwarded while the job was still producing output")
	}

	// The artifact payload is held back and never reaches the log.
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	body := []byte("coverage 81%\n")
	if err := tw.WriteHeader(&tar.Header{Name: "out/cover.txt", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	tw.Write(body)
	tw.Close()
	raw <- "::novaforge-artifacts-begin::"
	raw <- base64.StdEncoding.EncodeToString(buf.Bytes())
	raw <- "::novaforge-artifacts-end::"
	raw <- "done"
	close(raw)

	res := <-done
	if res.err != nil {
		t.Fatalf("filter: %v", res.err)
	}
	close(logs)
	var rest []string
	for l := range logs {
		rest = append(rest, l)
	}
	if len(rest) != 1 || rest[0] != "done" {
		t.Fatalf("remaining log lines = %q, want only \"done\" (the payload must not leak)", rest)
	}
	if len(res.arts) != 1 || res.arts[0].Name != "cover.txt" || string(res.arts[0].Content) != string(body) {
		t.Fatalf("artifact not recovered: %+v", res.arts)
	}
}
