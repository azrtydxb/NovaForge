package edge

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"path"
	"strconv"

	"github.com/go-chi/chi/v5"

	civ1 "github.com/novaforge/novaforge/gen/novaforge/ci/v1"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
)

// addCIHandlers mounts the CI read surface. Everything here is a question a
// person asks after a push: did it build, what did it print, what came out.
func addCIHandlers(h map[string]http.HandlerFunc, g gitv1.GitServiceClient, c civ1.CIServiceClient) {
	if c == nil || g == nil {
		return
	}
	repoID := func(r *http.Request) (string, error) {
		resp, err := g.GetRepo(r.Context(), &gitv1.GetRepoRequest{Name: chi.URLParam(r, "repo")})
		if err != nil {
			return "", err
		}
		return resp.GetRepo().GetId(), nil
	}
	// latestRun is what "the newest run" means for a repository: runs come back
	// newest first, so the first one is it.
	latestRun := func(r *http.Request) (string, error) {
		rid, err := repoID(r)
		if err != nil {
			return "", err
		}
		resp, err := c.ListRuns(r.Context(), &civ1.ListRunsRequest{RepoId: rid})
		if err != nil {
			return "", err
		}
		if len(resp.GetRuns()) == 0 {
			return "", nil
		}
		return resp.GetRuns()[0].GetId(), nil
	}

	h["listCIRuns"] = func(w http.ResponseWriter, r *http.Request) {
		rid, err := repoID(r)
		if err != nil {
			WriteError(w, StatusFromGRPC(err), err)
			return
		}
		resp, err := c.ListRuns(r.Context(), &civ1.ListRunsRequest{RepoId: rid})
		if err != nil {
			WriteError(w, StatusFromGRPC(err), err)
			return
		}
		out := make([]map[string]any, 0, len(resp.GetRuns()))
		for _, run := range resp.GetRuns() {
			out = append(out, ciRunJSON(run))
		}
		WriteJSON(w, http.StatusOK, map[string]any{"runs": out})
	}

	h["getCIRun"] = func(w http.ResponseWriter, r *http.Request) {
		resp, err := c.GetRun(r.Context(), &civ1.GetRunRequest{Id: chi.URLParam(r, "id")})
		if err != nil {
			WriteError(w, StatusFromGRPC(err), err)
			return
		}
		jobs := make([]map[string]any, 0, len(resp.GetJobs()))
		for _, j := range resp.GetJobs() {
			jobs = append(jobs, map[string]any{
				"id": j.GetId(), "name": j.GetName(),
				"status": j.GetStatus(), "detail": j.GetDetail(),
				"agent_role": j.GetAgentRole(), "agent_run_id": j.GetAgentRunId(),
				"work_item_key": j.GetWorkItemKey(),
			})
		}
		// The run is nested under "run", which is what the CI screen reads; its
		// fields stay at the top level as well for any client written against
		// the earlier flat shape.
		body := ciRunJSON(resp.GetRun())
		body["run"] = ciRunJSON(resp.GetRun())
		body["jobs"] = jobs
		WriteJSON(w, http.StatusOK, body)
	}

	h["getJobLogs"] = func(w http.ResponseWriter, r *http.Request) {
		resp, err := c.GetJobLogs(r.Context(), &civ1.GetJobLogsRequest{JobId: chi.URLParam(r, "id")})
		if err != nil {
			WriteError(w, StatusFromGRPC(err), err)
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{"lines": resp.GetLines()})
	}

	h["getLatestJobLogs"] = func(w http.ResponseWriter, r *http.Request) {
		runID, err := latestRun(r)
		if err != nil {
			WriteError(w, StatusFromGRPC(err), err)
			return
		}
		if runID == "" {
			WriteJSON(w, http.StatusOK, map[string]any{"lines": []string{}})
			return
		}
		resp, err := c.GetJobLogs(r.Context(), &civ1.GetJobLogsRequest{RunId: runID})
		if err != nil {
			WriteError(w, StatusFromGRPC(err), err)
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{"lines": resp.GetLines()})
	}

	h["listJobArtifacts"] = func(w http.ResponseWriter, r *http.Request) {
		resp, err := c.ListArtifacts(r.Context(), &civ1.ListArtifactsRequest{JobId: chi.URLParam(r, "id")})
		if err != nil {
			WriteError(w, StatusFromGRPC(err), err)
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{"artifacts": artifactsJSON(resp.GetArtifacts())})
	}

	h["downloadArtifact"] = func(w http.ResponseWriter, r *http.Request) {
		stream, err := c.DownloadArtifact(r.Context(), &civ1.DownloadArtifactRequest{ArtifactId: chi.URLParam(r, "id")})
		if err != nil {
			WriteError(w, StatusFromGRPC(err), err)
			return
		}
		// Nothing is written until the first message arrives, so an artifact
		// that does not exist — or belongs to another organization — is still
		// a 404 rather than a 200 carrying an empty file.
		first, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			WriteError(w, http.StatusNotFound, errors.New("no such artifact"))
			return
		}
		if err != nil {
			WriteError(w, StatusFromGRPC(err), err)
			return
		}
		w.Header().Set("Content-Type", artifactContentType(first.GetName()))
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": first.GetName()}))
		// An artifact is whatever a repository's job wrote. The browser must
		// neither sniff it into something renderable nor run it on the
		// platform's origin if it is opened anyway.
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "sandbox")
		w.Header().Set("Content-Length", strconv.FormatInt(first.GetSizeBytes(), 10))
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write(first.GetData()); err != nil {
			return
		}
		for {
			msg, err := stream.Recv()
			if err != nil {
				// Headers are gone; a failure part-way can only cut the body
				// short, which the declared Content-Length makes detectable.
				return
			}
			if _, err := w.Write(msg.GetData()); err != nil {
				return
			}
		}
	}

	h["listLatestArtifacts"] = func(w http.ResponseWriter, r *http.Request) {
		runID, err := latestRun(r)
		if err != nil {
			WriteError(w, StatusFromGRPC(err), err)
			return
		}
		if runID == "" {
			WriteJSON(w, http.StatusOK, map[string]any{"artifacts": []map[string]any{}})
			return
		}
		resp, err := c.ListArtifacts(r.Context(), &civ1.ListArtifactsRequest{RunId: runID})
		if err != nil {
			WriteError(w, StatusFromGRPC(err), err)
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{"artifacts": artifactsJSON(resp.GetArtifacts())})
	}
}

