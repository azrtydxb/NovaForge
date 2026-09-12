// Command runner is the NovaForge CI runner: it registers with the
// ci-runner service, then holds a single persistent outbound Connect
// stream that jobs are pushed down, so it never needs inbound network
// reachability. It executes each job it receives and streams live output
// back as log_chunk frames.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	civ1 "github.com/novaforge/novaforge/gen/novaforge/ci/v1"
	"github.com/novaforge/novaforge/internal/runner"
)

// podExec is the isolated execution path. It is nil only outside a cluster,
// where the host fallback applies and must be explicitly enabled.
var podExec *runner.PodExecutor

const (
	heartbeatInterval = 15 * time.Second
	initialBackoff    = 1 * time.Second
	maxBackoff        = 60 * time.Second
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	addr := env("CI_ADDR", "localhost:9095")
	orgID := env("RUNNER_ORG_ID", "")
	name := env("RUNNER_NAME", hostname())
	labels := splitLabels(env("RUNNER_LABELS", "linux"))
	workdir := env("RUNNER_WORKDIR", filepath.Join(os.TempDir(), "novaforge-runner"))

	var err2 error
	podExec, err2 = runner.NewPodExecutorFromCluster(env("RUNNER_JOB_NAMESPACE", "novaforge"))
	if err2 != nil {
		log.Fatalf("runner: kubernetes client: %v", err2)
	}
	switch {
	case podExec != nil:
		log.Printf("runner: jobs execute in pods in namespace %s", env("RUNNER_JOB_NAMESPACE", "novaforge"))
	case runner.LocalExecutionAllowed():
		log.Println("runner: WARNING jobs execute on this host (NOVAFORGE_ALLOW_LOCAL_EXEC=1)")
	default:
		log.Fatalln("runner: not running in a cluster and NOVAFORGE_ALLOW_LOCAL_EXEC is not set; " +
			"refusing to start rather than run repository-supplied commands on this host")
	}

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("runner: dial %s: %v", addr, err)
	}
	defer conn.Close()
	client := civ1.NewRunnerServiceClient(conn)

	regResp, err := client.Register(ctx, &civ1.RegisterRequest{OrgId: orgID, Name: name, Labels: labels})
	if err != nil {
		log.Fatalf("runner: register: %v", err)
	}
	runnerID := regResp.GetRunnerId()

	// Artifacts go back through the platform, not straight to object storage:
	// only the platform knows which job an artifact belongs to, and giving
	// every runner store credentials would make a runner a far more valuable
	// thing to compromise.
	if podExec != nil {
		podExec.OnArtifacts = func(ctx context.Context, jobID string, arts []runner.Artifact) error {
			for _, a := range arts {
				if _, err := client.UploadArtifact(ctx, &civ1.UploadArtifactRequest{
					RunnerId: runnerID, JobId: jobID,
					Name: a.Name, Content: a.Content,
				}); err != nil {
					return fmt.Errorf("upload %s: %w", a.Name, err)
				}
			}
			return nil
		}
	}
	log.Printf("runner: registered as %s (%s), labels %v", name, runnerID, labels)

	backoff := initialBackoff
	for ctx.Err() == nil {
		if err := runConnection(ctx, client, runnerID, workdir); err != nil && ctx.Err() == nil {
			log.Printf("runner: connection error: %v (retrying in %s)", err, backoff)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return
			}
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}
		backoff = initialBackoff
	}
}

// runConnection holds one Connect stream open until it errors or ctx is
// cancelled: it sends periodic heartbeats, executes every job it receives,
// and reports each job's outcome.
func runConnection(ctx context.Context, client civ1.RunnerServiceClient, runnerID, workdir string) error {
	stream, err := client.Connect(ctx)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}

	var sendMu sync.Mutex
	send := func(msg *civ1.ConnectRequest) error {
		sendMu.Lock()
		defer sendMu.Unlock()
		return stream.Send(msg)
	}

	if err := send(heartbeatMessage(runnerID)); err != nil {
		return fmt.Errorf("send initial heartbeat: %w", err)
	}

	recvCh := make(chan *civ1.ConnectResponse)
	recvErrCh := make(chan error, 1)
	go func() {
		for {
			job, err := stream.Recv()
			if err != nil {
				recvErrCh <- err
				return
			}
			select {
			case recvCh <- job:
			case <-ctx.Done():
				return
			}
		}
	}()

	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	var wg sync.WaitGroup
	defer wg.Wait()

	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-recvErrCh:
			return fmt.Errorf("recv: %w", err)
		case <-ticker.C:
			if err := send(heartbeatMessage(runnerID)); err != nil {
				return fmt.Errorf("send heartbeat: %w", err)
			}
		case job := <-recvCh:
			wg.Add(1)
			go func() {
				defer wg.Done()
				runJob(ctx, client, send, runnerID, workdir, job)
			}()
		}
	}
}

// runJob executes one job, forwarding its output as log_chunk frames and
// reporting its final status once it exits.
func runJob(ctx context.Context, client civ1.RunnerServiceClient, send func(*civ1.ConnectRequest) error, runnerID, workdir string, job *civ1.ConnectResponse) {
	log.Printf("runner: starting job %s", job.GetJobId())

	logs := make(chan string, 256)
	logsDone := make(chan struct{})
	go func() {
		defer close(logsDone)
		for line := range logs {
			if err := send(&civ1.ConnectRequest{
				RunnerId: runnerID,
				Payload:  &civ1.ConnectRequest_LogChunk{LogChunk: &civ1.LogChunk{JobId: job.GetJobId(), Line: line}},
			}); err != nil {
				log.Printf("runner: send log chunk for job %s: %v", job.GetJobId(), err)
			}
		}
	}()

	// Jobs run in their own pod whenever the runner is in a cluster. Falling
	// back to the host is only possible with an explicit opt-in, because a job
	// command comes from the repository and is attacker-controlled.
	var exitCode int
	var err error
	if podExec != nil {
		exitCode, err = podExec.Run(ctx, job, logs)
	} else {
		jobDir := filepath.Join(workdir, job.GetJobId())
		exitCode, err = runner.Execute(ctx, job, jobDir, logs)
	}
	close(logs)
	<-logsDone

	status := "success"
	detail := ""
	switch {
	case err != nil:
		status = "failure"
		detail = err.Error()
	case exitCode != 0:
		status = "failure"
		detail = fmt.Sprintf("exit code %d", exitCode)
	}

	reportCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := client.ReportStatus(reportCtx, &civ1.ReportStatusRequest{
		RunnerId: runnerID,
		JobId:    job.GetJobId(),
		Status:   status,
		ExitCode: int32(exitCode),
		Detail:   detail,
	}); err != nil {
		log.Printf("runner: report status for job %s: %v", job.GetJobId(), err)
	}
	log.Printf("runner: finished job %s: %s", job.GetJobId(), status)
}

func heartbeatMessage(runnerID string) *civ1.ConnectRequest {
	return &civ1.ConnectRequest{
		RunnerId: runnerID,
		Payload:  &civ1.ConnectRequest_Heartbeat{Heartbeat: &civ1.Heartbeat{At: time.Now().UTC().Format(time.RFC3339)}},
	}
}

func splitLabels(s string) []string {
	var out []string
	for _, l := range strings.Split(s, ",") {
		l = strings.TrimSpace(l)
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "runner"
	}
	return h
}
