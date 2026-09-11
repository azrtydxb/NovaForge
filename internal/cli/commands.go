package cli

import (
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"
)

// command is one nf subcommand. Subcommands are dispatched from a table so the
// help text cannot drift from what is actually runnable.
type command struct {
	name    string
	summary string
	run     func(args []string, stdout, stderr io.Writer) error
}

func commands() map[string]command {
	return map[string]command{
		"login":     {"login", "Authenticate and store a session token", cmdLogin},
		"org":       {"org", "Manage organizations", cmdOrg},
		"repo":      {"repo", "Manage repositories", cmdRepo},
		"work":      {"work", "Manage Work Items", cmdWork},
		"run":       {"run", "Inspect and merge Engineering Runs", cmdRun},
		"whoami":    {"whoami", "Show the authenticated user", cmdWhoami},
		"ci":        {"ci", "Inspect CI runs, logs and artifacts", cmdCI},
		"dashboard": {"dashboard", "Show what needs human attention", cmdDashboard},
	}
}

// Execute dispatches argv and returns the process exit code. Writers are
// injected so the whole CLI is testable without touching the real stdout.
func Execute(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		usage(stdout)
		return 0
	}
	cmds := commands()
	c, ok := cmds[args[0]]
	if !ok {
		fmt.Fprintf(stderr, "nf: unknown command %q\n\n", args[0])
		usage(stderr)
		return 2
	}
	if err := c.run(args[1:], stdout, stderr); err != nil {
		fmt.Fprintf(stderr, "nf %s: %v\n", args[0], err)
		return 1
	}
	return 0
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "nf — the NovaForge command line")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	cmds := commands()
	names := make([]string, 0, len(cmds))
	for n := range cmds {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(w, "  %-10s %s\n", n, cmds[n].summary)
	}
}

// session loads the stored config and returns a ready client.
func session() (*Client, Config, error) {
	cfg, err := LoadConfig()
	if err != nil {
		return nil, Config{}, err
	}
	if cfg.Server == "" {
		return nil, cfg, fmt.Errorf("not logged in: run `nf login --server <url> --username <u> --password <p>`")
	}
	return NewClient(cfg.Server, cfg.Token), cfg, nil
}

func requireOrg(cfg Config) (string, error) {
	if cfg.Org == "" {
		return "", fmt.Errorf("no organization selected: run `nf org use <name>`")
	}
	return cfg.Org, nil
}

func cmdLogin(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	fs.SetOutput(stderr)
	server := fs.String("server", "", "NovaForge server URL")
	username := fs.String("username", "", "username")
	password := fs.String("password", "", "password")
	totp := fs.String("totp", "", "TOTP code, when two-factor is enabled")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *server == "" || *username == "" || *password == "" {
		return fmt.Errorf("--server, --username and --password are required")
	}
	c := NewClient(*server, "")
	var resp struct {
		SessionToken string `json:"session_token"`
		RequiresTOTP bool   `json:"requires_totp"`
		UserID       string `json:"user_id"`
	}
	body := map[string]string{"username": *username, "password": *password, "totp_code": *totp}
	if err := c.Do("POST", "/api/v1/auth/login", body, &resp); err != nil {
		return err
	}
	if resp.RequiresTOTP && resp.SessionToken == "" {
		return fmt.Errorf("two-factor is enabled: supply --totp")
	}
	cfg, _ := LoadConfig()
	cfg.Server, cfg.Token = *server, resp.SessionToken
	if err := SaveConfig(cfg); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "logged in to %s\n", *server)
	return nil
}

func cmdWhoami(args []string, stdout, stderr io.Writer) error {
	c, _, err := session()
	if err != nil {
		return err
	}
	var out map[string]any
	if err := c.Do("GET", "/api/v1/user", nil, &out); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%v\n", out)
	return nil
}

func cmdOrg(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: nf org <create|list|use> [name]")
	}
	c, cfg, err := session()
	if err != nil && args[0] != "use" {
		return err
	}
	switch args[0] {
	case "create":
		if len(args) < 2 {
			return fmt.Errorf("usage: nf org create <name>")
		}
		var out map[string]any
		if err := c.Do("POST", "/api/v1/orgs", map[string]string{"name": args[1]}, &out); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "created organization %s\n", args[1])
		return nil
	case "list":
		var out struct {
			Orgs []struct {
				Name string `json:"name"`
			} `json:"orgs"`
		}
		if err := c.Do("GET", "/api/v1/orgs", nil, &out); err != nil {
			return err
		}
		for _, o := range out.Orgs {
			fmt.Fprintln(stdout, o.Name)
		}
		return nil
	case "use":
		if len(args) < 2 {
			return fmt.Errorf("usage: nf org use <name>")
		}
		cfg, _ = LoadConfig()
		cfg.Org = args[1]
		if err := SaveConfig(cfg); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "using organization %s\n", args[1])
		return nil
	}
	return fmt.Errorf("unknown org subcommand %q", args[0])
}

