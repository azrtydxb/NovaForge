package ci

import (
	"context"
	"errors"
	"io"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	civ1 "github.com/novaforge/novaforge/gen/novaforge/ci/v1"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/blobstore"
)

// QueryServer answers questions about runs, jobs, logs and artifacts for a
// person or a tool, as opposed to RunnerService, which serves runners.
type QueryServer struct {
	civ1.UnimplementedCIServiceServer

	store     *Store
	logs      *LogSink
	artifacts *ArtifactStore
	blobs     *blobstore.Client

	// scheduler and git back TriggerRun; both are nil until SetScheduler is
	// called, and TriggerRun says so rather than panicking.
	scheduler *Scheduler
	git       gitv1.GitServiceClient
}

// NewQueryServer wires the read side of CI.
func NewQueryServer(store *Store, logs *LogSink, artifacts *ArtifactStore, blobs *blobstore.Client) *QueryServer {
	return &QueryServer{store: store, logs: logs, artifacts: artifacts, blobs: blobs}
}

func (q *QueryServer) scope(ctx context.Context) (authz.Scope, error) {
	s, err := authz.FromContext(ctx)
	if err != nil || s.OrgID == uuid.Nil {
		return authz.Scope{}, status.Error(codes.Unauthenticated, "authentication required")
	}
	return s, nil
}

// ListRuns returns the workflow runs of one repository, within the caller's
// organization.
func (q *QueryServer) ListRuns(ctx context.Context, req *civ1.ListRunsRequest) (*civ1.ListRunsResponse, error) {
	sc, err := q.scope(ctx)
	if err != nil {
		return nil, err
	}
	repoID, err := uuid.Parse(req.GetRepoId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid repo id")
	}
	runs, err := q.store.ListRuns(ctx, sc.OrgID, repoID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list runs: %v", err)
	}
	out := make([]*civ1.WorkflowRunSummary, 0, len(runs))
	for _, r := range runs {
		out = append(out, runSummary(r))
	}
	return &civ1.ListRunsResponse{Runs: out}, nil
}

// GetRun returns one run and its jobs.
func (q *QueryServer) GetRun(ctx context.Context, req *civ1.GetRunRequest) (*civ1.GetRunResponse, error) {
	sc, err := q.scope(ctx)
	if err != nil {
		return nil, err
	}
	id, err := uuid.Parse(req.GetId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid run id")
	}
	run, err := q.store.GetRun(ctx, id)
	if err != nil {
		return nil, status.Error(codes.NotFound, "no such run")
	}
	// The org predicate is applied here rather than trusted from the request:
	// a run id from another organization must read as absent.
	if run.OrgID != sc.OrgID {
		return nil, status.Error(codes.NotFound, "no such run")
	}
	jobs, err := q.store.ListJobsForRun(ctx, id)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list jobs: %v", err)
	}
	outJobs := make([]*civ1.WorkflowJobSummary, 0, len(jobs))
	for _, j := range jobs {
		summary := &civ1.WorkflowJobSummary{
			Id: j.ID.String(), RunId: j.RunID.String(),
			Name: j.Name, Status: j.Status, Detail: j.Detail,
			AgentRole: j.AgentRole, WorkItemKey: j.WorkItemKey,
		}
		if j.AgentRunID != nil {
			summary.AgentRunId = j.AgentRunID.String()
		}
		outJobs = append(outJobs, summary)
	}
	return &civ1.GetRunResponse{Run: runSummary(run), Jobs: outJobs}, nil
}

// GetJobLogs returns a finished job's sealed log, or the live tail if it has
// not been sealed yet. With no job id, the first job of the given run is used,
// which is what "did my push build" usually means.
func (q *QueryServer) GetJobLogs(ctx context.Context, req *civ1.GetJobLogsRequest) (*civ1.GetJobLogsResponse, error) {
	sc, err := q.scope(ctx)
	if err != nil {
		return nil, err
	}
	jobID, err := q.resolveJob(ctx, sc, req.GetJobId(), req.GetRunId())
	if err != nil {
		return nil, err
	}

	if lines, journal, err := q.store.journalSnapshot(ctx, jobID); err != nil {
		return nil, status.Errorf(codes.Internal, "read admitted log: %v", err)
	} else if journal {
		return &civ1.GetJobLogsResponse{Lines: lines}, nil
	}

	// A finished job's log lives in object storage; a running job's in Redis.
	// Both are read, sealed first: a runner's last chunks can land after the
	// status report that sealed the log, and those lines stay live until the
	// next seal. Reading only one side would drop them.
	var lines []string
	rc, err := q.blobs.Get(ctx, sealedObjectKey(jobID))
	switch {
	case err == nil:
		body, rerr := io.ReadAll(rc)
		rc.Close()
		if rerr != nil {
			return nil, status.Errorf(codes.Internal, "read sealed log: %v", rerr)
		}
		lines = splitLines(string(body))
	case !errors.Is(err, blobstore.ErrNotFound):
		return nil, status.Errorf(codes.Internal, "read sealed log: %v", err)
	}

	live, err := q.logs.Snapshot(ctx, jobID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "read live log: %v", err)
	}
	lines = append(lines, live...)
	if lines == nil {
		lines = []string{}
	}
	return &civ1.GetJobLogsResponse{Lines: lines}, nil
}

