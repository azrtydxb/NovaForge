package edge_test

import (
	"bufio"
	"context"
	"encoding/json"
	"github.com/novaforge/novaforge/internal/agents"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/events"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	identityv1 "github.com/novaforge/novaforge/gen/novaforge/identity/v1"
	reviewsv1 "github.com/novaforge/novaforge/gen/novaforge/reviews/v1"
	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
	"github.com/novaforge/novaforge/internal/platformtest"
)

// Each GUI integration run owns its database; schema migration and teardown
// never touch another lane's tables or rows.
func guiDatabase(t *testing.T) {
	t.Helper()
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := pgx.Connect(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	name := "gui_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quoted := pgx.Identifier{name}.Sanitize()
	if _, err := owner.Exec(context.Background(), "CREATE DATABASE "+quoted); err != nil {
		_ = owner.Close(context.Background())
		t.Fatal(err)
	}
	t.Cleanup(func() {
		defer owner.Close(context.Background())
		if _, err := owner.Exec(context.Background(), "DROP DATABASE "+quoted); err != nil {
			t.Errorf("drop owned GUI database: %v", err)
		}
	})
	u.Path = "/" + name
	t.Setenv("TEST_DATABASE_URL", u.String())
}
func guiRequest(t *testing.T, method, path, session string, body any, want int) map[string]any {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = strings.NewReader(string(raw))
	}
	req, err := http.NewRequest(method, path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if session != "" {
		req.Header.Set("Authorization", "Bearer "+session)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != want {
		t.Fatalf("%s %s: status=%d want=%d body=%s", method, path, res.StatusCode, want, raw)
	}
	out := map[string]any{}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("invalid JSON: %s", raw)
	}
	return out
}

// Requires the parent-owned five routes and AddGUIHandlers registration. This
// test deliberately remains red until that integration is actually present.
func TestGUIRealWorkAndReviewContracts(t *testing.T) {
	guiDatabase(t)
	p := platformtest.Start(t)
	owner := p.NewUser(t, "guiowner")
	org := p.NewOrg(t, owner, "guiorg")
	repo := p.NewRepo(t, owner, org, "gui", nil)
	other := p.NewRepo(t, owner, org, "other", nil)
	ctx := p.AsUser(owner, org)
	created, err := p.Work.CreateItem(ctx, &workv1.CreateItemRequest{RepoId: repo.ID, Type: "feature", Goal: "old", Acceptance: []string{"keep"}, RequiredGates: []string{"tests"}})
	if err != nil {
		t.Fatal(err)
	}
	base := p.EdgeURL + "/api/v1/orgs/" + org.Name + "/repos/" + repo.Name
	itemURL := base + "/work/" + created.GetItem().GetKey()
	item := guiRequest(t, "GET", itemURL, owner.Session, nil, 200)
	patch := map[string]any{"goal": "revised", "expected": item}
	changed := guiRequest(t, "PATCH", itemURL, owner.Session, patch, 200)
	if changed["goal"] != "revised" || changed["required_gates"].([]any)[0] != "tests" {
		t.Fatalf("intent lost: %v", changed)
	}
	guiRequest(t, "PATCH", itemURL, owner.Session, patch, 409)
	guiRequest(t, "POST", itemURL+"/transitions", owner.Session, map[string]string{"expected_state": "open", "to_state": "blocked"}, 200)
	guiRequest(t, "POST", itemURL+"/transitions", owner.Session, map[string]string{"expected_state": "blocked", "to_state": "done"}, 400)
	guiRequest(t, "PATCH", strings.Replace(itemURL, "/repos/"+repo.Name, "/repos/"+other.Name, 1), owner.Session, patch, 404)
	guiRequest(t, "GET", strings.Replace(itemURL, "/repos/"+repo.Name, "/repos/"+other.Name, 1), owner.Session, nil, 404)
	guiRequest(t, "PATCH", itemURL, "", patch, 401)
	if _, err := p.Git.CreateBranch(ctx, &gitv1.CreateBranchRequest{Repo: repo.Name, Name: "feature", FromRef: "main"}); err != nil {
		t.Fatal(err)
	}
	run, err := p.Reviews.CreateRun(ctx, &reviewsv1.CreateRunRequest{RepoId: repo.ID, Title: "review", SourceRef: "feature", TargetRef: "main"})
	if err != nil {
		t.Fatal(err)
	}
	reviewer := p.NewUser(t, "guireviewer")
	p.AddMember(t, owner, org, reviewer, "member")
	reviewURL := base + "/runs/1/reviews"
	guiRequest(t, "POST", reviewURL, reviewer.Session, map[string]string{"verdict": "approve", "summary": "reviewed revision", "expected_source_sha": repo.Head}, 201)
	got := guiRequest(t, "GET", reviewURL, reviewer.Session, nil, 200)
	rows := got["reviews"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["summary"] != "reviewed revision" || got["current_source_sha"] != repo.Head {
		t.Fatalf("review evidence missing: %v", got)
	}
	if run.GetRun().GetNumber() != 1 {
		t.Fatal("fixture is not isolated")
	}
	outsider := p.NewUser(t, "guioutsider")
	guiRequest(t, "GET", reviewURL, outsider.Session, nil, 403)
}

// An explicit opt-in fixture lets ego-browser operate the real React app
// against this isolated platform. Credentials live only in a mode-0600 file.
func TestGUIBrowserFixture(t *testing.T) {
	dir := os.Getenv("GUI_BROWSER_FIXTURE_DIR")
	if dir == "" {
		t.Skip("interactive ego-browser fixture not requested")
	}
	guiDatabase(t)
	p := platformtest.Start(t)
	owner := p.NewUser(t, "browser")
	org := p.NewOrg(t, owner, "browserorg")
	repo := p.NewRepo(t, owner, org, "browserrepo", map[string]string{"README.md": "# GUI acceptance\n", "why?#.txt": "literal filename content\n"})
	edgeURL := p.EdgeURL
	if os.Getenv("GUI_BROWSER_AUTH_FAILURES") == "1" {
		edgeURL = authenticationFixtureEdge(t, dir, p.EdgeURL, p.Identity)
	}
	data := map[string]string{"edge": edgeURL, "username": owner.Username, "password": owner.Password, "org": org.Name, "repo": repo.Name}
	raw, _ := json.Marshal(data)
	if err := os.WriteFile(filepath.Join(dir, "fixture.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	timer := time.NewTimer(10 * time.Minute)
	defer timer.Stop()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-timer.C:
			t.Fatal("browser fixture timed out")
		case <-ticker.C:
			if _, err := os.Stat(filepath.Join(dir, "done")); err == nil {
				return
			}
		}
	}
}

func TestGUIAuthenticatedAgentEvidenceAndEvents(t *testing.T) {
	guiDatabase(t)
	p := platformtest.Start(t)
	owner := p.NewUser(t, "eventsowner")
	org := p.NewOrg(t, owner, "eventsorg")
	repo := p.NewRepo(t, owner, org, "eventsrepo", nil)
	agent := p.NewAgent(t, owner, org)
	item := p.NewWorkItem(t, owner, org, repo)
	run := p.StartAgentRun(t, owner, org, repo, agent, item)
	base := p.EdgeURL + "/api/v1/orgs/" + org.Name + "/agent-runs/" + run.GetId()
	scope := authz.WithScope(context.Background(), authz.Scope{OrgID: uuid.MustParse(org.ID), ActorID: uuid.MustParse(owner.ID), ActorKind: "user"})
	audit := agents.NewAuditLog(p.Pool)
	callID, err := audit.Record(scope, agents.Entry{RunID: uuid.MustParse(run.GetId()), Tool: "repo.read_file", ArgsJSON: []byte(`{"path":"README.md"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := audit.Complete(scope, callID, "success", ""); err != nil {
		t.Fatal(err)
	}
	tools := guiRequest(t, "GET", base+"/tools", owner.Session, nil, 200)
	if len(tools["calls"].([]any)) != 1 {
		t.Fatalf("missing persisted calls: %v", tools)
	}
	guiRequest(t, "GET", base+"/events", "", nil, 401)
	other := p.NewOrg(t, owner, "otherorg")
	foreign := strings.Replace(base, "/orgs/"+org.Name, "/orgs/"+other.Name, 1)
	guiRequest(t, "GET", foreign+"/events", owner.Session, nil, 404)
	guiRequest(t, "GET", foreign+"/tools", owner.Session, nil, 404)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", base+"/events", nil)
	req.Header.Set("Authorization", "Bearer "+owner.Session)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || response.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("not SSE: %d %v", response.StatusCode, response.Header)
	}
	if err := events.Publish(context.Background(), p.Redis, events.StreamAgentEvents, events.AgentEvent{RunID: uuid.MustParse(run.GetId()), At: time.Now(), Type: "tool_call", Tool: "gui.live.probe", Outcome: "success"}); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(response.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "gui.live.probe") {
			var event map[string]any
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
				t.Fatal(err)
			}
			if event["run_id"] != run.GetId() || event["at"] == "" || event["tool_call"] == nil {
				t.Fatalf("malformed event: %v", event)
			}
			return
		}
	}
	t.Fatalf("live event not delivered: %v", scanner.Err())
}

// Security integration blocker: expiring a cookie alone does not revoke the
// session copied from it. Parent owns identity RPC and shared logout wiring.
func TestGUILogoutRevokesOnlyPresentedSession(t *testing.T) {
	guiDatabase(t)
	p := platformtest.Start(t)
	for _, kind := range []string{"bearer-session", "cookie-session"} {
		t.Run(kind, func(t *testing.T) {
			user := p.NewUser(t, "logout")
			sibling, err := p.Identity.Login(context.Background(), &identityv1.LoginRequest{Username: user.Username, Password: user.Password})
			if err != nil {
				t.Fatal(err)
			}
			req, _ := http.NewRequest("POST", p.EdgeURL+"/api/v1/auth/logout", strings.NewReader(`{}`))
			if kind == "bearer-session" {
				req.Header.Set("Authorization", "Bearer "+user.Session)
			} else {
				req.AddCookie(&http.Cookie{Name: "nf_session", Value: user.Session})
			}
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			res.Body.Close()
			if res.StatusCode != 200 {
				t.Fatalf("logout status=%d", res.StatusCode)
			}
			guiRequest(t, "GET", p.EdgeURL+"/api/v1/user", user.Session, nil, 401)
			guiRequest(t, "GET", p.EdgeURL+"/api/v1/user", sibling.GetSessionToken(), nil, 200)
		})
	}
}
