package tools_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/novaforge/novaforge/internal/tools"
)

// S-8 wants a call's arguments in the audit log, and nothing wants a secret
// there. The contract that reconciles the two is per argument: identifiers are
// kept, content is kept as a digest. This pins both halves — a change that
// disclosed content, or that stopped disclosing identifiers, fails here.
func TestAuditArgsDisclosesIdentifiersAndDigestsContent(t *testing.T) {
	secret := strings.Repeat("x", 40) + "AWS_SECRET_ACCESS_KEY"
	for _, c := range []struct {
		name     string
		tool     string
		builtin  bool
		args     string
		contains []string
		absent   []string
	}{
		{
			name:     "a built-in tool's identifiers are kept",
			tool:     "repo.read_file",
			builtin:  true,
			args:     `{"repo":"ledger","ref":"main","path":"README.md"}`,
			contains: []string{`"ledger"`, `"main"`, `"README.md"`},
		},
		{
			name:     "a written file's contents are digested, its path is not",
			tool:     "workspace.write_file",
			builtin:  true,
			args:     `{"path":"config.yaml","content":"token: ` + secret + `"}`,
			contains: []string{`"config.yaml"`, `"sha256"`, `"bytes"`},
			absent:   []string{secret},
		},
		{
			name:     "a comment's body is digested, the work item it is on is not",
			tool:     "work.comment",
			builtin:  true,
			args:     `{"work_item_id":"NF-1","body":"` + secret + `"}`,
			contains: []string{`"NF-1"`, `"sha256"`},
			absent:   []string{secret},
		},
		{
			name:    "an external server's arguments are all digested",
			tool:    "mcp.jira.lookup",
			builtin: false,
			args:    `{"issue":"` + secret + `"}`,
			// The key names which argument was passed; the value never appears.
			contains: []string{`"issue"`, `"sha256"`},
			absent:   []string{secret},
		},
		{
			name:     "an identifier past the bound is digested rather than kept",
			tool:     "repo.search",
			builtin:  true,
			args:     `{"repo":"r","query":"` + strings.Repeat("q", 600) + `"}`,
			contains: []string{`"r"`, `"sha256"`},
			absent:   []string{strings.Repeat("q", 600)},
		},
		{
			name:     "arguments that are not an object are recorded as their shape",
			tool:     "repo.search",
			builtin:  true,
			args:     `"` + secret + `"`,
			contains: []string{"argument_bytes", "valid_json"},
			absent:   []string{secret},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := string(tools.AuditArgs(c.tool, c.builtin, []byte(c.args)))
			if !json.Valid([]byte(got)) {
				t.Fatalf("audited arguments are not valid JSON: %s", got)
			}
			for _, want := range c.contains {
				if !strings.Contains(got, want) {
					t.Errorf("audited arguments %s do not contain %s", got, want)
				}
			}
			for _, never := range c.absent {
				if strings.Contains(got, never) {
					t.Errorf("audited arguments disclosed a value they must not: %s", got)
				}
			}
		})
	}
}
