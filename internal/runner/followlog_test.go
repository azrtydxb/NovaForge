package runner_test

import (
	"context"
	"strings"
	"testing"

	"github.com/novaforge/novaforge/internal/runner"
)

// TestBrokenFollowIsCompletedFromTheFullLog replays what a node out of inotify
// instances did to a job's log on the cluster: the kubelet ended the follow
// after the job's first line and wrote its own error into the stream. The job's
// last line and its artifact block never arrived, and the error read as job
// output. The follow now holds its latest line back, and the job's complete
// log supplies everything after what was forwarded.
func TestBrokenFollowIsCompletedFromTheFullLog(t *testing.T) {
	followed := strings.NewReader("hello from novaforge ci\nfailed to create fsnotify watcher: too many open files\n")
	logs := make(chan string, 16)
	sent, err := runner.ForwardHoldingLastForTest(context.Background(), followed, logs)
	if err != nil {
		t.Fatal(err)
	}
	full := []string{"hello from novaforge ci", "build finished", "__NF_ARTIFACTS_BEGIN__", "payload", "__NF_ARTIFACTS_END__"}
	for _, l := range runner.RemainingLinesForTest(full, sent) {
		logs <- l
	}
	close(logs)
	var got []string
	for l := range logs {
		got = append(got, l)
	}
	if strings.Join(got, "|") != strings.Join(full, "|") {
		t.Fatalf("forwarded %q, want the job's complete log %q", got, full)
	}
	for _, l := range got {
		if strings.Contains(l, "fsnotify") {
			t.Fatalf("the kubelet's own error was forwarded as job output: %q", l)
		}
	}

	// A follow that ended normally forwards everything but its last line, which
	// the full log then supplies once — never twice.
	logs = make(chan string, 16)
	sent, _ = runner.ForwardHoldingLastForTest(context.Background(), strings.NewReader("a\nb\n"), logs)
	for _, l := range runner.RemainingLinesForTest([]string{"a", "b"}, sent) {
		logs <- l
	}
	close(logs)
	got = nil
	for l := range logs {
		got = append(got, l)
	}
	if strings.Join(got, "|") != "a|b" {
		t.Fatalf("a complete follow forwarded %q, want a|b exactly once", got)
	}
}
