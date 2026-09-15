package edge

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gatesv1 "github.com/novaforge/novaforge/gen/novaforge/gates/v1"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	graphv1 "github.com/novaforge/novaforge/gen/novaforge/graph/v1"
	"github.com/novaforge/novaforge/internal/approvals"
	"github.com/novaforge/novaforge/internal/mcp"
	"github.com/novaforge/novaforge/internal/tools"
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

		// putSecret stores a value and answers with its name and environment
		// only. No route anywhere returns a secret's value: the broker hands
		// it to a job through a lease, and a person who needs to see it again
		// has the place they got it from.
		h["putSecret"] = func(wr http.ResponseWriter, r *http.Request) {
			var body struct {
				Name        string `json:"name"`
				Environment string `json:"environment"`
				Value       string `json:"value"`
			}
			if err := decode(r, &body); err != nil {
				WriteError(wr, http.StatusBadRequest, err)
				return
			}
			resp, err := gates.PutSecret(r.Context(), &gatesv1.PutSecretRequest{
				Name: body.Name, Environment: body.Environment, Value: body.Value,
			})
			if err != nil {
				WriteError(wr, StatusFromGRPC(err), err)
				return
			}
			WriteJSON(wr, http.StatusCreated, map[string]any{
				"name":        resp.GetSecret().GetName(),
				"environment": resp.GetSecret().GetEnvironment(),
			})
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
				"id":            e.GetId(),
				"key":           e.GetKey(),
				"kind":          e.GetKind(),
				"title":         e.GetTitle(),
				"body":          e.GetBody(),
				"created_at":    e.GetCreatedAt(),
				"source_run_id": e.GetSourceRunId(),
			})
		}
		WriteJSON(wr, http.StatusOK, map[string]any{"entries": out, "mode": resp.GetMode()})
	}

	// recordKnowledge is how a person records a decision or a correction.
	// The Knowledge screen told people they record knowledge "when they
	// correct an agent", and offered no way to: only agents could write.
	h["recordKnowledge"] = func(wr http.ResponseWriter, r *http.Request) {
		var req struct {
			Kind  string `json:"kind"`
			Title string `json:"title"`
			Body  string `json:"body"`
		}
		if err := decode(r, &req); err != nil {
			WriteError(wr, http.StatusBadRequest, err)
			return
		}
		req.Title, req.Body = strings.TrimSpace(req.Title), strings.TrimSpace(req.Body)
		if req.Title == "" || req.Body == "" {
			WriteError(wr, http.StatusBadRequest, errKnowledgeFields)
			return
		}
		rid, err := repoID(r)
		if err != nil {
			WriteError(wr, StatusFromGRPC(err), err)
			return
		}
		key := tools.KnowledgeKey(req.Kind, req.Title)
		resp, err := graph.RecordKnowledge(r.Context(), &graphv1.RecordKnowledgeRequest{
			RepoId: rid, Key: key, Kind: req.Kind, Title: req.Title, Body: req.Body,
		})
		if err != nil {
			WriteError(wr, StatusFromGRPC(err), err)
			return
		}
		WriteJSON(wr, http.StatusCreated, map[string]any{"id": resp.GetId(), "key": key})
	}

	// getFileRelations is the Graph screen's question about a whole file.
	h["getFileRelations"] = func(wr http.ResponseWriter, r *http.Request) {
		path := strings.TrimSpace(r.URL.Query().Get("path"))
		if path == "" {
			WriteError(wr, http.StatusBadRequest, errMissingPath)
			return
		}
		rid, err := repoID(r)
		if err != nil {
			WriteError(wr, StatusFromGRPC(err), err)
			return
		}
		resp, err := graph.FileRelations(r.Context(), &graphv1.FileRelationsRequest{RepoId: rid, Path: path})
		if status.Code(err) == codes.NotFound {
			WriteJSON(wr, http.StatusOK, map[string]any{"indexed": false, "path": path})
			return
		}
		if err != nil {
			WriteError(wr, StatusFromGRPC(err), err)
			return
		}
		symbols := make([]map[string]any, 0, len(resp.GetSymbols()))
		for _, s := range resp.GetSymbols() {
			symbols = append(symbols, symbolJSON(s))
		}
		WriteJSON(wr, http.StatusOK, map[string]any{
			"indexed":     true,
			"path":        path,
			"symbols":     symbols,
			"imports":     nodesJSON(resp.GetImports()),
			"imported_by": nodesJSON(resp.GetImportedBy()),
			"dependents":  nodesJSON(resp.GetDependents()),
			"tests":       nodesJSON(resp.GetTests()),
			"history":     nodesJSON(resp.GetHistory()),
		})
	}

	// searchCode is the code index's only door for a person. The SearchCode
	// RPC was reachable by agents and MCP clients but by no route, so nobody
	// could see whether a push had ever been indexed — and none had.
	h["searchCode"] = func(wr http.ResponseWriter, r *http.Request) {
		q := strings.TrimSpace(r.URL.Query().Get("q"))
		if q == "" {
			WriteError(wr, http.StatusBadRequest, errMissingQuery)
			return
		}
		k := codeSearchLimit
		if raw := r.URL.Query().Get("k"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 || n > codeSearchLimit {
				WriteError(wr, http.StatusBadRequest, errBadSearchLimit)
				return
			}
			k = n
		}
		rid, err := repoID(r)
		if err != nil {
			WriteError(wr, StatusFromGRPC(err), err)
			return
		}
		resp, err := graph.SearchCode(r.Context(), &graphv1.SearchCodeRequest{
			RepoId: rid,
			Query:  q,
			K:      int32(k),
		})
		if err != nil {
			WriteError(wr, StatusFromGRPC(err), err)
			return
		}
		WriteJSON(wr, http.StatusOK, SearchCodeJSON(resp))
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
		if status.Code(err) == codes.NotFound {
			// "Nothing by that name is indexed" is an answer, not a failure.
			sym, err = &graphv1.GetSymbolResponse{}, nil
		}
		if err != nil {
			WriteError(wr, StatusFromGRPC(err), err)
			return
		}
		body := map[string]any{
			"symbol":          symbolJSON(sym.GetSymbol()),
			"dependencies":    []map[string]any{},
			"dependents":      []map[string]any{},
			"tests":           []map[string]any{},
			"last_changed_by": "",
			"last_change":     nil,
			"history":         []map[string]any{},
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
			body["tests"] = nodesJSON(tests.GetNodes())
		}
		if last, err := graph.LastChangedBy(ctx, &graphv1.LastChangedByRequest{RepoId: rid, Symbol: name}); err == nil {
			body["last_changed_by"] = last.GetWorkItemKey()
			body["last_change"] = map[string]any{
				"work_item_key": last.GetWorkItemKey(),
				"commit_sha":    last.GetCommitSha(),
				"author":        last.GetAuthor(),
				"changed_at":    last.GetChangedAt(),
				"message":       last.GetMessage(),
			}
			body["history"] = nodesJSON(last.GetHistory())
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
		// A node's key is an internal identifier. What a reader can use is
		// in its attributes: where a symbol or file is, which package an
		// import names, which commit and Work Item a change was.
		attrs := n.GetAttrs()
		out = append(out, map[string]any{
			"key":           n.GetKey(),
			"kind":          n.GetKind(),
			"name":          attrs["name"],
			"path":          attrs["path"],
			"symbol_kind":   attrs["kind"],
			"start_line":    attrs["start_line"],
			"import_path":   attrs["import_path"],
			"external":      attrs["external"] == "true",
			"sha":           attrs["sha"],
			"author":        attrs["author"],
			"message":       attrs["message"],
			"changed_at":    attrs["changed_at"],
			"work_item_key": attrs["work_item_key"],
		})
	}
	return out
}

