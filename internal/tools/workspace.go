package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type workspaceWriteFileArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type workspaceWriteFileResult struct {
	Path  string `json:"path"`
	Bytes int    `json:"bytes"`
}

func registerWorkspaceTools(r *Registry) {
	r.Register("workspace.write_file", workspaceWriteFileHandler)
}

// resolveWorkspacePath resolves path against root using filepath.Clean and
// refuses any result escaping root, whether through a literal ".." segment
// or an absolute path that simply names somewhere else on disk.
func resolveWorkspacePath(root, path string) (string, error) {
	cleanRoot := filepath.Clean(root)
	target := filepath.Clean(filepath.Join(cleanRoot, path))
	if target != cleanRoot && !strings.HasPrefix(target, cleanRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("workspace.write_file: path %q resolves outside workspace root", path)
	}
	return target, nil
}

func workspaceWriteFileHandler(ctx context.Context, rt Runtime, argsJSON []byte) ([]byte, error) {
	var args workspaceWriteFileArgs
	if err := unmarshalArgs("workspace.write_file", argsJSON, &args); err != nil {
		return nil, err
	}
	if rt.WorkspaceRoot == "" {
		return nil, fmt.Errorf("workspace.write_file: no workspace root configured")
	}
	target, err := resolveWorkspacePath(rt.WorkspaceRoot, args.Path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return nil, fmt.Errorf("workspace.write_file: create directory: %w", err)
	}
	if err := os.WriteFile(target, []byte(args.Content), 0o644); err != nil {
		return nil, fmt.Errorf("workspace.write_file: write: %w", err)
	}
	return json.Marshal(workspaceWriteFileResult{Path: args.Path, Bytes: len(args.Content)})
}
