package cli_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/novaforge/novaforge/internal/cli"
)

func stubEdge(t *testing.T, routes map[string]func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h, ok := routes[r.Method+" "+r.URL.Path]; ok {
			h(w, r)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"no stub for ` + r.Method + " " + r.URL.Path + `"}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRepoCreateAndList(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	created := ""
	srv := stubEdge(t, map[string]func(http.ResponseWriter, *http.Request){
		"POST /api/v1/orgs/acme/repos": func(w http.ResponseWriter, r *http.Request) {
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			created = body["name"]
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{"name": created, "default_branch": "main"})
		},
		"GET /api/v1/orgs/acme/repos": func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]any{
				"repos": []map[string]string{{"name": "widgets", "default_branch": "main"}},
			})
		},
	})

	if err := cli.SaveConfig(cli.Config{Server: srv.URL, Token: "nf_t", Org: "acme"}); err != nil {
		t.Fatal(err)
	}

	var out, errb bytes.Buffer
	if code := cli.Execute([]string{"repo", "create", "widgets"}, &out, &errb); code != 0 {
		t.Fatalf("repo create exit %d: %s", code, errb.String())
	}
	if created != "widgets" {
		t.Fatalf("server saw name %q", created)
	}

	out.Reset()
	if code := cli.Execute([]string{"repo", "list"}, &out, &errb); code != 0 {
		t.Fatalf("repo list exit %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "widgets") {
		t.Fatalf("want the repository in stdout, got %q", out.String())
	}
}

func TestLoginPersistsToken(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	srv := stubEdge(t, map[string]func(http.ResponseWriter, *http.Request){
		"POST /api/v1/auth/login": func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]any{"session_token": "nf_session_abc", "user_id": "u1"})
		},
	})
	var out, errb bytes.Buffer
	code := cli.Execute([]string{"login", "--server", srv.URL, "--username", "p", "--password", "x"}, &out, &errb)
	if code != 0 {
		t.Fatalf("login exit %d: %s", code, errb.String())
	}
	cfg, err := cli.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Token != "nf_session_abc" {
		t.Fatalf("token not persisted, got %q", cfg.Token)
	}
}

func TestUnknownCommandExitsNonZero(t *testing.T) {
	var out, errb bytes.Buffer
	if code := cli.Execute([]string{"frobnicate"}, &out, &errb); code == 0 {
		t.Fatal("want non-zero exit for an unknown command")
	}
	if !strings.Contains(errb.String(), "frobnicate") {
		t.Fatalf("want the unknown command named in stderr, got %q", errb.String())
	}
}

func TestServerErrorSurfacesToStderr(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	srv := stubEdge(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /api/v1/orgs/acme/repos": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"error":"cross-org access denied"}`))
		},
	})
	cli.SaveConfig(cli.Config{Server: srv.URL, Token: "t", Org: "acme"})
	var out, errb bytes.Buffer
	if code := cli.Execute([]string{"repo", "list"}, &out, &errb); code == 0 {
		t.Fatal("want non-zero exit when the server refuses")
	}
	if !strings.Contains(errb.String(), "cross-org access denied") {
		t.Fatalf("want the server's reason in stderr, got %q", errb.String())
	}
}