func ciRunJSON(r *civ1.WorkflowRunSummary) map[string]any {
	return map[string]any{
		"id": r.GetId(), "status": r.GetStatus(),
		"commit_sha": r.GetCommitSha(), "ref": r.GetRef(),
		"created_at": r.GetCreatedAt(),
	}
}

// inertTypes are served as application/octet-stream whatever their extension:
// a type a browser renders as a document, or executes, must not be served
// from the platform's origin with its real type.
var inertTypes = map[string]bool{
	"text/html": true, "application/xhtml+xml": true, "image/svg+xml": true,
	"text/javascript": true, "application/javascript": true, "text/xml": true,
	"application/xml": true,
}

// artifactContentType names an artifact's type from its extension, falling
// back to application/octet-stream for anything unknown or renderable.
func artifactContentType(name string) string {
	t := mime.TypeByExtension(path.Ext(name))
	if t == "" {
		return "application/octet-stream"
	}
	base, _, err := mime.ParseMediaType(t)
	if err != nil || inertTypes[base] {
		return "application/octet-stream"
	}
	return t
}

func artifactsJSON(as []*civ1.ArtifactSummary) []map[string]any {
	out := make([]map[string]any, 0, len(as))
	for _, a := range as {
		out = append(out, map[string]any{
			"id": a.GetId(), "job_id": a.GetJobId(),
			"name": a.GetName(), "size_bytes": a.GetSizeBytes(),
		})
	}
	return out
}
