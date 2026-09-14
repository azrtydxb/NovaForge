package runner

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	civ1 "github.com/novaforge/novaforge/gen/novaforge/ci/v1"
)

// Executor runs one job, streaming its output into logs. PodExecutor is the
// deployed implementation; LocalExecutor runs on the host when explicitly
// allowed.
type Executor interface {
	Run(ctx context.Context, job *civ1.ConnectResponse, logs chan<- string) (int, error)
}

// DefaultHeartbeatInterval is how often a connected runner says it is alive.
const DefaultHeartbeatInterval = 15 * time.Second

// Session is one runner's side of the RunnerService protocol: it holds a
// Connect stream open, executes every job pushed down it, forwards each job's
// output up the same stream as log chunks while the job runs, and reports the
// job's outcome when it ends.
//
// It lives here rather than in cmd/runner so the protocol a deployed runner
// speaks is the one a test drives over a real gRPC connection. While it sat in
// package main, nothing could exercise the stream end to end, and "logs are
// observable while a job runs" was asserted only against Redis with no job
// running at all.
type Session struct {
	Client   civ1.RunnerServiceClient
	RunnerID string
	Executor Executor
	// Heartbeat is the heartbeat interval; zero means DefaultHeartbeatInterval.
	Heartbeat time.Duration
}

// Run holds one Connect stream open until it errors or ctx is cancelled. It
// returns nil when ctx is cancelled, and waits for jobs already started to
// report before returning.
func (s *Session) Run(ctx context.Context) error {
	stream, err := s.Client.Connect(ctx)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}

	var sendMu sync.Mutex
	send := func(msg *civ1.ConnectRequest) error {
		sendMu.Lock()
		defer sendMu.Unlock()
		return stream.Send(msg)
	}

	if err := send(heartbeatMessage(s.RunnerID)); err != nil {
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

	interval := s.Heartbeat
	if interval <= 0 {
		interval = DefaultHeartbeatInterval
	}
	ticker := time.NewTicker(interval)
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
			if err := send(heartbeatMessage(s.RunnerID)); err != nil {
				return fmt.Errorf("send heartbeat: %w", err)
			}
		case job := <-recvCh:
			wg.Add(1)
			go func() {
				defer wg.Done()
				s.runJob(ctx, send, job)
			}()
		}
	}
}

// runJob executes one job, forwarding its output as log_chunk frames as it is
// produced and reporting its final status once it exits. The status is
// reported only after every chunk has been handed to the stream, so the
// platform never seals a log before the last lines the job printed.
func (s *Session) runJob(ctx context.Context, send func(*civ1.ConnectRequest) error, job *civ1.ConnectResponse) {
	log.Printf("runner: starting job %s", job.GetJobId())

	logs := make(chan string, 256)
	logsDone := make(chan struct{})
	go func() {
		defer close(logsDone)
		for line := range logs {
			if err := send(&civ1.ConnectRequest{
				RunnerId: s.RunnerID,
				Payload:  &civ1.ConnectRequest_LogChunk{LogChunk: &civ1.LogChunk{JobId: job.GetJobId(), Line: line}},
			}); err != nil {
				log.Printf("runner: send log chunk for job %s: %v", job.GetJobId(), err)
			}
		}
	}()

	exitCode, err := s.Executor.Run(ctx, job, logs)
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
	if _, err := s.Client.ReportStatus(reportCtx, &civ1.ReportStatusRequest{
		RunnerId: s.RunnerID,
		JobId:    job.GetJobId(),
		Status:   status,
		ExitCode: int32(exitCode),
		Detail:   detail,
	}); err != nil {
		log.Printf("runner: report status for job %s: %v", job.GetJobId(), err)
	}
	log.Printf("runner: finished job %s: %s", job.GetJobId(), status)
}

// UploadArtifactsThrough returns the OnArtifacts hook that sends a finished
// job's artifacts back through the platform.
//
// Artifacts go back through the platform, not straight to object storage:
// only the platform knows which job an artifact belongs to, and giving every
// runner store credentials would make a runner a far more valuable thing to
// compromise.
func UploadArtifactsThrough(client civ1.RunnerServiceClient, runnerID string) func(context.Context, string, []Artifact) error {
	return func(ctx context.Context, jobID string, arts []Artifact) error {
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

func heartbeatMessage(runnerID string) *civ1.ConnectRequest {
	return &civ1.ConnectRequest{
		RunnerId: runnerID,
		Payload:  &civ1.ConnectRequest_Heartbeat{Heartbeat: &civ1.Heartbeat{At: time.Now().UTC().Format(time.RFC3339)}},
	}
}
