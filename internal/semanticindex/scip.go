package semanticindex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Position and Range are zero-based, with an exclusive end. SCIP columns retain
// the producer's encoding, exposed by Document.PositionEncoding; they are not
// silently interpreted as byte offsets. LSP adapters use UTF-16 exclusively.
type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

type Occurrence struct {
	SymbolID, Symbol string
	Range            Range
	Definition       bool
	Roles            int
}
type Document struct {
	Path, ContentHash, Language, Status string
	PositionEncoding                    int
	Occurrences                         []Occurrence
}

// Result deliberately separates observed occurrences from absence evidence.
// Even a successful static index cannot prove no dynamic/external callers exist.
// MissingPaths is relative to the supplied manifest (including config files),
// not a claim that the tool supports every extension in that manifest.
type Result struct {
	OrgID, RepoID                          uuid.UUID
	Revision, SnapshotDigest, ArtifactHash string
	Tool, ToolVersion, ExecutionDigest     string
	Documents                              []Document
	MissingPaths                           []string
	AbsenceSafe                            bool
}

type scipIndex struct {
	Metadata struct {
		ProjectRoot string `json:"project_root"`
		ToolInfo    struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"tool_info"`
	} `json:"metadata"`
	Documents []scipDocument `json:"documents"`
}

type scipDocument struct {
	Path             string           `json:"relative_path"`
	Language         string           `json:"language"`
	Text             *string          `json:"text"`
	PositionEncoding int              `json:"position_encoding"`
	Occurrences      []scipOccurrence `json:"occurrences"`
}
type scipOccurrence struct {
	Range      []int           `json:"range"`
	TypedRange *scipTypedRange `json:"TypedRange"`
	Symbol     string          `json:"symbol"`
	Roles      int             `json:"symbol_roles"`
	// Diagnostic contents are not semantic evidence. Keep their bounded wire
	// representation instead of expanding an untrusted diagnostic collection.
	Diagnostics json.RawMessage `json:"diagnostics"`
}

// Decode collections separately: a byte limit alone cannot bound the expansion
// of tiny JSON elements into much larger Go structs. Raw arrays are counted
// before any typed document/occurrence slices are allocated.
func decodeSCIP(ctx context.Context, b []byte) (scipIndex, error) {
	var index scipIndex
	if err := validateJSONStrings(ctx, b); err != nil {
		return index, err
	}
	var envelope struct {
		Metadata  json.RawMessage `json:"metadata"`
		Documents json.RawMessage `json:"documents"`
	}
	if err := json.Unmarshal(b, &envelope); err != nil {
		return index, err
	}
	docs, err := decodeJSONArray(ctx, envelope.Documents, MaxFiles)
	if err != nil {
		return index, err
	}
	count := 0
	for _, raw := range docs {
		if err := ctx.Err(); err != nil {
			return index, err
		}
		var wire struct {
			Path             string          `json:"relative_path"`
			Language         string          `json:"language"`
			Text             *string         `json:"text"`
			PositionEncoding int             `json:"position_encoding"`
			Occurrences      json.RawMessage `json:"occurrences"`
		}
		if err := json.Unmarshal(raw, &wire); err != nil {
			return index, err
		}
		occurrences, err := decodeJSONArray(ctx, wire.Occurrences, MaxOccurrences-count)
		if err != nil {
			return index, err
		}
		count += len(occurrences)
		d := scipDocument{Path: wire.Path, Language: wire.Language, Text: wire.Text, PositionEncoding: wire.PositionEncoding}
		for _, raw := range occurrences {
			if err := ctx.Err(); err != nil {
				return index, err
			}
			var wire struct {
				Range       json.RawMessage `json:"range"`
				TypedRange  *scipTypedRange `json:"TypedRange"`
				Symbol      string          `json:"symbol"`
				Roles       int             `json:"symbol_roles"`
				Diagnostics json.RawMessage `json:"diagnostics"`
			}
			if err := json.Unmarshal(raw, &wire); err != nil {
				return index, err
			}
			columns, err := decodeJSONArray(ctx, wire.Range, 4)
			if err != nil {
				return index, err
			}
			diagnostics := bytes.TrimSpace(wire.Diagnostics)
			if len(diagnostics) > 0 && !bytes.Equal(diagnostics, []byte("null")) {
				if diagnostics[0] != '[' {
					return index, fmt.Errorf("SCIP diagnostics must be an array")
				}
				// Unmarshal already checked JSON syntax. Only presence matters;
				// inspecting the interior avoids allocating diagnostic elements.
				if len(bytes.TrimSpace(diagnostics[1:len(diagnostics)-1])) == 0 {
					diagnostics = nil
				}
			} else {
				diagnostics = nil
			}
			o := scipOccurrence{TypedRange: wire.TypedRange, Symbol: wire.Symbol, Roles: wire.Roles, Diagnostics: diagnostics}
			for _, column := range columns {
				// encoding/json accepts null into an int as zero; a missing
				// coordinate must not become an observed source position.
				if bytes.Equal(bytes.TrimSpace(column), []byte("null")) {
					return index, fmt.Errorf("SCIP range coordinate must not be null")
				}
				var n int
				if err := json.Unmarshal(column, &n); err != nil {
					return index, err
				}
				o.Range = append(o.Range, n)
			}
			d.Occurrences = append(d.Occurrences, o)
		}
		index.Documents = append(index.Documents, d)
	}
	if len(envelope.Metadata) > 0 {
		if err := json.Unmarshal(envelope.Metadata, &index.Metadata); err != nil {
			return index, err
		}
	}
	return index, ctx.Err()
}

// ImportSCIP consumes the JSON emitted by the operator-pinned `scip print --json`
// converter, not a homemade wire format. Unknown fields are intentionally ignored
// for SCIP forward compatibility. Truncation, invalid ranges, duplicate documents
// and mismatched evidence are errors, never successful empty indices. The reader
// must be a completed bounded artifact, not a live unbounded process stream.
func ImportSCIP(ctx context.Context, s Snapshot, e RunEvidence, r io.Reader) (Result, error) {
	if err := s.validateEvidence(e); err != nil {
		return Result{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	b, err := io.ReadAll(io.LimitReader(r, MaxArtifactBytes+1))
	if err != nil {
		return Result{}, fmt.Errorf("read SCIP: %w", err)
	}
	if len(b) > MaxArtifactBytes {
		return Result{}, fmt.Errorf("SCIP artifact exceeds %d bytes", MaxArtifactBytes)
	}
	index, err := decodeSCIP(ctx, b)
	if err != nil {
		return Result{}, fmt.Errorf("decode SCIP JSON: %w", err)
	}
	root, err := rootURL(index.Metadata.ProjectRoot)
	if err != nil {
		return Result{}, err
	}
	expected, _ := rootURL(s.RootURI)
	if root.Path != expected.Path || index.Metadata.ToolInfo.Name != e.Tool || index.Metadata.ToolInfo.Version != e.ToolVersion {
		return Result{}, fmt.Errorf("SCIP root or tool provenance mismatch")
	}
	if len(index.Documents) > MaxFiles {
		return Result{}, fmt.Errorf("SCIP document limit exceeded")
	}
	out := Result{OrgID: s.OrgID, RepoID: s.RepoID, Revision: s.Revision, SnapshotDigest: e.SnapshotDigest,
		ArtifactHash: digest(b), Tool: e.Tool, ToolVersion: e.ToolVersion, ExecutionDigest: e.ExecutionDigest}
	seen := map[string]bool{}
	count := 0
	for _, d := range index.Documents {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		source, ok := s.Files[d.Path]
		if !validPath(d.Path) || !ok || seen[d.Path] {
			return Result{}, fmt.Errorf("SCIP document %q is invalid, duplicate or absent from snapshot", d.Path)
		}
		if !utf8.Valid(source) || (d.Text != nil && *d.Text != string(source)) {
			return Result{}, fmt.Errorf("SCIP document %q has invalid or mismatched source text", d.Path)
		}
		if d.PositionEncoding < 0 || d.PositionEncoding > 3 {
			return Result{}, fmt.Errorf("SCIP document %q has unknown position encoding", d.Path)
		}
		lines := strings.Split(string(source), "\n")
		seen[d.Path] = true
		file := Document{Path: d.Path, ContentHash: digest(source), Language: d.Language, Status: "indexed", PositionEncoding: d.PositionEncoding}
		if d.PositionEncoding == 0 {
			file.Status = "encoding_unspecified"
		}
		count += len(d.Occurrences)
		if count > MaxOccurrences {
			return Result{}, fmt.Errorf("SCIP occurrence limit exceeded")
		}
		for _, o := range d.Occurrences {
			if err := ctx.Err(); err != nil {
				return Result{}, err
			}
			rng, err := decodeSCIPRange(o.Range, o.TypedRange)
			if err != nil || !validRange(lines, rng, d.PositionEncoding) || o.Roles < 0 {
				return Result{}, fmt.Errorf("SCIP document %q contains invalid occurrence range or roles", d.Path)
			}
			if len(o.Diagnostics) > 0 {
				file.Status = "diagnostics"
			}
			if o.Symbol == "" {
				continue
			}
			if !utf8.ValidString(o.Symbol) || strings.ContainsRune(o.Symbol, 0) || len(o.Symbol) > 16384 {
				return Result{}, fmt.Errorf("SCIP document %q contains invalid symbol", d.Path)
			}
			file.Occurrences = append(file.Occurrences, Occurrence{
				SymbolID: symbolID(s, d.Path, o.Symbol), Symbol: o.Symbol, Range: rng, Definition: o.Roles&1 != 0, Roles: o.Roles,
			})
		}
		out.Documents = append(out.Documents, file)
	}
	for p := range s.Files {
		if !seen[p] {
			out.MissingPaths = append(out.MissingPaths, p)
		}
	}
	sort.Strings(out.MissingPaths)
	sort.Slice(out.Documents, func(i, j int) bool { return out.Documents[i].Path < out.Documents[j].Path })
	return out, ctx.Err()
}

// Global SCIP symbols are exact opaque IDs. Local IDs have meaning only inside
// one document. We add no revision component, but producer symbols can include
// package versions; cross-revision symbol stability is not guaranteed.
func symbolID(s Snapshot, p, symbol string) string {
	localPath := ""
	if strings.HasPrefix(symbol, "local ") {
		localPath = p
	}
	b, _ := json.Marshal([]string{s.OrgID.String(), s.RepoID.String(), localPath, symbol})
	return "scip:" + digest(b)
}

func scipRange(v []int) (Range, error) {
	var r Range
	switch len(v) {
	case 3:
		r = Range{Position{v[0], v[1]}, Position{v[0], v[2]}}
	case 4:
		r = Range{Position{v[0], v[1]}, Position{v[2], v[3]}}
	default:
		return r, fmt.Errorf("SCIP range must have 3 or 4 elements")
	}
	return r, nil
}
