package ci_test

import (
	"testing"

	"github.com/novaforge/novaforge/internal/ci"
)

// TestParseWorkflowCarriesSecretsAndEnvironment pins what the broker is asked
// for: the declared secrets and environment reach the job, and a name that
// cannot be an environment variable or an environment that does not exist is
// refused where its author sees the error, not at dispatch.
func TestParseWorkflowCarriesSecretsAndEnvironment(t *testing.T) {
	wf, err := ci.ParseWorkflow([]byte("jobs:\n  deploy:\n    run: ./deploy.sh\n    environment: production\n    secrets: [DEPLOY_TOKEN]\n"))
	if err != nil {
		t.Fatalf("ParseWorkflow: %v", err)
	}
	job := wf.Jobs["deploy"]
	if job.Environment != "production" || len(job.Secrets) != 1 || job.Secrets[0] != "DEPLOY_TOKEN" {
		t.Fatalf("job = %+v", job)
	}
	for _, bad := range []string{
		"jobs:\n  d:\n    run: x\n    secrets: [deploy-token]\n",
		"jobs:\n  d:\n    run: x\n    environment: qa\n",
		"jobs:\n  d:\n    agent: security\n    secrets: [TOKEN]\n",
	} {
		if _, err := ci.ParseWorkflow([]byte(bad)); err == nil {
			t.Errorf("ParseWorkflow accepted %q", bad)
		}
	}
}
