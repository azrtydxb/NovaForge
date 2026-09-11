package ci

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"fmt"

	"github.com/google/uuid"

	civ1 "github.com/novaforge/novaforge/gen/novaforge/ci/v1"
)

// logAppender receives one live log line for a job. LogSink (logs.go)
// implements it; Connect works without one (log chunks are simply dropped)
// so the runner protocol does not depend on live logging being wired up.
type logAppender interface {
	Append(ctx context.Context, jobID uuid.UUID, line string) error
}

// Server implements novaforge.ci.v1.RunnerService: runners register once,
// then hold a single persistent outbound Connect stream that jobs are
// pushed down, so a runner never needs inbound network reachability.
type Server struct {
	civ1.UnimplementedRunnerServiceServer

	store      *Store
	dispatcher *Dispatcher
	logs       logAppender
}

// NewServer builds a Server backed by store and dispatcher.
func NewServer(store *Store, dispatcher *Dispatcher) *Server {
	return &Server{store: store, dispatcher: dispatcher}
}

// SetLogSink wires a live-log destination for streamed log_chunk frames.
func (s *Server) SetLogSink(logs logAppender) {
	s.logs = logs
}

// Register enrolls a new runner, returning the id and bearer token it must
// present (as runner_id on every ConnectRequest) on its Connect stream.
func (s *Server) Register(ctx context.Context, req *civ1.RegisterRequest) (*civ1.RegisterResponse, error) {
	orgID, err := uuid.Parse(req.GetOrgId())
	if err != nil {
		return nil, fmt.Errorf("invalid org_id: %w", err)
	}
	if req.GetName() == "" {
		return nil, fmt.Errorf("name is required")
	}

	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, fmt.Errorf("generate runner token: %w", err)
	}
	token := fmt.Sprintf("%x", tokenBytes)
	hash := sha256.Sum256(tokenBytes)

	runnerID, err := s.store.RegisterRunner(ctx, orgID, req.GetName(), req.GetLabels(), hash[:])
	if err != nil {
		return nil, err
	}
	return &civ1.RegisterResponse{RunnerId: runnerID.String(), Token: token}, nil
}

// Connect is the persistent, runner-initiated stream the platform pushes
// jobs down. The runner identifies itself on every frame via runner_id; the
// first frame received registers it with the Dispatcher using the labels it
// announced at Register time, and heartbeats keep its last-seen time fresh.
// When the stream ends for any reason, the runner is unregistered and any
// job still running against it is reaped.
func (s *Server) Connect(stream civ1.RunnerService_ConnectServer) error {
	ctx := stream.Context()

	send := make(chan *civ1.ConnectResponse, 16)
	sendErr := make(chan error, 1)
	go func() {
		for msg := range send {
			if err := stream.Send(msg); err != nil {
				sendErr <- err
				return
			}
		}
		sendErr <- nil
	}()
	defer close(send)

	var runnerID uuid.UUID
	for {
		req, err := stream.Recv()
		if err != nil {
			if runnerID != uuid.Nil {
				s.dispatcher.Unregister(context.Background(), runnerID)
			}
			if err.Error() == "EOF" {
				return nil
			}
			return err
		}

		id, perr := uuid.Parse(req.GetRunnerId())
		if perr != nil {
			continue
		}
		if runnerID == uuid.Nil {
			runnerID = id
			labels, lerr := s.store.RunnerLabels(ctx, runnerID)
			if lerr != nil {
				return lerr
			}
			s.dispatcher.Register(runnerID, labels, send)
		}

		switch req.GetPayload().(type) {
		case *civ1.ConnectRequest_Heartbeat:
			_ = s.store.TouchRunner(ctx, runnerID)
		case *civ1.ConnectRequest_LogChunk:
			chunk := req.GetLogChunk()
			jobID, jerr := uuid.Parse(chunk.GetJobId())
			if jerr != nil {
				continue
			}
			if s.logs != nil {
				_ = s.logs.Append(ctx, jobID, chunk.GetLine())
			}
		}

		select {
		case err := <-sendErr:
			if runnerID != uuid.Nil {
				s.dispatcher.Unregister(context.Background(), runnerID)
			}
			return err
		default:
		}
	}
}

// ReportStatus records a job's terminal (or intermediate) status, reported
// by the runner that executed it once it finishes — or transitions — a job.
func (s *Server) ReportStatus(ctx context.Context, req *civ1.ReportStatusRequest) (*civ1.ReportStatusResponse, error) {
	jobID, err := uuid.Parse(req.GetJobId())
	if err != nil {
		return nil, fmt.Errorf("invalid job_id: %w", err)
	}
	if err := s.store.SetJobStatus(ctx, jobID, req.GetStatus(), req.GetDetail()); err != nil {
		return nil, err
	}
	return &civ1.ReportStatusResponse{Ok: true}, nil
}