func cmdRepo(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: nf repo <create|list|branches|log> [name]")
	}
	c, cfg, err := session()
	if err != nil {
		return err
	}
	org, err := requireOrg(cfg)
	if err != nil {
		return err
	}
	base := "/api/v1/orgs/" + org + "/repos"

	switch args[0] {
	case "create":
		if len(args) < 2 {
			return fmt.Errorf("usage: nf repo create <name>")
		}
		var out map[string]any
		if err := c.Do("POST", base, map[string]string{"name": args[1]}, &out); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "created repository %s/%s\n", org, args[1])
		return nil
	case "list":
		var out struct {
			Repos []struct {
				Name          string `json:"name"`
				DefaultBranch string `json:"default_branch"`
			} `json:"repos"`
		}
		if err := c.Do("GET", base, nil, &out); err != nil {
			return err
		}
		for _, r := range out.Repos {
			fmt.Fprintf(stdout, "%s\t%s\n", r.Name, r.DefaultBranch)
		}
		return nil
	case "branches":
		if len(args) < 2 {
			return fmt.Errorf("usage: nf repo branches <name>")
		}
		var out struct {
			Refs []struct {
				Name string `json:"name"`
				SHA  string `json:"sha"`
			} `json:"refs"`
		}
		if err := c.Do("GET", base+"/"+args[1]+"/branches", nil, &out); err != nil {
			return err
		}
		for _, r := range out.Refs {
			fmt.Fprintf(stdout, "%s\t%s\n", r.Name, r.SHA)
		}
		return nil
	case "log":
		if len(args) < 2 {
			return fmt.Errorf("usage: nf repo log <name> [ref]")
		}
		ref := "main"
		if len(args) > 2 {
			ref = args[2]
		}
		var out struct {
			Commits []struct {
				SHA     string `json:"sha"`
				Message string `json:"message"`
			} `json:"commits"`
		}
		if err := c.Do("GET", base+"/"+args[1]+"/commits/"+ref, nil, &out); err != nil {
			return err
		}
		for _, cm := range out.Commits {
			sha := cm.SHA
			if len(sha) > 8 {
				sha = sha[:8]
			}
			fmt.Fprintf(stdout, "%s %s\n", sha, strings.TrimSpace(cm.Message))
		}
		return nil
	}
	return fmt.Errorf("unknown repo subcommand %q", args[0])
}

func cmdWork(args []string, stdout, stderr io.Writer) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: nf work <list|create|get> <repo> [...]")
	}
	c, cfg, err := session()
	if err != nil {
		return err
	}
	org, err := requireOrg(cfg)
	if err != nil {
		return err
	}
	base := fmt.Sprintf("/api/v1/orgs/%s/repos/%s/work", org, args[1])

	switch args[0] {
	case "list":
		var out struct {
			Items []struct {
				Key   string `json:"key"`
				Goal  string `json:"goal"`
				State string `json:"state"`
			} `json:"items"`
		}
		if err := c.Do("GET", base, nil, &out); err != nil {
			return err
		}
		for _, it := range out.Items {
			fmt.Fprintf(stdout, "%s\t%s\t%s\n", it.Key, it.State, it.Goal)
		}
		return nil
	case "create":
		fs := flag.NewFlagSet("work create", flag.ContinueOnError)
		fs.SetOutput(stderr)
		typ := fs.String("type", "feature", "Work Item type")
		goal := fs.String("goal", "", "what the change must achieve")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		if *goal == "" {
			return fmt.Errorf("--goal is required")
		}
		var out map[string]any
		if err := c.Do("POST", base, map[string]any{"type": *typ, "goal": *goal}, &out); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "created %v\n", out["key"])
		return nil
	case "get":
		if len(args) < 3 {
			return fmt.Errorf("usage: nf work get <repo> <key>")
		}
		var out map[string]any
		if err := c.Do("GET", base+"/"+args[2], nil, &out); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%v\n", out)
		return nil
	case "decompose":
		if len(args) < 3 {
			return fmt.Errorf("usage: nf work decompose <repo> <key>")
		}
		var out map[string]any
		if err := c.Do("POST", base+"/"+args[2]+"/decompose", nil, &out); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "decomposed %s\n", args[2])
		return nil
	case "subtasks":
		if len(args) < 3 {
			return fmt.Errorf("usage: nf work subtasks <repo> <key>")
		}
		var out struct {
			Subtasks []struct {
				Key   string `json:"key"`
				Goal  string `json:"goal"`
				State string `json:"state"`
				Ready bool   `json:"ready"`
			} `json:"subtasks"`
		}
		if err := c.Do("GET", base+"/"+args[2]+"/subtasks", nil, &out); err != nil {
			return err
		}
		for _, st := range out.Subtasks {
			status := "blocked"
			if st.Ready {
				status = "ready"
			}
			fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\n", st.Key, st.State, status, st.Goal)
		}
		return nil
	}
	return fmt.Errorf("unknown work subcommand %q", args[0])
}

