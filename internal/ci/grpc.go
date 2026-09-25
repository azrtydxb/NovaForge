package ci

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"log"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/google/uuid"

	civ1 "github.com/novaforge/novaforge/gen/novaforge/ci/v1"
)

// logAppender receives one live log line for a job. LogSink (logs.go)
// implements it; Connect works without one (log chunks are simply dropped)
// so the runner protocol does not depend on live logging being wired up.
type logAppender interface {
	Append(ctx context.Context, jobID uuid.UUID, line string) error
}

// logSealer moves a finished job's live log into object storage.
type logSealer interface {
	Seal(ctx context.Context, jobID uuid.UUID) (string, error)
}

// Server implements novaforge.ci.v1.RunnerService: runners register once,
// then hold a single persistent outbound Connect stream that jobs are
// pushed down, so a runner never needs inbound network reachability.
type Server struct {
	civ1.UnimplementedRunnerServiceServer

	store      *Store
	dispatcher *Dispatcher
	logs       logAppender
	sealer     logSealer
	artifacts  *ArtifactStore
	redactions *Redactions
}

// SetRedactions wires the registry of credential values to mask in job logs.
// It must be the registry the pump registers into.
func (s *Server) SetRedactions(r *Redactions) { s.redactions = r }

// SetArtifactStore wires artifact storage, which the runner uploads through.
func (s *Server) SetArtifactStore(a *ArtifactStore) { s.artifacts = a }

// NewServer builds a Server backed by store and dispatcher.
func NewServer(store *Store, dispatcher *Dispatcher) *Server {
	return &Server{store: store, dispatcher: dispatcher}
}

// SetLogSink wires a live-log destination for streamed log_chunk frames.
// When the sink can also seal (LogSink can), a job's log is sealed as soon as
// its runner reports it finished.
func (s *Server) SetLogSink(logs logAppender) {
	s.logs = logs
	if sink, ok := logs.(*LogSink); ok {
		sink.store = s.store
	}
	if sealer, ok := logs.(logSealer); ok {
		s.sealer = sealer
	}
}

