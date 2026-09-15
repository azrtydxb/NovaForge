// Package runner executes one CI job on a runner: it clones the repository
// under test at the pushed commit and runs the job's command, streaming
// every output line back so live logs can flow through Redis.
package runner

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	civ1 "github.com/novaforge/novaforge/gen/novaforge/ci/v1"
)

// logScannerMaxLine bounds a single scanned log line at 1 MiB so a long
// line (a large diff, a base64 blob) is never silently truncated.
const logScannerMaxLine = 1 << 20

// cancelWaitDelay bounds how long Execute waits for the command's I/O
// pipes to close after it has been killed, so a cancelled job's stdout
// pipe being held open cannot leak the call forever.
const cancelWaitDelay = 5 * time.Second

// LocalExecutor runs a job's command on the runner host, in a directory per
// job under Workdir. It exists for development and for tests that drive the
// real runner protocol without a cluster; a deployed runner uses PodExecutor.
//
// It captures declared artifacts exactly the way a pod does — the job emits
// them on its own output and the same filter lifts them out — so the path a
// test exercises is the path a pod job takes, not a parallel one.
type LocalExecutor struct {
	Workdir string
	// OnArtifacts receives whatever the job declared, once, after it succeeds.
	OnArtifacts func(ctx context.Context, jobID string, artifacts []Artifact) error
}

// Run executes job in Workdir/<job id>.
func (e *LocalExecutor) Run(ctx context.Context, job *civ1.ConnectResponse, logs chan<- string) (int, error) {
	dir := e.Workdir
	if job.GetJobId() != "" {
		dir = filepath.Join(e.Workdir, filepath.Base(job.GetJobId()))
	}
	return execute(ctx, job, dir, logs, e.OnArtifacts)
}

// Execute clones job's repository at job.CommitSha into workdir (skipped
// when job carries no RepoCloneUrl, which test jobs with a bare command
// don't), then runs job.RunCmd there, streaming every output line — stdout
// and stderr interleaved, in the order the process produced them — into
// logs. A failing command is a result, not a transport error: Execute
// returns the command's real exit code with a nil error. Cancelling ctx
// kills the running command via CommandContext's Cancel hook rather than
// leaking it, waiting at most WaitDelay for its output pipes to close.
func Execute(ctx context.Context, job *civ1.ConnectResponse, workdir string, logs chan<- string) (int, error) {
	return execute(ctx, job, workdir, logs, nil)
}

func execute(ctx context.Context, job *civ1.ConnectResponse, workdir string, logs chan<- string,
	onArtifacts func(context.Context, string, []Artifact) error) (int, error) {
	// A CI job runs whatever the repository's workflow file says, so the command
	// is attacker-controlled by construction. The defence is isolation, not
	// input validation — see PodExecutor. Running it on the runner host is a
	// development convenience and has to be opted into deliberately.
	if !LocalExecutionAllowed() {
		return 0, ErrLocalExecutionNotPermitted
	}
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		return 0, fmt.Errorf("create workdir: %w", err)
	}

	if job.GetRepoCloneUrl() != "" {
		if err := cloneAt(ctx, job.GetRepoCloneUrl(), job.GetCommitSha(), workdir); err != nil {
			return 0, err
		}
	}

	// Reached only when an operator has explicitly set NOVAFORGE_ALLOW_LOCAL_EXEC=1
	// (checked above); the isolated path is PodExecutor.Run, which is the default
	// in a cluster and the only path a deployed runner can take.
	script := job.GetRunCmd()
	if onArtifacts != nil {
		script += artifactCaptureScript(job.GetArtifactPaths())
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", script) // nosemgrep: dangerous-exec-command
	cmd.Dir = workdir
	cmd.Env = append(os.Environ(), envSlice(job.GetEnv())...)
	cmd.Env = append(cmd.Env, envSlice(job.GetSecretEnv())...)
	cmd.Cancel = func() error {
		return cmd.Process.Kill()
	}
	cmd.WaitDelay = cancelWaitDelay

	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw

	raw := make(chan string, 256)
	type filtered struct {
		arts []Artifact
		err  error
	}
	filterDone := make(chan filtered, 1)
	go func() {
		arts, err := filterOutput(ctx, raw, logs)
		filterDone <- filtered{arts, err}
	}()

	scanDone := make(chan struct{})
	go func() {
		defer close(scanDone)
		defer close(raw)
		scanner := bufio.NewScanner(pr)
		scanner.Buffer(make([]byte, 64*1024), logScannerMaxLine)
		for scanner.Scan() {
			raw <- scanner.Text()
		}
		// Unblock the writer if scanning stopped early (an over-long line).
		_, _ = io.Copy(io.Discard, pr)
	}()

	if err := cmd.Start(); err != nil {
		pw.Close()
		<-scanDone
		<-filterDone
		return 0, fmt.Errorf("start command: %w", err)
	}

	waitErr := cmd.Wait()
	pw.Close()
	<-scanDone
	out := <-filterDone

	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			return exitErr.ExitCode(), nil
		}
		if ctx.Err() != nil {
			return -1, ctx.Err()
		}
		return 0, fmt.Errorf("run command: %w", waitErr)
	}

	// As in a pod: artifacts are kept only from a job that succeeded.
	if onArtifacts != nil {
		switch {
		case out.err != nil:
			sendLine(ctx, logs, "novaforge: reading artifacts failed: "+out.err.Error())
		case len(out.arts) > 0:
			if uerr := onArtifacts(ctx, job.GetJobId(), out.arts); uerr != nil {
				sendLine(ctx, logs, "novaforge: uploading artifacts failed: "+uerr.Error())
			}
		}
	}
	return 0, nil
}

// sendLine reports something the runner itself has to say into the job's log,
// without blocking forever on a reader that has gone away.
func sendLine(ctx context.Context, logs chan<- string, line string) {
	select {
	case logs <- line:
	case <-ctx.Done():
	}
}

// cloneAt clones url into dir and checks it out at sha (when sha is
// non-empty), using the git binary directly rather than a library so no
// additional dependency is needed for a straightforward shallow-enough
// clone-and-checkout.
func cloneAt(ctx context.Context, url, sha, dir string) error {
	if err := runGit(ctx, "", "clone", url, dir); err != nil {
		return fmt.Errorf("clone %s: %w", url, err)
	}
	if sha != "" {
		if err := runGit(ctx, dir, "checkout", sha); err != nil {
			return fmt.Errorf("checkout %s: %w", sha, err)
		}
	}
	return nil
}

// runGit shells out to git with a static binary name, so the only variable
// part is the argument list.
func runGit(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %v: %w: %s", args, err, string(out))
	}
	return nil
}

func envSlice(m map[string]string) []string {
	s := make([]string, 0, len(m))
	for k, v := range m {
		s = append(s, k+"="+v)
	}
	return s
}
