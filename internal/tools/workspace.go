package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"
)

// Workspace is the run's isolated workspace: a pod in the run's own
// namespace, holding a copy of the repository, with no network. Files are
// staged in it and commands run in it.
//
// Staging used to write into agent-runtime's own filesystem, where nothing
// could run against the files and nothing read them back: git.commit took its
// content from the model's arguments, so staged files went nowhere.
type Workspace interface {
	WriteFile(ctx context.Context, path string, content []byte) error
	ReadFile(ctx context.Context, path string) ([]byte, error)
	// Run executes command with sh in the workspace root, returning its
	// combined output and exit code. A non-zero exit is a result, not an error.
	Run(ctx context.Context, command string) (output string, exitCode int, err error)
}

// stagedFiles tracks the paths workspace.write_file staged in this run, which
// git.commit commits when it is given no files of its own.
type stagedFiles struct {
	mu    sync.Mutex
	paths map[string]struct{}
}

func (s *stagedFiles) add(p string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.paths[p] = struct{}{}
}

func (s *stagedFiles) list() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.paths))
	for p := range s.paths {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func (s *stagedFiles) clear(committed []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range committed {
		delete(s.paths, p)
	}
}

type workspaceWriteFileArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type workspaceWriteFileResult struct {
	Path  string `json:"path"`
	Bytes int    `json:"bytes"`
}

type workspaceReadFileArgs struct {
	Path string `json:"path"`
}

type workspaceRunArgs struct {
	Command string `json:"command"`
}

type workspaceRunResult struct {
	ExitCode int    `json:"exit_code"`
	Output   string `json:"output"`
}

// maxRunOutput bounds what a command's output returns to the model: a test
// suite's full log would otherwise consume the run's whole token budget.
const maxRunOutput = 16000

func registerWorkspaceTools(r *Registry) {
	r.Register("workspace.write_file", workspaceWriteFileHandler)
	r.Register("workspace.read_file", workspaceReadFileHandler)
	r.Register("workspace.run", workspaceRunHandler)
}

// cleanWorkspacePath turns p into a clean path relative to the workspace root,
// refusing anything that would resolve outside it: a ".." segment climbing
// past the root, or an absolute path naming somewhere else.
func cleanWorkspacePath(tool, p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("%s: path is required", tool)
	}
	cleaned := path.Clean("/" + p)
	if strings.HasPrefix(p, "/") || strings.Contains("/"+p+"/", "/../") || cleaned == "/" {
		return "", fmt.Errorf("%s: path %q resolves outside workspace root", tool, p)
	}
	return strings.TrimPrefix(cleaned, "/"), nil
}

func workspaceWriteFileHandler(ctx context.Context, rt Runtime, argsJSON []byte) ([]byte, error) {
	var args workspaceWriteFileArgs
	if err := unmarshalArgs("workspace.write_file", argsJSON, &args); err != nil {
		return nil, err
	}
	p, err := cleanWorkspacePath("workspace.write_file", args.Path)
	if err != nil {
		return nil, err
	}
	if rt.Workspace == nil {
		return nil, fmt.Errorf("workspace.write_file: this run has no workspace")
	}
	if err := rt.Workspace.WriteFile(ctx, p, []byte(args.Content)); err != nil {
		return nil, fmt.Errorf("workspace.write_file: %w", err)
	}
	rt.staged.add(p)
	return json.Marshal(workspaceWriteFileResult{Path: p, Bytes: len(args.Content)})
}

func workspaceReadFileHandler(ctx context.Context, rt Runtime, argsJSON []byte) ([]byte, error) {
	var args workspaceReadFileArgs
	if err := unmarshalArgs("workspace.read_file", argsJSON, &args); err != nil {
		return nil, err
	}
	p, err := cleanWorkspacePath("workspace.read_file", args.Path)
	if err != nil {
		return nil, err
	}
	if rt.Workspace == nil {
		return nil, fmt.Errorf("workspace.read_file: this run has no workspace")
	}
	content, err := rt.Workspace.ReadFile(ctx, p)
	if err != nil {
		return nil, fmt.Errorf("workspace.read_file: %w", err)
	}
	return json.Marshal(map[string]string{"path": p, "content": string(content)})
}

func workspaceRunHandler(ctx context.Context, rt Runtime, argsJSON []byte) ([]byte, error) {
	var args workspaceRunArgs
	if err := unmarshalArgs("workspace.run", argsJSON, &args); err != nil {
		return nil, err
	}
	if strings.TrimSpace(args.Command) == "" {
		return nil, fmt.Errorf("workspace.run: command is required")
	}
	if rt.Workspace == nil {
		return nil, fmt.Errorf("workspace.run: this run has no workspace")
	}
	out, code, err := rt.Workspace.Run(ctx, args.Command)
	if err != nil {
		return nil, fmt.Errorf("workspace.run: %w", err)
	}
	if len(out) > maxRunOutput {
		// The end of the output is kept: that is where a failing test or a
		// compiler error reports itself.
		out = fmt.Sprintf("… (%d bytes omitted)\n", len(out)-maxRunOutput) + out[len(out)-maxRunOutput:]
	}
	return json.Marshal(workspaceRunResult{ExitCode: code, Output: out})
}
