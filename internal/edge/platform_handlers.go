package edge

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	gatesv1 "github.com/novaforge/novaforge/gen/novaforge/gates/v1"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	graphv1 "github.com/novaforge/novaforge/gen/novaforge/graph/v1"
	"github.com/novaforge/novaforge/internal/approvals"
	"github.com/novaforge/novaforge/internal/mcp"
)

// knowledgeLimit bounds a knowledge search. The screen showing these is a
// reading surface, not an export.
const knowledgeLimit = 50

// addPlatformHandlers mounts the surfaces that let a person see what the
// platform knows and what governs it: project knowledge, the engineering
// graph, the approval policy, and the MCP tools this deployment exposes.
//
// The approval policy and the MCP tool list are served from this process
// rather than fetched: both are compiled-in facts about the platform, and
// asking a service for them over the network would only add a way for the
// two to disagree.
func addPlatformHandlers(
	h map[string]http.HandlerFunc,
	g gitv1.GitServiceClient,
	graph graphv1.GraphServiceClient,
	gates gatesv1.GatesServiceClient,
) {
	if gates != nil {
		h["listSecrets"] = func(wr http.ResponseWriter, r *http.Request) {
			resp, err := gates.ListSecrets(r.Context(), &gatesv1.ListSecretsRequest{})
			if err != nil {
				WriteError(wr, StatusFromGRPC(err), err)
				return
			}
			out := make([]map[string]any, 0, len(resp.GetSecrets()))
			for _, s := range resp.GetSecrets() {
				out = append(out, map[string]any{
					"name":        s.GetName(),
					"environment": s.GetEnvironment(),
				})
			}
			WriteJSON(wr, http.StatusOK, map[string]any{"secrets": out})
		}

		h["listLeases"] = func(wr http.ResponseWriter, r *http.Request) {
			resp, err := gates.ListLeases(r.Context(), &gatesv1.ListLeasesRequest{})
			if err != nil {
				WriteError(wr, StatusFromGRPC(err), err)
				return
			}
			out := make([]map[string]any, 0, len(resp.GetLeases()))
			for _, l := range resp.GetLeases() {
				out = append(out, map[string]any{
					"id":          l.GetId(),
					"secret_name": l.GetSecretName(),
					"run_id":      l.GetRunId(),
					"state":       l.GetState(),
					"expires_at":  l.GetExpiresAt(),
				})
			}
			WriteJSON(wr, http.StatusOK, map[string]any{"leases": out})
		}

		h["revokeLease"] = func(wr http.ResponseWriter, r *http.Request) {
			if _, err := gates.RevokeLease(r.Context(), &gatesv1.RevokeLeaseRequest{
				Id: chi.URLParam(r, "id"),
			}); err != nil {
				WriteError(wr, StatusFromGRPC(err), err)
				return
			}
			WriteJSON(wr, http.StatusOK, map[string]any{"revoked": chi.URLParam(r, "id")})
		}
	}

	h["approvalPolicy"] = func(wr http.ResponseWriter, r *http.Request) {
		rules := approvals.Rules()
		out := make([]map[string]any, 0, len(rules))
		for _, rule := range rules {
			policy := string(rule.Decision)
			if rule.GrantDependent {
				// "human" and "forbidden" are the same action seen through
				// two different grants; saying so is more useful than
				// showing whichever one this reader happens to have.
				policy = string(rule.Decision) + " / forbidden without the capability"
			}
			out = append(out, map[string]any{
				"action": humanAction(rule.Action),
				"policy": policy,
			})
		}
		WriteJSON(wr, http.StatusOK, map[string]any{"rules": out})
	}

	h["mcpTools"] = func(wr http.ResponseWriter, r *http.Request) {
		defs := mcp.ToolDefs()
		out := make([]map[string]any, 0, len(defs))
		for _, d := range defs {
			out = append(out, map[string]any{
				"name":        d.Name,
				"description": d.Description,
			})
		}
		WriteJSON(wr, http.StatusOK, map[string]any{
			"tools":    out,
			"revision": mcp.ProtocolVersion,
		})
	}

	if graph == nil {
		return
	}

	repoID := func(r *http.Request) (string, error) {
		resp, err := g.GetRepo(r.Context(), &gitv1.GetRepoRequest{Name: chi.URLParam(r, "repo")})
		if err != nil {
			return "", err
		}
		return resp.GetRepo().GetId(), nil
	}

	h["searchKnowledge"] = func(wr http.ResponseWriter, r *http.Request) {
		rid, err := repoID(r)
		if err != nil {
			WriteError(wr, StatusFromGRPC(err), err)
			return
		}
		resp, err := graph.SearchKnowledge(r.Context(), &graphv1.SearchKnowledgeRequest{
			RepoId: rid,
			Query:  r.URL.Query().Get("q"),
			K:      knowledgeLimit,
		})
		if err != nil {
			WriteError(wr, StatusFromGRPC(err), err)
			return
		}
		out := make([]map[string]any, 0, len(resp.GetEntries()))
		for _, e := range resp.GetEntries() {
			out = append(out, map[string]any{
				"id":         e.GetId(),
				"key":        e.GetKey(),
				"kind":       e.GetKind(),
				"title":      e.GetTitle(),
				"body":       e.GetBody(),
				"created_at": e.GetCreatedAt(),
			})
		}
		WriteJSON(wr, http.StatusOK, map[string]any{"entries": out})
	}

	// getSymbolRelations answers the questions a file tree cannot: what this
	// depends on, what depends on it, what tests cover it, and which Work
	// Item last changed it. They are four RPCs because the graph stores four
	// different edge kinds; a caller wants them together.
	h["getSymbolRelations"] = func(wr http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		if name == "" {
			WriteError(wr, http.StatusBadRequest, errMissingSymbol)
			return
		}
		rid, err := repoID(r)
		if err != nil {
			WriteError(wr, StatusFromGRPC(err), err)
			return
		}
		ctx := r.Context()

		sym, err := graph.GetSymbol(ctx, &graphv1.GetSymbolRequest{RepoId: rid, Name: name})
		if err != nil {
			WriteError(wr, StatusFromGRPC(err), err)
			return
		}
		body := map[string]any{
			"symbol":          symbolJSON(sym.GetSymbol()),
			"dependencies":    []map[string]any{},
			"dependents":      []map[string]any{},
			"tests":           []string{},
			"last_changed_by": "",
		}
		if sym.GetSymbol() == nil || sym.GetSymbol().GetName() == "" {
			body["symbol"] = nil
			WriteJSON(wr, http.StatusOK, body)
			return
		}

		// Each relation is best-effort: a graph that has indexed symbols but
		// not yet their test coverage should still answer the parts it knows,
		// rather than failing the whole question.
		if deps, err := graph.Dependencies(ctx, &graphv1.DependenciesRequest{RepoId: rid, Symbol: name}); err == nil {
			body["dependencies"] = nodesJSON(deps.GetNodes())
		}
		if dep, err := graph.Dependents(ctx, &graphv1.DependentsRequest{RepoId: rid, Symbol: name}); err == nil {
			body["dependents"] = nodesJSON(dep.GetNodes())
		}
		if tests, err := graph.TestsCovering(ctx, &graphv1.TestsCoveringRequest{RepoId: rid, Symbol: name}); err == nil {
			body["tests"] = tests.GetTests()
		}
		if last, err := graph.LastChangedBy(ctx, &graphv1.LastChangedByRequest{RepoId: rid, Symbol: name}); err == nil {
			body["last_changed_by"] = last.GetWorkItemKey()
		}
		WriteJSON(wr, http.StatusOK, body)
	}
}

