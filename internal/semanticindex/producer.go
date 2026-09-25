package semanticindex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// ArtifactSet is untrusted subprocess output. Only the controller creates run
// evidence and imports these completed artifacts against its original snapshot.
type ArtifactSet map[string]json.RawMessage

// RunProducer runs ONLY in the isolated, credential-free producer image. The
// service entrypoint does not call it while processing a request in its own pod.
func RunProducer(input io.Reader, output io.Writer) error {
	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Minute)
	defer cancel()
	var s Snapshot
	data, err := io.ReadAll(io.LimitReader(input, 2*MaxSourceBytes+1))
	if err != nil {
		return err
	}
	if len(data) > 2*MaxSourceBytes {
		return fmt.Errorf("producer input exceeds limit")
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	if s.RootURI != ProducerRoot {
		return fmt.Errorf("producer root mismatch")
	}
	if _, err := s.Digest(); err != nil {
		return err
	}
	for _, dir := range []string{"/work/home", "/work/tmp", "/work/cache", "/work/modules"} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
	}
	artifacts, err := produceArtifacts(ctx, s, "/work/source", producerEnvironment)
	if err != nil {
		return err
	}
	b, err := json.Marshal(artifacts)
	if err != nil {
		return err
	}
	if len(b) > MaxArtifactBytes {
		return fmt.Errorf("combined producer artifact limit exceeded")
	}
	_, err = output.Write(b)
	return err
}

func producerLanguages(s Snapshot) []string {
	langs := map[string]bool{}
	for p := range s.Files {
		switch filepath.Ext(p) {
		case ".go":
			langs["go"] = true
		case ".ts", ".tsx", ".js", ".jsx":
			langs["typescript"] = true
		case ".py":
			langs["python"] = true
		}
	}
	var out []string
	for l := range langs {
		out = append(out, l)
	}
	sort.Strings(out)
	return out
}

func produceArtifacts(ctx context.Context, s Snapshot, dir string, env []string) (ArtifactSet, error) {
	if err := os.Mkdir(dir, 0700); err != nil {
		return nil, err
	}
	for p, b := range s.Files {
		// Git metadata must never turn source transfer into hooks/config execution;
		// tool artifact paths are reserved so a repository cannot supply stale output.
		if !validPath(p) || strings.Contains("/"+p+"/", "/.git/") || p == "index.scip" {
			return nil, fmt.Errorf("reserved producer path %q", p)
		}
		dest := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(dest, b, 0600); err != nil {
			return nil, err
		}
	}
	out := ArtifactSet{}
	for _, lang := range producerLanguages(s) {
		_ = os.Remove(filepath.Join(dir, "index.scip"))
		command := append([]string(nil), producerCommands[lang]...)
		if _, err := runProducerCommand(ctx, dir, env, command); err != nil {
			return nil, err
		}
		artifact := filepath.Join(dir, "index.scip")
		info, err := os.Lstat(artifact)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() || info.Size() > MaxArtifactBytes {
			return nil, fmt.Errorf("SCIP artifact is not a bounded regular file")
		}
		raw, err := runProducerCommand(ctx, dir, env, []string{"scip", "print", "--json", artifact})
		if err != nil {
			return nil, err
		}
		out[lang] = json.RawMessage(raw)
	}
	return out, nil
}

type boundedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, fmt.Errorf("semantic output limit exceeded")
	}
	return b.Buffer.Write(p)
}
func runProducerCommand(ctx context.Context, dir string, env, argv []string) ([]byte, error) {
	// argv is never repository input: it is either an entry of the fixed
	// producerCommands table, selected by detected language, or a literal
	// argv whose only variable element is a path this package built. The
	// repository supplies file contents, which land under dir, not the command.
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) // nosemgrep: dangerous-exec-command
	cmd.Dir = dir
	cmd.Env = env
	out := &boundedBuffer{limit: MaxArtifactBytes}
	stderr := &boundedBuffer{limit: 64 << 10}
	cmd.Stdout = out
	cmd.Stderr = stderr
	// A child keeping inherited pipes open must not outlive the producer deadline.
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("semantic tool %s failed: %w: %s", argv[0], err, stderr.String())
	}
	return out.Bytes(), nil
}

// ImportArtifacts is the controller boundary. Missing languages and unexpected
// tools are errors, never a successful empty result or heuristic fallback.
func ImportArtifacts(ctx context.Context, s Snapshot, data []byte) ([]Result, error) {
	if len(data) > MaxArtifactBytes {
		return nil, fmt.Errorf("combined artifact limit exceeded")
	}
	var artifacts ArtifactSet
	if err := json.Unmarshal(data, &artifacts); err != nil {
		return nil, err
	}
	langs := producerLanguages(s)
	if len(artifacts) != len(langs) {
		return nil, fmt.Errorf("producer language manifest mismatch")
	}
	d, err := s.Digest()
	if err != nil {
		return nil, err
	}
	var out []Result
	for _, lang := range langs {
		raw, ok := artifacts[lang]
		if !ok {
			return nil, fmt.Errorf("missing %s producer artifact", lang)
		}
		tool := producerTools[lang]
		e := RunEvidence{Revision: s.Revision, SnapshotDigest: d, ExecutionDigest: s.ExecutionDigest, Tool: tool.Name, ToolVersion: tool.Version}
		result, err := ImportSCIP(ctx, s, e, bytes.NewReader(raw))
		if err != nil {
			return nil, fmt.Errorf("%s artifact: %w", lang, err)
		}
		out = append(out, result)
	}
	return out, nil
}

// RunLSP replaces the credential-free helper with a fixed gopls server. It does
// not run in the controller process: Kubernetes exec invokes this image mode.
func RunLSP() error {
	if err := os.Chdir("/work/source"); err != nil {
		return err
	}
	return syscall.Exec("/usr/local/bin/gopls", []string{"gopls", "serve"}, producerEnvironment)
}
