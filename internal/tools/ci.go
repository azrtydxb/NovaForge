package tools

import (
	"context"
	"encoding/json"
	"fmt"
)

type ciRunTestArgs struct {
	Repo  string `json:"repo"`
	Ref   string `json:"ref"`
	Suite string `json:"suite"`
}

type ciRunTestResult struct {
	RunID string `json:"run_id"`
}

type ciGetLogsArgs struct {
	JobID string `json:"job_id"`
}

type ciGetLogsResult struct {
	Log string `json:"log"`
}

func registerCITools(r *Registry) {
	r.Register("ci.run_test", ciRunTestHandler)
	r.Register("ci.get_logs", ciGetLogsHandler)
}

func ciRunTestHandler(ctx context.Context, rt Runtime, argsJSON []byte) ([]byte, error) {
	var args ciRunTestArgs
	if err := unmarshalArgs("ci.run_test", argsJSON, &args); err != nil {
		return nil, err
	}
	if rt.CI == nil {
		return nil, fmt.Errorf("ci.run_test: no ci client configured")
	}
	runID, err := rt.CI.RunTest(ctx, args.Repo, args.Ref, args.Suite)
	if err != nil {
		return nil, fmt.Errorf("ci.run_test: %w", err)
	}
	return json.Marshal(ciRunTestResult{RunID: runID})
}

func ciGetLogsHandler(ctx context.Context, rt Runtime, argsJSON []byte) ([]byte, error) {
	var args ciGetLogsArgs
	if err := unmarshalArgs("ci.get_logs", argsJSON, &args); err != nil {
		return nil, err
	}
	if rt.CI == nil {
		return nil, fmt.Errorf("ci.get_logs: no ci client configured")
	}
	log, err := rt.CI.GetLogs(ctx, args.JobID)
	if err != nil {
		return nil, fmt.Errorf("ci.get_logs: %w", err)
	}
	return json.Marshal(ciGetLogsResult{Log: log})
}
