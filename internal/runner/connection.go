package runner

import (
	"context"
	"fmt"
	"google.golang.org/protobuf/proto"
	"log"
	"sync"
	"time"

	civ1 "github.com/novaforge/novaforge/gen/novaforge/ci/v1"
	"github.com/novaforge/novaforge/internal/redact"
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
	// Token is the secret Register returned. Every call presents it: the runner
	// id alone is not a secret, and ci-runner refuses a runner without it.
	Token    string
	Executor Executor
	// Heartbeat is the heartbeat interval; zero means DefaultHeartbeatInterval.
	Heartbeat time.Duration
}

// Run holds one Connect stream open until it errors or ctx is cancelled. It
// returns nil when ctx is cancelled, and waits for jobs already started to
// report before returning.
func (s *Session) Run(ctx context.Context) error {
	ctx, cancelSession := context.WithCancel(ctx)
	defer cancelSession()
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

	if err := send(heartbeatMessage(s.RunnerID, s.Token)); err != nil {
		return fmt.Errorf("send initial heartbeat: %w", err)
	}

	hello, err := stream.Recv()
	if err != nil || hello.GetConnectionId() == "" || hello.GetJobId() != "" {
		return fmt.Errorf("runner connection handshake failed")
	}
	connectionID := hello.GetConnectionId()
	rawSend := send
	send = func(msg *civ1.ConnectRequest) error { msg.ConnectionId = connectionID; return rawSend(msg) }
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
	defer func() { cancelSession(); wg.Wait() }()

	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-recvErrCh:
			return fmt.Errorf("recv: %w", err)
		case <-ticker.C:
			if err := send(heartbeatMessage(s.RunnerID, s.Token)); err != nil {
				return fmt.Errorf("send heartbeat: %w", err)
			}
		case job := <-recvCh:
			if job.GetConnectionId() != connectionID || job.GetJobId() == "" {
				return fmt.Errorf("job has invalid connection identity")
			}
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

	// The job's brokered credentials are masked before a line leaves this
	// host. ci-runner masks them again before storing the line, so a runner
	// that does not is caught there — but the fewer places the value travels,
	// the fewer places it can leak from.
	mask := redact.New(redact.Values(job.GetSecretEnv()))

	logs := make(chan string, 256)
	logsDone := make(chan struct{})
	var lastLogSequence int64
	go func() {
		defer close(logsDone)
		for line := range logs {
			lastLogSequence++
			line = mask.Line(line)
			if err := send(&civ1.ConnectRequest{
				RunnerId: s.RunnerID,
				Token:    s.Token,
				Payload:  &civ1.ConnectRequest_LogChunk{LogChunk: &civ1.LogChunk{JobId: job.GetJobId(), Line: line, Sequence: lastLogSequence}},
			}); err != nil {
				log.Printf("runner: send log chunk for job %s: %v", job.GetJobId(), err)
			}
		}
	}()

	ctx = context.WithValue(ctx, connectionContextKey{}, job.GetConnectionId())
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
		RunnerId:        s.RunnerID,
		ConnectionId:    job.GetConnectionId(),
		Token:           s.Token,
		JobId:           job.GetJobId(),
		Status:          status,
		ExitCode:        int32(exitCode),
		LastLogSequence: proto.Int64(lastLogSequence),
		Detail:          mask.Line(detail),
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
func UploadArtifactsThrough(client civ1.RunnerServiceClient, runnerID, token string) func(context.Context, string, []Artifact) error {
	return func(ctx context.Context, jobID string, arts []Artifact) error {
		for _, a := range arts {
			if _, err := client.UploadArtifact(ctx, &civ1.UploadArtifactRequest{
				RunnerId: runnerID, Token: token, JobId: jobID, ConnectionId: connectionFromContext(ctx),
				Name: a.Name, Content: a.Content,
			}); err != nil {
				return fmt.Errorf("upload %s: %w", a.Name, err)
			}
		}
		return nil
	}
}

func heartbeatMessage(runnerID, token string) *civ1.ConnectRequest {
	return &civ1.ConnectRequest{
		RunnerId: runnerID,
		Token:    token,
		Payload:  &civ1.ConnectRequest_Heartbeat{Heartbeat: &civ1.Heartbeat{At: time.Now().UTC().Format(time.RFC3339)}},
	}
}

type connectionContextKey struct{}

func connectionFromContext(ctx context.Context) string {
	value, _ := ctx.Value(connectionContextKey{}).(string)
	return value
}
