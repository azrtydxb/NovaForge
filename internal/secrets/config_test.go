package secrets_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/secrets"
)

func TestConfiguredBrokerRejectsMalformedConfiguration(t *testing.T) {
	cfg := secrets.OpenBaoConfig{Endpoint: "https://issuer.invalid", TokenFile: "/mounted/token", Bindings: []secrets.OpenBaoBinding{{OrgID: uuid.New(), Name: "TOKEN", Environment: "staging", Path: "database/creds/owned", Method: "GET", Field: "value"}}}
	raw, _ := json.Marshal(cfg)
	for name, contents := range map[string]string{"valid": string(raw), "unknown-field": string(raw[:len(raw)-1]) + `,"token":"must-not-be-inline"}`, "trailing": string(raw) + ` {}`, "no-bindings": `{"endpoint":"https://issuer.invalid","token_file":"/token"}`} {
		t.Run(name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(file, []byte(contents), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := secrets.NewConfiguredBroker(nil, []byte("key"), file)
			if name == "valid" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("bad configuration accepted")
			}
		})
	}
	cfg.CAFile = filepath.Join(t.TempDir(), "missing")
	if _, err := secrets.NewOpenBaoBroker(nil, []byte("key"), cfg); err == nil {
		t.Fatal("missing CA accepted")
	}
	if err := os.WriteFile(cfg.CAFile, []byte("not a CA"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := secrets.NewOpenBaoBroker(nil, []byte("key"), cfg); err == nil {
		t.Fatal("invalid CA accepted")
	}
}
