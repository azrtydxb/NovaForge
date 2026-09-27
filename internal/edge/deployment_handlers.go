package edge

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	deploymentv1 "github.com/novaforge/novaforge/gen/novaforge/deployment/v1"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func addDeploymentHandlers(h map[string]http.HandlerFunc, g gitv1.GitServiceClient, d deploymentv1.DeploymentServiceClient) {
	if g == nil || d == nil {
		return
	}
	write := func(w http.ResponseWriter, p proto.Message) {
		raw, err := (protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true}).Marshal(p)
		if err != nil {
			WriteError(w, 500, err)
			return
		}
		WriteJSON(w, 200, json.RawMessage(raw))
	}
	repo := func(w http.ResponseWriter, r *http.Request) string {
		out, err := g.GetRepo(r.Context(), &gitv1.GetRepoRequest{Name: chi.URLParam(r, "repo")})
		if err != nil {
			WriteError(w, StatusFromGRPC(err), err)
			return ""
		}
		return out.GetRepo().GetId()
	}
	h["listDeploymentTargets"] = func(w http.ResponseWriter, r *http.Request) {
		id := repo(w, r)
		if id == "" {
			return
		}
		out, err := d.ListTargets(r.Context(), &deploymentv1.ListTargetsRequest{RepoId: id})
		if err != nil {
			WriteError(w, StatusFromGRPC(err), err)
			return
		}
		write(w, out)
	}
	h["listDeployments"] = func(w http.ResponseWriter, r *http.Request) {
		id := repo(w, r)
		if id == "" {
			return
		}
		out, err := d.ListDeployments(r.Context(), &deploymentv1.ListDeploymentsRequest{RepoId: id})
		if err != nil {
			WriteError(w, StatusFromGRPC(err), err)
			return
		}
		write(w, out)
	}
	h["requestDeployment"] = func(w http.ResponseWriter, r *http.Request) {
		id := repo(w, r)
		if id == "" {
			return
		}
		var body struct {
			ID       string `json:"id"`
			RunID    string `json:"run_id"`
			Target   string `json:"target"`
			Artifact string `json:"artifact"`
		}
		if err := decode(r, &body); err != nil {
			WriteError(w, 400, err)
			return
		}
		if body.ID == "" {
			body.ID = uuid.NewString()
		}
		out, err := d.RequestDeployment(r.Context(), &deploymentv1.RequestDeploymentRequest{Id: body.ID, RunId: body.RunID, RepoId: id, Target: body.Target, Artifact: body.Artifact})
		if err != nil {
			WriteError(w, StatusFromGRPC(err), err)
			return
		}
		write(w, out.GetOperation())
	}
	for _, action := range []string{"get", "execute", "retry", "reconcile"} {
		h[action+"Deployment"] = func(w http.ResponseWriter, r *http.Request) {
			repoID := repo(w, r)
			if repoID == "" {
				return
			}
			id := chi.URLParam(r, "id")
			existing, err := d.GetDeployment(r.Context(), &deploymentv1.GetDeploymentRequest{Id: id})
			if err != nil {
				WriteError(w, StatusFromGRPC(err), err)
				return
			}
			if existing.GetOperation().GetRepoId() != repoID {
				WriteError(w, 404, errors.New("deployment not found in this repository"))
				return
			}
			operation := existing.GetOperation()
			switch action {
			case "execute":
				out, e := d.ExecuteDeployment(r.Context(), &deploymentv1.ExecuteDeploymentRequest{Id: id})
				err = e
				if e == nil {
					operation = out.GetOperation()
				}
			case "retry":
				out, e := d.RetryDeployment(r.Context(), &deploymentv1.RetryDeploymentRequest{Id: id})
				err = e
				if e == nil {
					operation = out.GetOperation()
				}
			case "reconcile":
				out, e := d.ReconcileDeployment(r.Context(), &deploymentv1.ReconcileDeploymentRequest{Id: id})
				err = e
				if e == nil {
					operation = out.GetOperation()
				}
			}
			if err != nil {
				WriteError(w, StatusFromGRPC(err), err)
				return
			}
			write(w, operation)
		}
	}
}
