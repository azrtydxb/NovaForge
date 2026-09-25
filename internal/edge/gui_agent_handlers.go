package edge

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	agentsv1 "github.com/novaforge/novaforge/gen/novaforge/agents/v1"
)

func guiToolJSON(c *agentsv1.ToolCall) map[string]any {
	return map[string]any{"tool": c.GetTool(), "args": c.GetArgs(), "outcome": c.GetOutcome(), "error": c.GetError(), "started_at": c.GetStartedAt()}
}
func addGUIAgentHandlers(h map[string]http.HandlerFunc, a agentsv1.AgentServiceClient) {
	h["getAgentRunTools"] = func(wr http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if _, err := a.GetRun(r.Context(), &agentsv1.GetRunRequest{Id: id}); err != nil {
			WriteError(wr, StatusFromGRPC(err), err)
			return
		}
		out, err := a.ListToolCalls(r.Context(), &agentsv1.ListToolCallsRequest{RunId: id})
		if err != nil {
			WriteError(wr, StatusFromGRPC(err), err)
			return
		}
		calls := []map[string]any{}
		for _, c := range out.GetCalls() {
			calls = append(calls, guiToolJSON(c))
		}
		WriteJSON(wr, 200, map[string]any{"calls": calls})
	}
	h["streamAgentRunEvents"] = func(wr http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		cursor := r.Header.Get("Last-Event-ID")
		if query := r.URL.Query().Get("after_cursor"); query != "" {
			if cursor != "" && cursor != query {
				WriteError(wr, 400, fmt.Errorf("conflicting event cursors"))
				return
			}
			cursor = query
		}
		if cursor != "" && !validAgentCursor(id, cursor) {
			WriteError(wr, 400, fmt.Errorf("invalid event cursor"))
			return
		}

		if _, err := a.GetRun(r.Context(), &agentsv1.GetRunRequest{Id: id}); err != nil {
			WriteError(wr, StatusFromGRPC(err), err)
			return
		}
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		stream, err := a.StreamRunEvents(ctx, &agentsv1.StreamRunEventsRequest{RunId: id, AfterCursor: cursor})
		if err != nil {
			WriteError(wr, StatusFromGRPC(err), err)
			return
		}
		// Header readiness is an owner acknowledgement of scoped run/cursor
		// validation. A stream RPC handle alone does not carry initial RPC errors.
		readyTimeout := time.AfterFunc(10*time.Second, cancel)
		headers, headerErr := stream.Header()
		if headerErr == nil && (len(headers.Get("x-novaforge-stream-ready")) != 1 || headers.Get("x-novaforge-stream-ready")[0] != "1") {
			// grpc-go may report trailers-only failures as empty Header success.
			// Read that terminal status without ever committing HTTP 200.
			_, headerErr = stream.Recv()
			if headerErr == nil || headerErr == io.EOF {
				headerErr = status.Error(codes.Unavailable, "event stream readiness not acknowledged")
			}
		}
		if !readyTimeout.Stop() {
			WriteError(wr, http.StatusGatewayTimeout, fmt.Errorf("event stream readiness timed out"))
			return
		}
		if headerErr != nil {
			WriteError(wr, StatusFromGRPC(headerErr), headerErr)
			return
		}
		if _, ok := wr.(http.Flusher); !ok {
			WriteError(wr, 500, fmt.Errorf("stream flushing unavailable"))
			return
		}
		wr.Header().Set("Content-Type", "text/event-stream")
		wr.Header().Set("Cache-Control", "no-store")
		wr.Header().Set("X-Accel-Buffering", "no")
		controller := http.NewResponseController(wr)
		send := func(event string, data any, cursor string) error {
			raw, err := json.Marshal(data)
			if err != nil {
				return err
			}
			_ = controller.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if cursor != "" {
				if _, err = fmt.Fprintf(wr, "id: %s\n", cursor); err != nil {
					return err
				}
			}
			if _, err = fmt.Fprintf(wr, "event: %s\ndata: %s\n\n", event, raw); err != nil {
				return err
			}
			return controller.Flush()
		}
		// Cursor-less connected/state/heartbeat frames must not reset tool replay.
		if err := send("connected", map[string]string{"run_id": id}, ""); err != nil {
			return
		}
		type result struct {
			event *agentsv1.StreamRunEventsResponse
			err   error
		}
		messages := make(chan result, 1)
		go func() {
			for {
				event, err := stream.Recv()
				select {
				case messages <- result{event, err}:
				case <-ctx.Done():
					return
				}
				if err != nil {
					return
				}
			}
		}()
		tick := time.NewTicker(15 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if err := send("heartbeat", map[string]string{"run_id": id}, ""); err != nil {
					return
				}
			case msg := <-messages:
				if msg.err != nil {
					if msg.err != io.EOF && ctx.Err() == nil {
						_ = send("error", map[string]string{"error": msg.err.Error()}, "")
					}
					return
				}
				if msg.event.GetRunId() != id {
					continue
				}
				body := map[string]any{"run_id": id, "at": msg.event.GetAt()}
				eventCursor := ""
				if c := msg.event.GetToolCall(); c != nil {
					eventCursor = msg.event.GetCursor()
					if !validAgentCursor(id, eventCursor) {
						_ = send("error", map[string]string{"error": "tool event cursor unavailable"}, "")
						return
					}
					body["cursor"] = eventCursor
					body["tool_call_id"] = msg.event.GetToolCallId()
					body["tool_call"] = guiToolJSON(c)
				}
				if s := msg.event.GetStateChange(); s != nil {
					body["state_change"] = map[string]string{"from_state": s.GetFromState(), "to_state": s.GetToState()}
				}
				if err := send("message", body, eventCursor); err != nil {
					return
				}
			}
		}
	}
}

// Validation is bounded and local; existence/retention remains the owner's
// scoped check. Cursor possession never replaces authorization on reconnect.
func validAgentCursor(run, cursor string) bool {
	if len(cursor) > 64 {
		return false
	}
	id, seq, ok := strings.Cut(cursor, ":")
	if !ok || id != run {
		return false
	}
	parsed, err := uuid.Parse(id)
	if err != nil || parsed.String() != id {
		return false
	}
	n, err := strconv.ParseUint(seq, 10, 64)
	return err == nil && n > 0 && strconv.FormatUint(n, 10) == seq
}
