package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
)

func repoMigration(c *Client, base string, args []string, stdout, stderr io.Writer) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: nf repo <fork|import|mirror> <repository> [flags]")
	}
	endpoint := base + "/" + url.PathEscape(args[1])
	switch args[0] {
	case "fork":
		fs := flag.NewFlagSet("repo fork", flag.ContinueOnError)
		fs.SetOutput(stderr)
		name := fs.String("name", "", "new fork name")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		if *name == "" {
			return fmt.Errorf("--name is required")
		}
		var out map[string]any
		if err := c.Do("POST", endpoint+"/forks", map[string]string{"name": *name}, &out); err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(out)
	case "import":
		fs := flag.NewFlagSet("repo import", flag.ContinueOnError)
		fs.SetOutput(stderr)
		remote := fs.String("remote", "", "upstream HTTP(S) URL without credentials")
		mirror := fs.Bool("mirror", false, "continue following upstream and refuse local pushes")
		interval := fs.Int("interval", 3600, "mirror refresh interval in seconds")
		credentialFile := fs.String("credential-file", "", "read upstream token from file; - reads stdin")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		if *remote == "" {
			return fmt.Errorf("--remote is required")
		}
		credential := ""
		if *credentialFile != "" {
			var r io.Reader = os.Stdin
			if *credentialFile != "-" {
				f, err := os.Open(*credentialFile)
				if err != nil {
					return err
				}
				defer f.Close()
				r = f
			}
			b, err := io.ReadAll(io.LimitReader(r, 65537))
			if err != nil {
				return err
			}
			if len(b) > 65536 {
				return fmt.Errorf("credential exceeds 64KiB")
			}
			credential = strings.TrimSpace(string(b))
		}
		var out map[string]any
		body := map[string]any{"name": args[1], "remote": *remote, "mirror": *mirror, "interval_seconds": *interval, "credential": credential}
		if err := c.Do("POST", base+"/import", body, &out); err != nil {
			return err
		}
		fmt.Fprintln(stderr, "Imported Git refs/history. LFS payloads, releases, users, issues and CI metadata require separate migration.")
		return json.NewEncoder(stdout).Encode(out)
	case "mirror":
		action := "status"
		if len(args) > 2 {
			action = args[2]
		}
		method := "GET"
		path := endpoint + "/mirror"
		switch action {
		case "status":
		case "refresh":
			method = "POST"
			path += "/refresh"
		case "stop":
			method = "DELETE"
		default:
			return fmt.Errorf("usage: nf repo mirror <repository> [status|refresh|stop]")
		}
		var out map[string]any
		if err := c.Do(method, path, nil, &out); err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(out)
	}
	return fmt.Errorf("unknown repository migration command")
}