// ListArtifacts returns a run's artifacts, or one job's when job_id is set.
//
// job_id used to be ignored: the request was resolved as a run, an empty run
// id is invalid, and so every "this job's artifacts" read — the only one the
// interface makes — failed.
func (q *QueryServer) ListArtifacts(ctx context.Context, req *civ1.ListArtifactsRequest) (*civ1.ListArtifactsResponse, error) {
	sc, err := q.scope(ctx)
	if err != nil {
		return nil, err
	}
	var arts []Artifact
	if req.GetJobId() != "" {
		jobID, jerr := q.resolveJob(ctx, sc, req.GetJobId(), "")
		if jerr != nil {
			return nil, jerr
		}
		arts, err = q.artifacts.ListForJob(ctx, jobID)
	} else {
		runID, rerr := q.resolveRun(ctx, sc, req.GetRunId())
		if rerr != nil {
			return nil, rerr
		}
		arts, err = q.artifacts.List(ctx, runID)
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list artifacts: %v", err)
	}
	out := make([]*civ1.ArtifactSummary, 0, len(arts))
	for _, a := range arts {
		out = append(out, &civ1.ArtifactSummary{
			Id: a.ID.String(), JobId: a.JobID.String(),
			Name: a.Name, SizeBytes: a.SizeBytes,
		})
	}
	return &civ1.ListArtifactsResponse{Artifacts: out}, nil
}

// downloadChunk is the size of each message DownloadArtifact sends, well
// under gRPC's default 4 MiB message limit.
const downloadChunk = 256 << 10

// DownloadArtifact streams one artifact's content to a member of the
// organization that owns it. An artifact of another organization reads as
// absent, exactly like one that does not exist.
func (q *QueryServer) DownloadArtifact(req *civ1.DownloadArtifactRequest, stream civ1.CIService_DownloadArtifactServer) error {
	ctx := stream.Context()
	sc, err := q.scope(ctx)
	if err != nil {
		return err
	}
	id, err := uuid.Parse(req.GetArtifactId())
	if err != nil {
		return status.Error(codes.InvalidArgument, "invalid artifact id")
	}
	art, rc, err := q.artifacts.OpenInOrg(ctx, sc.OrgID, id)
	if err != nil {
		if errors.Is(err, ErrArtifactNotFound) {
			return status.Error(codes.NotFound, "no such artifact")
		}
		return status.Errorf(codes.Internal, "open artifact: %v", err)
	}
	defer rc.Close()

	buf := make([]byte, downloadChunk)
	first := true
	for {
		n, rerr := io.ReadFull(rc, buf)
		if n > 0 || first {
			msg := &civ1.DownloadArtifactResponse{Data: buf[:n]}
			if first {
				msg.Name, msg.SizeBytes = art.Name, art.SizeBytes
				first = false
			}
			if err := stream.Send(msg); err != nil {
				return err
			}
		}
		if errors.Is(rerr, io.EOF) || errors.Is(rerr, io.ErrUnexpectedEOF) {
			return nil
		}
		if rerr != nil {
			return status.Errorf(codes.Internal, "read artifact: %v", rerr)
		}
	}
}

func (q *QueryServer) resolveRun(ctx context.Context, sc authz.Scope, id string) (uuid.UUID, error) {
	runID, err := uuid.Parse(id)
	if err != nil {
		return uuid.Nil, status.Error(codes.InvalidArgument, "invalid run id")
	}
	run, err := q.store.GetRun(ctx, runID)
	if err != nil || run.OrgID != sc.OrgID {
		return uuid.Nil, status.Error(codes.NotFound, "no such run")
	}
	return runID, nil
}

func (q *QueryServer) resolveJob(ctx context.Context, sc authz.Scope, jobID, runID string) (uuid.UUID, error) {
	if jobID != "" {
		id, err := uuid.Parse(jobID)
		if err != nil {
			return uuid.Nil, status.Error(codes.InvalidArgument, "invalid job id")
		}
		job, err := q.store.GetJob(ctx, id)
		if err != nil {
			return uuid.Nil, status.Error(codes.NotFound, "no such job")
		}
		run, err := q.store.GetRun(ctx, job.RunID)
		if err != nil || run.OrgID != sc.OrgID {
			return uuid.Nil, status.Error(codes.NotFound, "no such job")
		}
		return id, nil
	}
	rid, err := q.resolveRun(ctx, sc, runID)
	if err != nil {
		return uuid.Nil, err
	}
	jobs, err := q.store.ListJobsForRun(ctx, rid)
	if err != nil || len(jobs) == 0 {
		return uuid.Nil, status.Error(codes.NotFound, "the run has no jobs")
	}
	return jobs[0].ID, nil
}

func runSummary(r Run) *civ1.WorkflowRunSummary {
	return &civ1.WorkflowRunSummary{
		Id: r.ID.String(), RepoId: r.RepoID.String(),
		CommitSha: r.CommitSHA, Ref: r.Ref, Status: r.Status,
		CreatedAt: r.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

func splitLines(s string) []string {
	out := []string{}
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}
