package edge

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	mcpv1 "github.com/novaforge/novaforge/gen/novaforge/mcp/v1"
)

// addMcpServerHandlers mounts an organization's register of external MCP
// servers. Who may decide is enforced by mcp-server, not here: the edge has
// no role data, and a second check here would be a second place to be wrong.
func addMcpServerHandlers(h map[string]http.HandlerFunc, m mcpv1.McpServiceClient) {
	h["listMcpServers"] = func(wr http.ResponseWriter, r *http.Request) {
		resp, err := m.ListServers(r.Context(), &mcpv1.ListServersRequest{Status: r.URL.Query().Get("status")})
		if err != nil {
			WriteError(wr, StatusFromGRPC(err), err)
			return
		}
		out := make([]map[string]any, 0, len(resp.GetServers()))
		for _, s := range resp.GetServers() {
			out = append(out, mcpServerJSON(s))
		}
		WriteJSON(wr, http.StatusOK, map[string]any{"servers": out, "can_decide": resp.GetCanDecide()})
	}

	h["requestMcpServer"] = func(wr http.ResponseWriter, r *http.Request) {
		var req struct {
			Name        string `json:"name"`
			URL         string `json:"url"`
			Transport   string `json:"transport"`
			Description string `json:"description"`
		}
		if err := decode(r, &req); err != nil {
			WriteError(wr, http.StatusBadRequest, err)
			return
		}
		resp, err := m.RequestServer(r.Context(), &mcpv1.RequestServerRequest{
			Name: req.Name, Url: req.URL, Transport: req.Transport, Description: req.Description,
		})
		if err != nil {
			WriteError(wr, StatusFromGRPC(err), err)
			return
		}
		WriteJSON(wr, http.StatusCreated, mcpServerJSON(resp.GetServer()))
	}

	h["decideMcpServer"] = func(wr http.ResponseWriter, r *http.Request) {
		// The decision is a word, not a boolean: an omitted boolean decodes
		// as false, which would turn a malformed approval into a rejection.
		var req struct {
			Decision string `json:"decision"`
			Reason   string `json:"reason"`
		}
		if err := decode(r, &req); err != nil {
			WriteError(wr, http.StatusBadRequest, err)
			return
		}
		var approve bool
		switch req.Decision {
		case "approve":
			approve = true
		case "reject":
		default:
			WriteError(wr, http.StatusBadRequest, errors.New(`decision must be "approve" or "reject"`))
			return
		}
		resp, err := m.DecideServer(r.Context(), &mcpv1.DecideServerRequest{
			Id: chi.URLParam(r, "id"), Approve: approve, Reason: req.Reason,
		})
		if err != nil {
			WriteError(wr, StatusFromGRPC(err), err)
			return
		}
		WriteJSON(wr, http.StatusOK, mcpServerJSON(resp.GetServer()))
	}

	h["revokeMcpServer"] = func(wr http.ResponseWriter, r *http.Request) {
		resp, err := m.RevokeServer(r.Context(), &mcpv1.RevokeServerRequest{
			Id: chi.URLParam(r, "id"), Reason: r.URL.Query().Get("reason"),
		})
		if err != nil {
			WriteError(wr, StatusFromGRPC(err), err)
			return
		}
		WriteJSON(wr, http.StatusOK, mcpServerJSON(resp.GetServer()))
	}
}

func mcpServerJSON(s *mcpv1.McpServer) map[string]any {
	return map[string]any{
		"id":           s.GetId(),
		"name":         s.GetName(),
		"url":          s.GetUrl(),
		"transport":    s.GetTransport(),
		"description":  s.GetDescription(),
		"status":       s.GetStatus(),
		"requested_by": s.GetRequestedBy(),
		"decided_by":   s.GetDecidedBy(),
		"decided_at":   s.GetDecidedAt(),
		"reason":       s.GetReason(),
		"created_at":   s.GetCreatedAt(),
	}
}
