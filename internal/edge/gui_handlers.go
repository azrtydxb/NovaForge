package edge

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	agentsv1 "github.com/novaforge/novaforge/gen/novaforge/agents/v1"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	reviewsv1 "github.com/novaforge/novaforge/gen/novaforge/reviews/v1"
	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

// AddGUIHandlers must be called after the existing handlers: the Work Item
// wrappers also enforce the URL repository on the older key-only operations.
func AddGUIHandlers(h map[string]http.HandlerFunc, g gitv1.GitServiceClient, w workv1.WorkServiceClient, rv reviewsv1.ReviewsServiceClient, a agentsv1.AgentServiceClient) {
	if g != nil && w != nil {
		for _, name := range []string{"getWorkItem", "listWorkComments", "addWorkComment", "assignWorkItem", "listSubtasks", "decomposeEpic", "listWorkItemAgentRuns"} {
			if old := h[name]; old != nil {
				h[name] = func(wr http.ResponseWriter, r *http.Request) {
					if _, err := guiWorkItem(r, g, w); err != nil {
						WriteError(wr, guiStatus(err), err)
						return
					}
					old(wr, r)
				}
			}
		}
		h["getWorkItem"] = func(wr http.ResponseWriter, r *http.Request) {
			item, err := guiWorkItem(r, g, w)
			if err != nil {
				WriteError(wr, guiStatus(err), err)
				return
			}
			WriteJSON(wr, http.StatusOK, guiItemJSON(item))
		}
		h["patchWorkItem"] = func(wr http.ResponseWriter, r *http.Request) {
			item, err := guiWorkItem(r, g, w)
			if err != nil {
				WriteError(wr, guiStatus(err), err)
				return
			}
			var body map[string]json.RawMessage
			if err := decodeGUI(wr, r, &body); err != nil {
				WriteError(wr, 400, err)
				return
			}
			req := &workv1.PatchItemRequest{Id: item.GetId(), RepoId: item.GetRepoId(), Values: &workv1.WorkItem{}, Expected: &workv1.WorkItem{}, UpdateMask: &fieldmaskpb.FieldMask{}}
			if len(body["expected"]) == 0 || string(body["expected"]) == "null" {
				WriteError(wr, 400, errors.New("expected intent is required"))
				return
			}
			if err := json.Unmarshal(body["expected"], req.Expected); err != nil {
				WriteError(wr, 400, err)
				return
			}
			for key, raw := range body {
				if key == "expected" {
					continue
				}
				var dest any
				switch key {
				case "type":
					dest = &req.Values.Type
				case "goal":
					dest = &req.Values.Goal
				case "acceptance":
					dest = &req.Values.Acceptance
				case "constraints":
					dest = &req.Values.Constraints
				case "required_gates":
					dest = &req.Values.RequiredGates
				default:
					WriteError(wr, 400, errors.New("unsupported editable field: "+key))
					return
				}
				if string(raw) == "null" {
					WriteError(wr, 400, errors.New("editable fields cannot be null"))
					return
				}
				if err := json.Unmarshal(raw, dest); err != nil {
					WriteError(wr, 400, err)
					return
				}
				req.UpdateMask.Paths = append(req.UpdateMask.Paths, key)
			}
			out, err := w.PatchItem(r.Context(), req)
			if err != nil {
				WriteError(wr, guiStatus(err), err)
				return
			}
			WriteJSON(wr, 200, guiItemJSON(out.GetItem()))
		}
		h["transitionWorkItem"] = func(wr http.ResponseWriter, r *http.Request) {
			item, err := guiWorkItem(r, g, w)
			if err != nil {
				WriteError(wr, guiStatus(err), err)
				return
			}
			var body struct {
				ToState       string `json:"to_state"`
				ExpectedState string `json:"expected_state"`
			}
			if err := decodeGUI(wr, r, &body); err != nil {
				WriteError(wr, 400, err)
				return
			}
			out, err := w.TransitionItem(r.Context(), &workv1.TransitionItemRequest{Id: item.GetId(), RepoId: item.GetRepoId(), ToState: body.ToState, ExpectedState: body.ExpectedState})
			if err != nil {
				WriteError(wr, guiStatus(err), err)
				return
			}
			WriteJSON(wr, 200, guiItemJSON(out.GetItem()))
		}
	}
	if rv != nil {
		// Canonical persisted identity: unlike a slug lookup, this history read
		// must not require Git merely to rediscover a run the caller already knows.
		h["listEngineeringRunReviews"] = func(wr http.ResponseWriter, r *http.Request) {
			id, err := uuid.Parse(chi.URLParam(r, "run_id"))
			if err != nil || id == uuid.Nil {
				WriteError(wr, 400, errors.New("valid run_id required"))
				return
			}
			writeGUIReviews(wr, r, rv, id.String())
		}
	}
	if g != nil && rv != nil {
		h["listRunReviews"] = func(wr http.ResponseWriter, r *http.Request) {
			run, err := guiRun(r, g, rv)
			if err != nil {
				WriteError(wr, guiStatus(err), err)
				return
			}
			writeGUIReviews(wr, r, rv, run.GetId())
		}
		// The old handler discarded summary. Keep revision and summary together in
		// the same durable write, and do not resolve a newer revision for the caller.
		h["submitReview"] = func(wr http.ResponseWriter, r *http.Request) {
			run, err := guiRun(r, g, rv)
			if err != nil {
				WriteError(wr, guiStatus(err), err)
				return
			}
			var body struct {
				Verdict           string `json:"verdict"`
				Summary           string `json:"summary"`
				ExpectedSourceSHA string `json:"expected_source_sha"`
			}
			if err := decodeGUI(wr, r, &body); err != nil {
				WriteError(wr, 400, err)
				return
			}
			_, err = rv.SubmitReview(r.Context(), &reviewsv1.SubmitReviewRequest{RunId: run.GetId(), Verdict: body.Verdict, Summary: body.Summary, ExpectedSourceSha: body.ExpectedSourceSHA})
			if err != nil {
				WriteError(wr, guiStatus(err), err)
				return
			}
			WriteJSON(wr, 201, map[string]string{"status": "recorded"})
		}
	}
	if a != nil {
		addGUIAgentHandlers(h, a)
	}
}
func guiWorkItem(r *http.Request, g gitv1.GitServiceClient, w workv1.WorkServiceClient) (*workv1.WorkItem, error) {
	repo, err := g.GetRepo(r.Context(), &gitv1.GetRepoRequest{Name: chi.URLParam(r, "repo")})
	if err != nil {
		return nil, err
	}
	item, err := w.GetItem(r.Context(), &workv1.GetItemRequest{Key: chi.URLParam(r, "key")})
	if err != nil {
		return nil, err
	}
	if item.GetItem() == nil || repo.GetRepo().GetId() == "" || item.GetItem().GetRepoId() != repo.GetRepo().GetId() {
		return nil, status.Error(codes.NotFound, "work item not found in this repository")
	}
	return item.GetItem(), nil
}
func guiRun(r *http.Request, g gitv1.GitServiceClient, rv reviewsv1.ReviewsServiceClient) (*reviewsv1.Run, error) {
	n, err := strconv.ParseInt(chi.URLParam(r, "number"), 10, 32)
	if err != nil || n <= 0 {
		return nil, status.Error(codes.InvalidArgument, "positive run number required")
	}
	repo, err := g.GetRepo(r.Context(), &gitv1.GetRepoRequest{Name: chi.URLParam(r, "repo")})
	if err != nil {
		return nil, err
	}
	out, err := rv.GetRun(r.Context(), &reviewsv1.GetRunRequest{RepoId: repo.GetRepo().GetId(), Number: int32(n)})
	if err != nil {
		return nil, err
	}
	if out.GetRun() == nil || repo.GetRepo().GetId() == "" || out.GetRun().GetRepoId() != repo.GetRepo().GetId() {
		return nil, status.Error(codes.NotFound, "run not found in this repository")
	}
	return out.GetRun(), nil
}
func guiItemJSON(item *workv1.WorkItem) map[string]any {
	out := WorkItemJSON(item)
	out["awaiting_approval"] = item.GetAwaitingApproval()
	out["maintenance_proposal"] = item.GetMaintenanceProposal()
	out["execution_claimed"] = item.GetExecutionClaimed()
	return out
}
func decodeGUI(wr http.ResponseWriter, r *http.Request, v any) error {
	defer r.Body.Close()
	d := json.NewDecoder(http.MaxBytesReader(wr, r.Body, 1<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return errors.New("exactly one JSON body is required")
	}
	return nil
}

// Compare-and-swap failures are conflicts, not server faults.
func guiStatus(err error) int {
	if status.Code(err) == codes.Aborted {
		return http.StatusConflict
	}
	return StatusFromGRPC(err)
}

func writeGUIReviews(wr http.ResponseWriter, r *http.Request, rv reviewsv1.ReviewsServiceClient, id string) {
	out, err := rv.ListReviews(r.Context(), &reviewsv1.ListReviewsRequest{RunId: id})
	if err != nil {
		WriteError(wr, guiStatus(err), err)
		return
	}
	rows := []map[string]any{}
	for _, v := range out.GetReviews() {
		rows = append(rows, map[string]any{"reviewer_id": v.GetReviewerId(), "reviewer_kind": v.GetReviewerKind(), "verdict": v.GetVerdict(), "summary": v.GetSummary(), "source_sha": v.GetSourceSha(), "created_at": v.GetCreatedAt()})
	}
	WriteJSON(wr, 200, map[string]any{"reviews": rows, "current_source_sha": out.GetCurrentSourceSha(), "current_source_available": out.GetCurrentSourceAvailable()})
}