func symbolJSON(s *graphv1.Symbol) map[string]any {
	if s == nil {
		return nil
	}
	return map[string]any{
		"id":         s.GetId(),
		"name":       s.GetName(),
		"kind":       s.GetKind(),
		"path":       s.GetPath(),
		"start_line": s.GetStartLine(),
		"signature":  s.GetSignature(),
	}
}

func nodesJSON(nodes []*graphv1.Node) []map[string]any {
	out := make([]map[string]any, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, map[string]any{"key": n.GetKey(), "kind": n.GetKind()})
	}
	return out
}

// humanAction renders an Action for a reader. The enum's values are wire
// identifiers; a settings screen shows people what they mean.
func humanAction(a approvals.Action) string {
	switch a {
	case approvals.ActionReadSource:
		return "Read source"
	case approvals.ActionModifyWorkspace:
		return "Modify isolated workspace"
	case approvals.ActionAddDependency:
		return "Add a dependency"
	case approvals.ActionChangeDBSchema:
		return "Change the database schema"
	case approvals.ActionAccessSecret:
		return "Access a temporary secret"
	case approvals.ActionDeployStaging:
		return "Deploy to staging"
	case approvals.ActionDeployProduction:
		return "Deploy to production"
	}
	return string(a)
}

// errMissingSymbol is returned when a symbol lookup names nothing.
var errMissingSymbol = errors.New("name is required")
