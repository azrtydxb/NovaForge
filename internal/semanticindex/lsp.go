package semanticindex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const MaxQueries = 2000

// Query is a source position in UTF-16 code units, as negotiated with the LSP
// server. The caller supplies candidates; this adapter does not pretend these
// selected definition requests enumerate all references in a repository.
type Query struct {
	Path, Language string
	Position       Position
}

type Location struct {
	Path, ContentHash string
	Range             Range
}

type Resolution struct {
	Query           Query
	Targets         []Location
	ExternalTargets int
}

type DefinitionResult struct {
	OrgID, RepoID                                                uuid.UUID
	Revision, SnapshotDigest, ExecutionDigest, Tool, ToolVersion string
	Resolutions                                                  []Resolution
	AbsenceSafe                                                  bool
}

// ResolveDefinitions uses a fresh stdio-framed LSP session, initializes UTF-16,
// opens the query documents with exact snapshot bytes, and resolves definitions.
// transport must connect to an operator-selected server in an isolated checkout
// containing this snapshot. Close MUST unblock reads/writes; it is called once
// on every return (including invalid input), or on cancellation. The controller
// must terminate/reap the remote process tree separately. No commands are launched
// by this package. A server cannot ask this client to edit or execute anything.
func ResolveDefinitions(ctx context.Context, transport io.ReadWriteCloser, s Snapshot, e RunEvidence, queries []Query) (DefinitionResult, error) {
	if transport == nil {
		return DefinitionResult{}, fmt.Errorf("LSP transport is required")
	}
	var once sync.Once
	closeTransport := func() { once.Do(func() { _ = transport.Close() }) }
	defer closeTransport()
	if err := s.validateEvidence(e); err != nil {
		return DefinitionResult{}, err
	}
	if len(queries) == 0 || len(queries) > MaxQueries {
		return DefinitionResult{}, fmt.Errorf("LSP query count outside 1..%d", MaxQueries)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	sources := sourceCache{}
	opened := map[string]string{}
	for _, q := range queries {
		source, err := sources.get(ctx, s, q.Path)
		if err != nil {
			return DefinitionResult{}, err
		}
		if q.Language == "" || !validPosition(source.lines, q.Position, 2) {
			return DefinitionResult{}, fmt.Errorf("invalid LSP query for %q", q.Path)
		}
		if lang, exists := opened[q.Path]; exists && lang != q.Language {
			return DefinitionResult{}, fmt.Errorf("conflicting LSP language IDs for %q", q.Path)
		}
		opened[q.Path] = q.Language
	}
	stop := context.AfterFunc(ctx, closeTransport)
	defer stop()
	conn := &lspConnection{ctx: ctx, reader: bufio.NewReader(transport), writer: transport}
	initialized, err := conn.call("initialize", map[string]any{
		"processId": nil, "rootUri": s.RootURI,
		"capabilities": map[string]any{"general": map[string]any{"positionEncodings": []string{"utf-16"}}},
	})
	if err != nil {
		return DefinitionResult{}, fmt.Errorf("LSP initialize: %w", err)
	}
	var initResult struct {
		Capabilities struct {
			PositionEncoding   string          `json:"positionEncoding"`
			DefinitionProvider json.RawMessage `json:"definitionProvider"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(initialized, &initResult); err != nil {
		return DefinitionResult{}, fmt.Errorf("invalid LSP initialize response: %w", err)
	}
	if enc := initResult.Capabilities.PositionEncoding; enc != "" && enc != "utf-16" {
		return DefinitionResult{}, fmt.Errorf("LSP server did not negotiate UTF-16")
	}
	provider := strings.TrimSpace(string(initResult.Capabilities.DefinitionProvider))
	if provider != "true" && !strings.HasPrefix(provider, "{") {
		return DefinitionResult{}, fmt.Errorf("LSP server does not support definition requests")
	}
	if err := conn.notify("initialized", map[string]any{}); err != nil {
		return DefinitionResult{}, err
	}
	paths := make([]string, 0, len(opened))
	for p := range opened {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		if err := conn.notify("textDocument/didOpen", map[string]any{"textDocument": map[string]any{
			"uri": sourceURI(s, p), "languageId": opened[p], "version": 1, "text": string(s.Files[p]),
		}}); err != nil {
			return DefinitionResult{}, err
		}
	}
	out := DefinitionResult{OrgID: s.OrgID, RepoID: s.RepoID, Revision: s.Revision, SnapshotDigest: e.SnapshotDigest, ExecutionDigest: e.ExecutionDigest, Tool: e.Tool, ToolVersion: e.ToolVersion}
	for _, q := range queries {
		if err := ctx.Err(); err != nil {
			return DefinitionResult{}, err
		}
		data, err := conn.call("textDocument/definition", map[string]any{
			"textDocument": map[string]string{"uri": sourceURI(s, q.Path)}, "position": q.Position,
		})
		if err != nil {
			return DefinitionResult{}, fmt.Errorf("LSP definition %q: %w", q.Path, err)
		}
		resolution, err := decodeLocations(ctx, s, q, data, sources)
		if err != nil {
			return DefinitionResult{}, err
		}
		out.Resolutions = append(out.Resolutions, resolution)
	}
	if _, err := conn.call("shutdown", nil); err != nil {
		return DefinitionResult{}, fmt.Errorf("LSP shutdown: %w", err)
	}
	if err := conn.notify("exit", nil); err != nil {
		return DefinitionResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return DefinitionResult{}, err
	}
	return out, ctx.Err()
}

func sourceURI(s Snapshot, p string) string {
	u, _ := rootURL(s.RootURI)
	u.Path = strings.TrimSuffix(u.Path, "/") + "/" + p
	u.RawPath = ""
	return u.String()
}

type sourceMetadata struct {
	lines []string
	hash  string
}
type sourceCache map[string]sourceMetadata

func (c sourceCache) get(ctx context.Context, s Snapshot, p string) (sourceMetadata, error) {
	if err := ctx.Err(); err != nil {
		return sourceMetadata{}, err
	}
	if source, ok := c[p]; ok {
		return source, nil
	}
	b, ok := s.Files[p]
	if !validPath(p) || !ok || !utf8.Valid(b) {
		return sourceMetadata{}, fmt.Errorf("LSP source %q is absent or invalid", p)
	}
	source := sourceMetadata{lines: strings.Split(string(b), "\n"), hash: digest(b)}
	if err := ctx.Err(); err != nil {
		return sourceMetadata{}, err
	}
	c[p] = source
	return source, nil
}

func decodeLocations(ctx context.Context, s Snapshot, q Query, data []byte, sources sourceCache) (Resolution, error) {
	out := Resolution{Query: q}
	if err := validateJSONStrings(ctx, data); err != nil {
		return out, err
	}
	data = bytes.TrimSpace(data)
	var items []json.RawMessage
	if string(data) == "null" {
		return out, ctx.Err()
	}
	if len(data) > 0 && data[0] == '[' {
		var err error
		items, err = decodeJSONArray(ctx, data, MaxQueries)
		if err != nil {
			return out, err
		}
	} else {
		items = []json.RawMessage{data}
	}
	if len(items) > MaxQueries {
		return out, fmt.Errorf("LSP definition target limit exceeded")
	}
	root, _ := rootURL(s.RootURI)
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		var loc struct {
			URI                  string `json:"uri"`
			Range                *Range `json:"range"`
			TargetURI            string `json:"targetUri"`
			TargetSelectionRange *Range `json:"targetSelectionRange"`
		}
		if err := json.Unmarshal(item, &loc); err != nil {
			return out, err
		}
		if loc.TargetURI != "" {
			if loc.URI != "" {
				return out, fmt.Errorf("ambiguous LSP location")
			}
			loc.URI, loc.Range = loc.TargetURI, loc.TargetSelectionRange
		}
		u, err := url.Parse(loc.URI)
		if err != nil || u.Scheme == "" || loc.URI == "" || loc.Range == nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.User != nil {
			return out, fmt.Errorf("invalid LSP location")
		}
		prefix := strings.TrimSuffix(root.Path, "/") + "/"
		if u.Scheme != "file" || u.Host != "" || !strings.HasPrefix(u.Path, prefix) {
			out.ExternalTargets++
			continue
		}
		p := strings.TrimPrefix(u.Path, prefix)
		source, err := sources.get(ctx, s, p)
		if err != nil {
			return out, err
		}
		if !validRange(source.lines, *loc.Range, 2) {
			return out, fmt.Errorf("LSP target %q is absent from snapshot or has invalid range", p)
		}
		out.Targets = append(out.Targets, Location{Path: p, ContentHash: source.hash, Range: *loc.Range})
	}
	return out, ctx.Err()
}