var (
	errMissingPath     = errors.New("path is required")
	errKnowledgeFields = errors.New("title and body are required")
)

// humanActionName is humanAction for an action as it arrives over the wire.
func humanActionName(a string) string { return humanAction(approvals.Action(a)) }

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
	case approvals.ActionChangeGateConfig:
		return "Change a gate definition"
	}
	return string(a)
}

// SearchCodeJSON renders a code search. results is always a list, never null,
// and mode says whether the results are nearest by meaning ("semantic") or a
// substring match made because no embedding model answered ("lexical").
func SearchCodeJSON(resp *graphv1.SearchCodeResponse) map[string]any {
	results := make([]map[string]any, 0, len(resp.GetChunks()))
	for _, c := range resp.GetChunks() {
		results = append(results, map[string]any{
			"path":       c.GetPath(),
			"start_line": c.GetStartLine(),
			"end_line":   c.GetEndLine(),
			"score":      c.GetScore(),
			"text":       c.GetText(),
		})
	}
	return map[string]any{"mode": resp.GetMode(), "results": results}
}

// codeSearchLimit bounds a code search: enough to scan, not an export.
const codeSearchLimit = 25

var (
	errMissingQuery   = errors.New("q is required")
	errBadSearchLimit = errors.New("k must be a number from 1 to 25")
)

// errMissingSymbol is returned when a symbol lookup names nothing.
var errMissingSymbol = errors.New("name is required")