// Register enrolls a new runner, returning the id and bearer token it must
// present (as runner_id on every ConnectRequest) on its Connect stream.
//
// A runner is enrolled into the organization of whoever registers it: an
// owner or admin of that organization, or a platform credential minted for it
// (the runner deployment holds the platform secret and mints one naming its
// configured organization). This RPC used to take the organization from the
// request and check nobody, so anyone who could reach the port could enrol a
// runner into any organization, be dispatched that organization's jobs, and
// receive the 30-minute clone credential sent with each one.
func (s *Server) Register(ctx context.Context, req *civ1.RegisterRequest) (*civ1.RegisterResponse, error) {
	scope, err := registrationScope(ctx)
	if err != nil {
		return nil, err
	}
	orgID := scope.OrgID
	if raw := req.GetOrgId(); raw != "" && raw != orgID.String() {
		return nil, status.Error(codes.PermissionDenied, "a runner is registered into the caller's organization, not one named in the request")
	}
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
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
	ctx, cancelStream := context.WithCancel(stream.Context())
	defer cancelStream()

	send := make(chan *civ1.ConnectResponse, 16)
	sendErr := make(chan error, 1)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case msg := <-send:
				if err := stream.Send(msg); err != nil {
					sendErr <- err
					return
				}
			}
		}
	}()
	// The dispatcher may hold an in-flight channel reference after unregister.
	// Cancellation stops the sender; closing send would panic that producer.

	var runnerID uuid.UUID
	var runnerToken string
	var connectionID uuid.UUID
	defer func() {
		if connectionID != uuid.Nil {
			s.dispatcher.UnregisterConnection(ctx, runnerID, connectionID)
		}
	}()
	for {
		req, err := stream.Recv()
		if err != nil {
			if runnerID != uuid.Nil {
				s.dispatcher.UnregisterConnection(ctx, runnerID, connectionID)
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
			// The stream is the runner only once it proves it holds the token
			// Register returned; before this, a runner id — which is in job
			// records and logs — was all it took to be dispatched another
			// runner's jobs and to write into their logs.
			if err := s.authenticateRunner(ctx, id, req.GetToken()); err != nil {
				return err
			}
			if req.GetHeartbeat() == nil || req.GetConnectionId() != "" {
				return status.Error(codes.InvalidArgument, "initial heartbeat required")
			}
			runnerID = id
			runnerToken = req.GetToken()
			connectionID, err = s.dispatcher.activateConnection(ctx, id, send)
			if err != nil {
				return err
			}
			continue
		} else if id != runnerID || req.GetToken() != runnerToken || req.GetConnectionId() != connectionID.String() {
			s.dispatcher.UnregisterConnection(ctx, runnerID, connectionID)
			return status.Error(codes.PermissionDenied, "a Connect stream speaks for one runner")
		}

		if err := s.store.runnerConnection(ctx, runnerID, connectionID); err != nil {
			return status.Error(codes.PermissionDenied, "runner connection superseded")
		}
		switch req.GetPayload().(type) {
		case *civ1.ConnectRequest_Heartbeat:
			// The incarnation-qualified heartbeat above already refreshed liveness.
		case *civ1.ConnectRequest_LogChunk:
			chunk := req.GetLogChunk()
			jobID, jerr := uuid.Parse(chunk.GetJobId())
			if jerr != nil {
				continue
			}
			// Only the runner a job was dispatched to writes its log, and a
			// brokered value is masked before the line is stored, whatever the
			// runner did: the log is readable by every member of the
			// organization and the credential is not.
			if err := s.store.appendRunnerLog(ctx, runnerID, connectionID, jobID, chunk.GetSequence(), s.maskRunnerOutput(ctx, jobID, chunk.GetLine()), sha256.Sum256([]byte(chunk.GetLine()))); err != nil {
				return status.Error(codes.FailedPrecondition, "runner log admission failed")
			}
		}

		select {
		case err := <-sendErr:
			if runnerID != uuid.Nil {
				s.dispatcher.UnregisterConnection(ctx, runnerID, connectionID)
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
		return nil, status.Errorf(codes.InvalidArgument, "invalid job_id: %v", err)
	}
	runnerID, err := uuid.Parse(req.GetRunnerId())
	if err != nil {
		return nil, status.Error(codes.PermissionDenied, "a status report must name its runner")
	}
	// Anyone could set any job's status, and so any run's outcome, before this.
	if err := s.authenticateRunner(ctx, runnerID, req.GetToken()); err != nil {
		return nil, err
	}
	connectionID, err := uuid.Parse(req.GetConnectionId())
	if err != nil || s.store.jobConnection(ctx, jobID, runnerID, connectionID) != nil {
		return nil, status.Error(codes.PermissionDenied, "job connection superseded")
	}
	detail := s.maskRunnerOutput(ctx, jobID, req.GetDetail())
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		err = s.store.recordRunnerReceipt(ctx, runnerID, connectionID, jobID, req, detail)
		if !errors.Is(err, errLogBarrier) {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return nil, status.Error(codes.FailedPrecondition, "log sequence barrier incomplete")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, "runner receipt rejected")
	}
	// Sealing projects a committed terminal receipt, never a speculative
	// runner request. Failure retains the durable obligation for reconciliation.
	if terminalJobStatus(req.GetStatus()) && s.sealer != nil {
		if _, err := s.sealer.Seal(ctx, jobID); err != nil {
			log.Printf("ci: seal pending for %s: %v", jobID, err)
		}
	}
	switch req.GetStatus() {
	case "success", "failure", "cancelled":
		s.redactions.Forget(jobID)
	}
	return &civ1.ReportStatusResponse{Ok: true}, nil
}

func terminalJobStatus(s string) bool {
	return s == "success" || s == "failure" || s == "cancelled"
}

// maxArtifactBytes bounds one uploaded artifact. CI artifacts are reports and
// binaries, not disk images; a cap keeps one job from filling the object store
// and is far easier to reason about than a streaming quota.
const maxArtifactBytes = 64 << 20

// UploadArtifact stores one artifact a runner collected from a finished job.
//
// The runner uploads through this service rather than straight to object
// storage: only the platform knows which job an artifact belongs to, and
// handing every runner object-store credentials would make a runner a far more
// valuable thing to compromise.
func (s *Server) UploadArtifact(ctx context.Context, req *civ1.UploadArtifactRequest) (*civ1.UploadArtifactResponse, error) {
	if s.artifacts == nil {
		return nil, status.Error(codes.FailedPrecondition, "artifact storage is not configured")
	}
	jobID, err := uuid.Parse(req.GetJobId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid job id")
	}
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "artifact name is required")
	}
	if len(req.GetContent()) > maxArtifactBytes {
		return nil, status.Errorf(codes.InvalidArgument,
			"artifact %q is %d bytes, over the %d byte limit", req.GetName(), len(req.GetContent()), maxArtifactBytes)
	}
	// The job must exist and must belong to the runner presenting it, so a
	// runner cannot attach an artifact to somebody else's job.
	job, err := s.store.GetJob(ctx, jobID)
	if err != nil {
		return nil, status.Error(codes.NotFound, "no such job")
	}
	runnerID, err := uuid.Parse(req.GetRunnerId())
	if err != nil || job.RunnerID == nil || *job.RunnerID != runnerID {
		return nil, status.Error(codes.PermissionDenied, "this job is not assigned to that runner")
	}
	if err := s.authenticateRunner(ctx, runnerID, req.GetToken()); err != nil {
		return nil, err
	}

	connectionID, err := uuid.Parse(req.GetConnectionId())
	if err != nil || s.store.jobConnection(ctx, jobID, runnerID, connectionID) != nil {
		return nil, status.Error(codes.PermissionDenied, "job connection superseded")
	}
	art, err := s.artifacts.Upload(ctx, jobID, req.GetName(),
		bytes.NewReader(req.GetContent()), int64(len(req.GetContent())), connectionID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "store artifact: %v", err)
	}
	return &civ1.UploadArtifactResponse{ArtifactId: art.ID.String()}, nil
}

// Missing jobs/classification never imply credential-free output. A status RPC
// may reach a replica other than the one holding the runner's Connect stream.
func (s *Server) maskRunnerOutput(ctx context.Context, jobID uuid.UUID, line string) string {
	job, err := s.store.GetJob(ctx, jobID)
	if err != nil {
		return SuppressedCredentialOutput
	}
	return s.redactions.mask(jobID, line, len(job.Secrets) > 0)
}
