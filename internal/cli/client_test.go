package cli_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/novaforge/novaforge/internal/cli"
)

func TestClientSendsBearerToken(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"ok": "yes"})
	}))
	defer srv.Close()

	c := cli.NewClient(srv.URL, "nf_testtoken")
	var out map[string]string
	if err := c.Do("GET", "/api/v1/user", nil, &out); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if gotAuth != "Bearer nf_testtoken" {
		t.Fatalf("want bearer header, got %q", gotAuth)
	}
}

func TestClientSurfacesServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":"cross-org access denied"}`))
	}))
	defer srv.Close()

	c := cli.NewClient(srv.URL, "t")
	err := c.Do("GET", "/api/v1/orgs/other/repos", nil, nil)
	if err == nil {
		t.Fatal("want error on 403")
	}
	if !strings.Contains(err.Error(), "cross-org access denied") {
		t.Fatalf("want server message in error, got %v", err)
	}
	if !strings.Contains(err.Error(), "403") {
		t.Fatalf("want status in error, got %v", err)
	}
}

func TestConfigFilePermissions(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := cli.SaveConfig(cli.Config{Server: "https://x", Token: "nf_secret"}); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	p := filepath.Join(dir, "novaforge", "config.json")
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("want mode 0600 for a file holding a token, got %o", perm)
	}
}

func TestConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	want := cli.Config{Server: "https://nf.example", Token: "nf_abc", Org: "acme"}
	if err := cli.SaveConfig(want); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	got, err := cli.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got != want {
		t.Fatalf("want %+v, got %+v", want, got)
	}
}