func cmdRun(args []string, stdout, stderr io.Writer) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: nf run <list|get|gates|merge> <repo> [number]")
	}
	c, cfg, err := session()
	if err != nil {
		return err
	}
	org, err := requireOrg(cfg)
	if err != nil {
		return err
	}
	base := fmt.Sprintf("/api/v1/orgs/%s/repos/%s/runs", org, args[1])

	switch args[0] {
	case "list":
		var out struct {
			Runs []struct {
				Number int    `json:"number"`
				Title  string `json:"title"`
				State  string `json:"state"`
			} `json:"runs"`
		}
		if err := c.Do("GET", base, nil, &out); err != nil {
			return err
		}
		for _, r := range out.Runs {
			fmt.Fprintf(stdout, "#%d\t%s\t%s\n", r.Number, r.State, r.Title)
		}
		return nil
	case "get", "gates", "merge":
		if len(args) < 3 {
			return fmt.Errorf("usage: nf run %s <repo> <number>", args[0])
		}
		path := base + "/" + args[2]
		method := "GET"
		switch args[0] {
		case "gates":
			path += "/proof"
		case "merge":
			path += "/merge"
			method = "POST"
		}
		var out map[string]any
		if err := c.Do(method, path, nil, &out); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%v\n", out)
		return nil
	}
	return fmt.Errorf("unknown run subcommand %q", args[0])
}

func cmdCI(args []string, stdout, stderr io.Writer) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: nf ci <runs|logs|artifacts> <repo> [job-id]")
	}
	c, cfg, err := session()
	if err != nil {
		return err
	}
	org, err := requireOrg(cfg)
	if err != nil {
		return err
	}
	base := fmt.Sprintf("/api/v1/orgs/%s/repos/%s/ci", org, args[1])

	switch args[0] {
	case "runs":
		var out struct {
			Runs []struct {
				ID        string `json:"id"`
				Status    string `json:"status"`
				CommitSHA string `json:"commit_sha"`
				Ref       string `json:"ref"`
			} `json:"runs"`
		}
		if err := c.Do("GET", base+"/runs", nil, &out); err != nil {
			return err
		}
		for _, r := range out.Runs {
			sha := r.CommitSHA
			if len(sha) > 8 {
				sha = sha[:8]
			}
			fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\n", r.ID, r.Status, sha, r.Ref)
		}
		return nil
	case "logs":
		// With no job id this shows the newest run's first job, which is what
		// somebody checking "did my push build" actually wants.
		path := base + "/logs"
		if len(args) > 2 {
			path = base + "/jobs/" + args[2] + "/logs"
		}
		var out struct {
			Lines []string `json:"lines"`
		}
		if err := c.Do("GET", path, nil, &out); err != nil {
			return err
		}
		for _, l := range out.Lines {
			fmt.Fprintln(stdout, l)
		}
		return nil
	case "artifacts":
		path := base + "/artifacts"
		if len(args) > 2 {
			path = base + "/jobs/" + args[2] + "/artifacts"
		}
		var out struct {
			Artifacts []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
				Size int64  `json:"size_bytes"`
			} `json:"artifacts"`
		}
		if err := c.Do("GET", path, nil, &out); err != nil {
			return err
		}
		for _, a := range out.Artifacts {
			fmt.Fprintf(stdout, "%s\t%s\t%d\n", a.ID, a.Name, a.Size)
		}
		return nil
	}
	return fmt.Errorf("unknown ci subcommand %q", args[0])
}

func cmdDashboard(args []string, stdout, stderr io.Writer) error {
	c, cfg, err := session()
	if err != nil {
		return err
	}
	org, err := requireOrg(cfg)
	if err != nil {
		return err
	}
	var out struct {
		AgentsRunning         int `json:"agents_running"`
		ReadyToAutoMerge      int `json:"ready_to_auto_merge"`
		NeedHumanReview       int `json:"need_human_review"`
		ArchitectureDecisions int `json:"architecture_decisions"`
		GateFailures          int `json:"gate_failures"`
		AgentsBlocked         int `json:"agents_blocked"`
	}
	if err := c.Do("GET", "/api/v1/orgs/"+org+"/dashboard", nil, &out); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%d agents running\n", out.AgentsRunning)
	fmt.Fprintf(stdout, "%d ready to auto-merge\n", out.ReadyToAutoMerge)
	fmt.Fprintf(stdout, "%d need human review\n", out.NeedHumanReview)
	fmt.Fprintf(stdout, "%d architecture decisions\n", out.ArchitectureDecisions)
	fmt.Fprintf(stdout, "%d gate failures\n", out.GateFailures)
	fmt.Fprintf(stdout, "%d agents blocked\n", out.AgentsBlocked)
	return nil
}
