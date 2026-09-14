// Package analysis runs the static and dynamic checks a gate or a maintenance
// scanner needs, using tools that genuinely produce machine-readable output,
// and parses that output.
//
// It replaces an integration that never worked. The gates and scanners were
// written against a JSON contract with the procoder CLI — "procoder security"
// printing {"secrets":[...],"sast":[...]}, "procoder test" printing
// {"coverage":...} — that procoder does not provide: it prints text for people,
// and the binary was not in any image besides. On the cluster every such gate
// reported "error" and three of the eight maintenance scanners could never
// produce a finding. Each check here names the tool it runs and parses the
// format that tool actually emits.
package analysis

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Exec runs name with args in dir and returns its stdout and exit code. err
// is reserved for failing to run the tool at all — not installed, or killed;
// a tool that ran and exited non-zero reports that through exit, because for
// most of these tools a non-zero exit is how they say "I found something".
type Exec func(ctx context.Context, dir, name string, args ...string) (stdout []byte, exit int, err error)

// ErrToolMissing reports a tool that is not installed. A check that cannot run
// is an error, never a pass: a secret scan that did not run has not found that
// there are no secrets.
var ErrToolMissing = errors.New("analysis tool is not installed")

// DefaultExec runs tools from PATH.
func DefaultExec(ctx context.Context, dir, name string, args ...string) ([]byte, int, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return nil, 0, fmt.Errorf("%w: %s", ErrToolMissing, name)
	}
	cmd := exec.CommandContext(ctx, path, args...) // #nosec G204 -- tool names are constants in this package
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return stdout.Bytes(), 0, nil
	case errors.As(err, &exitErr) && ctx.Err() == nil:
		// Carry stderr along so a failing tool's own explanation survives
		// into whatever reports the result.
		out := stdout.Bytes()
		if len(out) == 0 {
			out = stderr.Bytes()
		}
		return out, exitErr.ExitCode(), nil
	default:
		return nil, 0, fmt.Errorf("run %s: %w: %s", name, err, bytes.TrimSpace(stderr.Bytes()))
	}
}

// IsGoModule reports whether dir is the root of a Go module. The Go-specific
// checks (tests, vet, doc comments, outdated modules) do not apply elsewhere,
// and a gate says "skipped" rather than inventing a result for a repository
// they cannot read.
func IsGoModule(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, "go.mod"))
	return err == nil && !info.IsDir()
}
