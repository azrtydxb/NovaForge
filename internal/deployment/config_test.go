package deployment

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfiguredDeploymentBindsImmutableExecutor(t *testing.T) {
	s, _, _, req, _ := fixture(t)
	credentials := &partialCredentials{}
	helm, _ := recoveryHelm(t, credentials)
	target := s.targets[req.Target]
	config := Config{Targets: []TargetConfig{{Name: target.Name, OrgID: target.OrgID, RepoID: target.RepoID, Environment: target.Environment, Helm: helm.config}}}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "targets.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	deps := Dependencies{ResolveRun: s.resolveRun, Kubernetes: helm.client, Credentials: credentials}
	built, err := NewConfiguredService(s.pool, s.approvals, path, deps)
	if err != nil {
		t.Fatal(err)
	}
	got := built.targets[req.Target]
	if got.Revision != helm.Revision() || got.Destination != helm.Destination() {
		t.Fatal("config did not bind exact immutable executor")
	}
	for _, body := range []string{`null`, `{}`, `{"targets":[]} {}`, strings.TrimSuffix(string(raw), "}") + `,"command":"arbitrary"}`, strings.Repeat(" ", 1<<20+1)} {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err = NewConfiguredService(s.pool, s.approvals, path, deps); err == nil {
			t.Fatal("invalid operator configuration accepted")
		}
	}
	if disabled, err := NewConfiguredService(s.pool, s.approvals, "", Dependencies{}); disabled != nil || err != nil {
		t.Fatal("blank config should explicitly disable service", err)
	}
	if _, err = NewConfiguredService(s.pool, s.approvals, path+"-missing", deps); err == nil {
		t.Fatal("missing config ignored")
	}
}
