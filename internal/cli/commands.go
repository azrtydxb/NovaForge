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
		"login":  {"login", "Authenticate and store a session token", cmdLogin},
		"org":    {"org", "Manage organizations", cmdOrg},
		"repo":   {"repo", "Manage repositories", cmdRepo},
		"work":   {"work", "Manage Work Items", cmdWork},
		"run":    {"run", "Inspect and merge Engineering Runs", cmdRun},
		"whoami": {"whoami", "Show the authenticated user", cmdWhoami},
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
		fmt.Fprintf(w, "  %-8s %s\n", n, cmds[n].summary)
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
