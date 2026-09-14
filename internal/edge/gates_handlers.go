package edge

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	gatesv1 "github.com/novaforge/novaforge/gen/novaforge/gates/v1"
)

// addGateConfigHandlers mounts reading a repository's gate configuration and
// proposing a change to it. There is deliberately no route that writes gate
// configuration directly: a change becomes a run, reviewed like any other.
func addGateConfigHandlers(h map[string]http.HandlerFunc, gates gatesv1.GatesServiceClient) {
	h["listGateConfig"] = func(wr http.ResponseWriter, r *http.Request) {
		resp, err := gates.ListGateConfig(r.Context(), &gatesv1.ListGateConfigRequest{Repo: chi.URLParam(r, "repo")})
		if err != nil {
			WriteError(wr, StatusFromGRPC(err), err)
			return
		}
		out := make([]map[string]any, 0, len(resp.GetGates()))
		for _, g := range resp.GetGates() {
			var params map[string]any
			if err := json.Unmarshal([]byte(g.GetParamsJson()), &params); err != nil {
				WriteError(wr, http.StatusBadGateway, err)
				return
			}
			out = append(out, map[string]any{
				"name":     g.GetName(),
				"declared": g.GetDeclared(),
				"enabled":  g.GetEnabled(),
				"path":     g.GetPath(),
				"params":   params,
			})
		}
		WriteJSON(wr, http.StatusOK, map[string]any{
			"ref": resp.GetRef(), "commit_sha": resp.GetCommitSha(), "gates": out,
		})
	}

	h["proposeGateChange"] = func(wr http.ResponseWriter, r *http.Request) {
		// Pointer and raw fields, because "enabled": false is the request a
		// disable toggle sends, and a plain bool cannot tell it from absent.
		var req struct {
			Enabled *bool           `json:"enabled"`
			Params  json.RawMessage `json:"params"`
		}
		if err := decode(r, &req); err != nil {
			WriteError(wr, http.StatusBadRequest, err)
			return
		}
		in := &gatesv1.ProposeGateChangeRequest{
			Repo: chi.URLParam(r, "repo"), Gate: chi.URLParam(r, "gate"), Enabled: req.Enabled,
		}
		if len(req.Params) > 0 && string(req.Params) != "null" {
			var obj map[string]any
			if err := json.Unmarshal(req.Params, &obj); err != nil {
				WriteError(wr, http.StatusBadRequest, errors.New("params must be a JSON object"))
				return
			}
			compact, err := json.Marshal(obj)
			if err != nil {
				WriteError(wr, http.StatusBadRequest, err)
				return
			}
			in.ParamsJson = string(compact)
		}
		resp, err := gates.ProposeGateChange(r.Context(), in)
		if err != nil {
			WriteError(wr, StatusFromGRPC(err), err)
			return
		}
		WriteJSON(wr, http.StatusCreated, map[string]any{
			"run_id":     resp.GetRunId(),
			"run_number": resp.GetRunNumber(),
			"branch":     resp.GetBranch(),
			"commit_sha": resp.GetCommitSha(),
			"title":      resp.GetTitle(),
		})
	}
}
