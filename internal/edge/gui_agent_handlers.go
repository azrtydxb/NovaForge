package edge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
		if _, err := a.GetRun(r.Context(), &agentsv1.GetRunRequest{Id: id}); err != nil {
			WriteError(wr, StatusFromGRPC(err), err)
			return
		}
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		stream, err := a.StreamRunEvents(ctx, &agentsv1.StreamRunEventsRequest{RunId: id})
		if err != nil {
			WriteError(wr, StatusFromGRPC(err), err)
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
		send := func(event string, data any) error {
			raw, err := json.Marshal(data)
			if err != nil {
				return err
			}
			_ = controller.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if _, err = fmt.Fprintf(wr, "event: %s\ndata: %s\n\n", event, raw); err != nil {
				return err
			}
			return controller.Flush()
		}
		// No cursor or exactly-once promise: persisted calls are read separately.
		if err := send("connected", map[string]string{"run_id": id}); err != nil {
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
				if err := send("heartbeat", map[string]string{"run_id": id}); err != nil {
					return
				}
			case msg := <-messages:
				if msg.err != nil {
					if msg.err != io.EOF && ctx.Err() == nil {
						_ = send("error", map[string]string{"error": msg.err.Error()})
					}
					return
				}
				if msg.event.GetRunId() != id {
					continue
				}
				body := map[string]any{"run_id": id, "at": msg.event.GetAt()}
				if c := msg.event.GetToolCall(); c != nil {
					body["tool_call"] = guiToolJSON(c)
				}
				if s := msg.event.GetStateChange(); s != nil {
					body["state_change"] = map[string]string{"from_state": s.GetFromState(), "to_state": s.GetToState()}
				}
				if err := send("message", body); err != nil {
					return
				}
			}
		}
	}
}
