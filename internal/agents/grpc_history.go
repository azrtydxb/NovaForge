package agents

import (
	"context"
	"encoding/json"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentsv1 "github.com/novaforge/novaforge/gen/novaforge/agents/v1"
)

// ListStats returns each agent's run history for the caller's organization.
func (g *GRPCServer) ListStats(ctx context.Context, _ *agentsv1.ListStatsRequest) (*agentsv1.ListStatsResponse, error) {
	found, err := g.Store.ListStats(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list agent stats: %v", err)
	}
	out := make([]*agentsv1.AgentStats, 0, len(found))
	for _, s := range found {
		out = append(out, &agentsv1.AgentStats{
			AgentId:    s.AgentID.String(),
			Runs:       int32(s.Runs),
			Succeeded:  int32(s.Succeeded),
			Failed:     int32(s.Failed),
			OverBudget: int32(s.OverBudget),
			Running:    int32(s.Running),
			TokensUsed: s.TokensUsed,
			TokenLimit: s.TokenLimit,
		})
	}
	return &agentsv1.ListStatsResponse{Stats: out}, nil
}

// ListToolCalls returns the audited record of what one run actually did.
// Every tool call an agent makes is recorded with its arguments and its
// outcome, which is what makes a run's behaviour reviewable rather than
// described.
func (g *GRPCServer) ListToolCalls(ctx context.Context, req *agentsv1.ListToolCallsRequest) (*agentsv1.ListToolCallsResponse, error) {
	runID, err := parseUUID("run_id", req.GetRunId())
	if err != nil {
		return nil, err
	}
	if g.Audit == nil {
		return nil, status.Error(codes.Unimplemented, "this deployment records no tool calls")
	}
	entries, err := g.Audit.List(ctx, runID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list tool calls: %v", err)
	}
	out := make([]*agentsv1.ToolCall, 0, len(entries))
	for _, e := range entries {
		out = append(out, &agentsv1.ToolCall{
			Tool:      e.Tool,
			Outcome:   e.Outcome,
			Args:      compactArgs(e.ArgsJSON),
			Error:     e.Error,
			StartedAt: e.StartedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		})
	}
	return &agentsv1.ListToolCallsResponse{Calls: out}, nil
}

// ListRunsForWorkItem returns the agent runs started against a Work Item,
// newest first. An Engineering Run shows the tool calls of the agent run that
// produced it, and this is how the two are connected.
func (g *GRPCServer) ListRunsForWorkItem(ctx context.Context, req *agentsv1.ListRunsForWorkItemRequest) (*agentsv1.ListRunsForWorkItemResponse, error) {
	id, err := parseUUID("work_item_id", req.GetWorkItemId())
	if err != nil {
		return nil, err
	}
	ids, err := g.Store.RunsForWorkItem(ctx, id)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list runs for work item: %v", err)
	}
	out := make([]string, 0, len(ids))
	runs := make([]*agentsv1.Run, 0, len(ids))
	for _, r := range ids {
		out = append(out, r.String())
		// One read per run: a Work Item has a handful of runs, and GetRun is
		// the one place a run row is decoded, so the list cannot drift from
		// what GetRun reports for the same run.
		run, err := g.Store.GetRun(ctx, r)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "read run %s: %v", r, err)
		}
		runs = append(runs, toProtoRun(run))
	}
	return &agentsv1.ListRunsForWorkItemResponse{RunIds: out, Runs: runs}, nil
}

// compactArgs renders a tool call's arguments as one line. The audit log
// stores the JSON it was called with; a reader wants to see it, not parse it.
func compactArgs(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	out, err := json.Marshal(v)
	if err != nil {
		return string(raw)
	}
	return string(out)
}
