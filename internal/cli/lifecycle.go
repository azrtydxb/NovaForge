package cli

import (
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"strings"
)

// cmdRun drives Engineering Runs from open to merged: the part of the
// lifecycle that had no command, so "with no GUI involved" held only up to
// the point where a change was ready to review.
func cmdRun(args []string, stdout, stderr io.Writer) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: nf run <list|create|get|gates|proof|review|merge> <repo> [number] [flags]")
	}
	c, cfg, err := session()
	if err != nil {
		return err
	}
	org, err := requireOrg(cfg)
	if err != nil {
		return err
	}
	base := fmt.Sprintf("/api/v1/orgs/%s/repos/%s/runs", url.PathEscape(org), url.PathEscape(args[1]))

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

	case "create":
		fs := flag.NewFlagSet("run create", flag.ContinueOnError)
		fs.SetOutput(stderr)
		title := fs.String("title", "", "what the change does")
		source := fs.String("source", "", "the branch carrying the change")
		target := fs.String("target", "", "the branch it merges into; the repository's default if empty")
		item := fs.String("work-item", "", "the Work Item key the change is for")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		if *title == "" || *source == "" {
			return fmt.Errorf("--title and --source are required")
		}
		var out struct {
			Number int    `json:"number"`
			State  string `json:"state"`
		}
		body := map[string]string{"title": *title, "source_ref": *source, "target_ref": *target, "work_item": *item}
		if err := c.Do("POST", base, body, &out); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "#%d\t%s\n", out.Number, out.State)
		return nil
	}

	if len(args) < 3 {
		return fmt.Errorf("usage: nf run %s <repo> <number>", args[0])
	}
	run := base + "/" + url.PathEscape(args[2])
	switch args[0] {
	case "get":
		var out map[string]any
		if err := c.Do("GET", run, nil, &out); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%v\n", out)
		return nil

	case "gates":
		// Inspecting gates evaluates them at the run's current head and
		// prints each verdict. Reading proof alone showed nothing for a run
		// whose gates had never been evaluated, which reads as "no gate
		// objects" rather than "nobody asked".
		var out struct {
			Evaluations []struct {
				Gate   string `json:"gate"`
				Status string `json:"status"`
				Detail string `json:"detail"`
			} `json:"evaluations"`
		}
		if err := c.Do("POST", run+"/gates/evaluate", nil, &out); err != nil {
			return err
		}
		if len(out.Evaluations) == 0 {
			fmt.Fprintln(stdout, "no gates are configured for this run")
		}
		failed := 0
		for _, e := range out.Evaluations {
			fmt.Fprintf(stdout, "%s\t%s\t%s\n", e.Gate, e.Status, firstLine(e.Detail))
			if e.Status != "pass" {
				failed++
			}
		}
		if failed > 0 {
			return fmt.Errorf("%d gate(s) did not pass", failed)
		}
		return nil

	case "proof":
		var out struct {
			Proof []struct {
				Gate       string `json:"gate"`
				Status     string `json:"status"`
				RecordedAt string `json:"recorded_at"`
			} `json:"proof"`
		}
		if err := c.Do("GET", run+"/proof", nil, &out); err != nil {
			return err
		}
		for _, p := range out.Proof {
			fmt.Fprintf(stdout, "%s\t%s\t%s\n", p.Gate, p.Status, p.RecordedAt)
		}
		return nil

	case "review":
		fs := flag.NewFlagSet("run review", flag.ContinueOnError)
		fs.SetOutput(stderr)
		verdict := fs.String("verdict", "", "approve, request_changes or reject")
		summary := fs.String("summary", "", "why")
		sourceSHA := fs.String("source-sha", "", "the source revision reviewed (default: the run's current source)")
		if err := fs.Parse(args[3:]); err != nil {
			return err
		}
		if *verdict == "" {
			return fmt.Errorf("--verdict is required")
		}
		// A verdict names the revision it was formed against. The server refuses
		// a review that does not, so that an approval cannot silently carry over
		// to work pushed after it was read. When the reviewer does not say which
		// revision, resolve the run's current source and report it: if the source
		// moves between this read and the POST, the server still rejects.
		reviewed := *sourceSHA
		if reviewed == "" {
			var current struct {
				SHA       string `json:"current_source_sha"`
				Available bool   `json:"current_source_available"`
			}
			if err := c.Do("GET", run+"/reviews", nil, &current); err != nil {
				return err
			}
			if !current.Available || current.SHA == "" {
				return fmt.Errorf("the run's current source revision is unavailable; pass --source-sha")
			}
			reviewed = current.SHA
		}
		body := map[string]string{"verdict": *verdict, "summary": *summary, "expected_source_sha": reviewed}
		if err := c.Do("POST", run+"/reviews", body, nil); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "recorded %s on #%s at %s\n", *verdict, args[2], reviewed)
		return nil

	case "merge":
		fs := flag.NewFlagSet("run merge", flag.ContinueOnError)
		fs.SetOutput(stderr)
		method := fs.String("method", "merge", "merge, squash or ff-only")
		if err := fs.Parse(args[3:]); err != nil {
			return err
		}
		var out struct {
			MergeSHA string `json:"merge_sha"`
		}
		if err := c.Do("POST", run+"/merge", map[string]string{"method": *method}, &out); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "merged #%s at %s\n", args[2], out.MergeSHA)
		return nil
	}
	return fmt.Errorf("unknown run subcommand %q", args[0])
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// repoClone clones a repository with an unmodified git client over HTTPS,
// carrying the stored credential. The git host is not the API's host — the
// edge does not serve the git transport — so it is given with --git or
// remembered from a previous --git. With --print the URL is printed rather
// than cloned, for a caller who wants to run git themself; it contains the
// credential, so it is printed only when asked.
func repoClone(c *Client, cfg Config, org string, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("repo clone", flag.ContinueOnError)
	fs.SetOutput(stderr)
	gitBase := fs.String("git", cfg.GitURL, "the git-platform HTTP(S) base URL, e.g. https://git.example.com")
	printOnly := fs.Bool("print", false, "print the credentialed clone URL instead of cloning")
	if len(args) < 1 {
		return fmt.Errorf("usage: nf repo clone <name> [dir] [--git <url>] [--print]")
	}
	name := args[0]
	rest := args[1:]
	dir := ""
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		dir, rest = rest[0], rest[1:]
	}
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if *gitBase == "" {
		return fmt.Errorf("the git host is not known: pass --git <url> once and it is remembered")
	}
	u, err := url.Parse(strings.TrimRight(*gitBase, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("--git must be an http or https URL, not %q", *gitBase)
	}
	if *gitBase != cfg.GitURL {
		cfg.GitURL = *gitBase
		if err := SaveConfig(cfg); err != nil {
			return err
		}
	}
	u.User = url.UserPassword("nf", c.token)
	u.Path = u.Path + "/" + org + "/" + name + ".git"
	if *printOnly {
		fmt.Fprintln(stdout, u.String())
		return nil
	}
	if dir == "" {
		dir = name
	}
	cmd := exec.Command("git", "clone", u.String(), dir)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git clone: %w", err)
	}
	fmt.Fprintf(stdout, "cloned %s/%s into %s\n", org, name, dir)
	return nil
}

// orgAddMember adds a person to the selected organization (owner or admin).
func orgAddMember(c *Client, cfg Config, args []string, stdout, stderr io.Writer) error {
	org, err := requireOrg(cfg)
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("org add-member", flag.ContinueOnError)
	fs.SetOutput(stderr)
	role := fs.String("role", "member", "owner, admin or member")
	if len(args) < 1 {
		return fmt.Errorf("usage: nf org add-member <username> [--role member]")
	}
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if err := c.Do("POST", "/api/v1/orgs/"+url.PathEscape(org)+"/members",
		map[string]string{"username": args[0], "role": *role}, nil); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "added %s to %s as %s\n", args[0], org, *role)
	return nil
}

// stringList is a repeatable flag.
type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }
