package tools

import (
	"context"
	"encoding/json"
	"fmt"
)

// WorkItemSummary is the read-only view of a Work Item returned by
// work.get.
type WorkItemSummary struct {
	ID    string `json:"id"`
	Key   string `json:"key"`
	Goal  string `json:"goal"`
	State string `json:"state"`
}

type workGetArgs struct {
	WorkItemID string `json:"work_item_id"`
}

type workCommentArgs struct {
	WorkItemID string `json:"work_item_id"`
	Body       string `json:"body"`
}

type workCommentResult struct {
	OK bool `json:"ok"`
}

type gateStatusArgs struct {
	RunID string `json:"run_id"`
}

type gateStatusResult struct {
	Gates map[string]string `json:"gates"`
}

func registerWorkTools(r *Registry) {
	r.Register("work.get", workGetHandler)
	r.Register("work.comment", workCommentHandler)
	r.Register("gate.status", gateStatusHandler)
}

func workGetHandler(ctx context.Context, rt Runtime, argsJSON []byte) ([]byte, error) {
	var args workGetArgs
	if err := unmarshalArgs("work.get", argsJSON, &args); err != nil {
		return nil, err
	}
	if rt.Work == nil {
		return nil, fmt.Errorf("work.get: no work client configured")
	}
	item, err := rt.Work.Get(ctx, args.WorkItemID)
	if err != nil {
		return nil, fmt.Errorf("work.get: %w", err)
	}
	return json.Marshal(item)
}

func workCommentHandler(ctx context.Context, rt Runtime, argsJSON []byte) ([]byte, error) {
	var args workCommentArgs
	if err := unmarshalArgs("work.comment", argsJSON, &args); err != nil {
		return nil, err
	}
	if rt.Work == nil {
		return nil, fmt.Errorf("work.comment: no work client configured")
	}
	if err := rt.Work.Comment(ctx, args.WorkItemID, args.Body); err != nil {
		return nil, fmt.Errorf("work.comment: %w", err)
	}
	return json.Marshal(workCommentResult{OK: true})
}

func gateStatusHandler(ctx context.Context, rt Runtime, argsJSON []byte) ([]byte, error) {
	var args gateStatusArgs
	if err := unmarshalArgs("gate.status", argsJSON, &args); err != nil {
		return nil, err
	}
	if rt.Reviews == nil {
		return nil, fmt.Errorf("gate.status: no reviews client configured")
	}
	gates, err := rt.Reviews.GateStatus(ctx, args.RunID)
	if err != nil {
		return nil, fmt.Errorf("gate.status: %w", err)
	}
	return json.Marshal(gateStatusResult{Gates: gates})
}
