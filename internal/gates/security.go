package gates

import (
	"context"
	"encoding/json"
	"fmt"
)

// securityReport is the structured JSON procoder's "security" command
// prints to stdout: secret-scanner and static-analysis findings.
type securityReport struct {
	Secrets []string `json:"secrets"`
	SAST    []string `json:"sast"`
}

// runSecurity evaluates the security gate by invoking procoder's "security"
// command. A secret finding fails the gate independent of the SAST result
// and independent of the process exit code, since the spec marks secret
// leaks as unconditionally blocking.
func runSecurity(ctx context.Context, in Input) (Evaluation, error) {
	stdout, exitCode, err := in.Proc(ctx, in.WorkDir, "security")
	if err != nil {
		return newEvaluation(in, "security", "error", fmt.Sprintf("procoder security: %v", err)), nil
	}

	var report securityReport
	if jsonErr := json.Unmarshal(stdout, &report); jsonErr != nil {
		return newEvaluation(in, "security", "error", fmt.Sprintf("parse procoder security output: %v", jsonErr)), nil
	}

	if len(report.Secrets) > 0 {
		return newEvaluation(in, "security", "fail", fmt.Sprintf("secrets found: %v", report.Secrets)), nil
	}
	if exitCode != 0 {
		return newEvaluation(in, "security", "fail",
			fmt.Sprintf("procoder security exited %d: sast findings %v", exitCode, report.SAST)), nil
	}
	if len(report.SAST) > 0 {
		return newEvaluation(in, "security", "fail", fmt.Sprintf("sast findings: %v", report.SAST)), nil
	}
	return newEvaluation(in, "security", "pass", "no secrets or sast findings"), nil
}
