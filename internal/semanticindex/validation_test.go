package semanticindex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestSCIPRejectsInvalidRangesAndSourceText(t *testing.T) {
	for _, tc := range []struct {
		name string
		doc  map[string]any
	}{
		{"column beyond source", map[string]any{"relative_path": "a.ts", "occurrences": []any{map[string]any{"range": []int{0, 6, 999}, "symbol": "local 0"}}}},
		{"negative line", map[string]any{"relative_path": "a.ts", "occurrences": []any{map[string]any{"range": []int{-1, 0, 1}, "symbol": "local 0"}}}},
		{"unknown encoding", map[string]any{"relative_path": "a.ts", "position_encoding": 99}},
		{"mismatched embedded source", map[string]any{"relative_path": "a.ts", "text": "not the snapshot"}},
		{"missing range", map[string]any{"relative_path": "a.ts", "occurrences": []any{map[string]any{"symbol": "local 0"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := fixtureSnapshot()
			_, err := ImportSCIP(context.Background(), s, fixtureEvidence(t, s), strings.NewReader(scipJSON(t, []any{tc.doc})))
			if err == nil {
				t.Fatal("accepted invalid source evidence")
			}
		})
	}
}

func TestSCIPLimitsCancellationAndDuplicateDocuments(t *testing.T) {
	s := fixtureSnapshot()
	e := fixtureEvidence(t, s)
	for _, input := range []string{
		strings.Repeat(" ", MaxArtifactBytes+1),
		scipJSON(t, []any{map[string]any{"relative_path": "a.ts"}, map[string]any{"relative_path": "a.ts"}}),
		`{"metadata":`,
		scipJSON(t, nil) + `{}`,
	} {
		if _, err := ImportSCIP(context.Background(), s, e, strings.NewReader(input)); err == nil {
			t.Fatal("accepted malformed or oversized artifact")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ImportSCIP(ctx, s, e, strings.NewReader(scipJSON(t, nil))); err == nil {
		t.Fatal("ignored cancellation")
	}
}

func TestSnapshotAndSymbolTenantIsolation(t *testing.T) {
	s := fixtureSnapshot()
	d, err := s.Digest()
	if err != nil {
		t.Fatal(err)
	}
	id := symbolID(s, "a.ts", "compiler package Symbol.")
	other := s
	other.OrgID = uuid.New()
	otherDigest, _ := other.Digest()
	if d == otherDigest || id == symbolID(other, "a.ts", "compiler package Symbol.") {
		t.Fatal("organization boundary lost")
	}
	other = s
	other.RepoID = uuid.New()
	otherDigest, _ = other.Digest()
	if d == otherDigest || id == symbolID(other, "a.ts", "compiler package Symbol.") {
		t.Fatal("repository boundary lost")
	}
}

func TestLSPRejectsHostileLocationsAndPreservesLiteralPaths(t *testing.T) {
	s := fixtureSnapshot()
	p := " space ü#%?.ts "
	s.Files[p] = []byte("🚀 foo\n")
	good := Range{Position{0, 3}, Position{0, 6}}
	data, _ := json.Marshal(map[string]any{"targetUri": sourceURI(s, p), "targetSelectionRange": good})
	result, err := decodeLocations(context.Background(), s, Query{}, data, sourceCache{})
	if err != nil || len(result.Targets) != 1 || result.Targets[0].Path != p {
		t.Fatalf("literal URI roundtrip failed: %+v %v", result, err)
	}
	for _, uri := range []string{"file:///workspace/../a.ts", "file:///workspace/%2e%2e/a.ts", "file:///workspace/unknown.ts", "file:///workspace/a.ts?query=1"} {
		data, _ := json.Marshal(map[string]any{"uri": uri, "range": Range{Position{}, Position{0, 1}}})
		if _, err := decodeLocations(context.Background(), s, Query{}, data, sourceCache{}); err == nil {
			t.Fatalf("accepted hostile location %q", uri)
		}
	}
	for _, raw := range []string{`{}`, `{"uri":"file:///workspace/a.ts","range":{}}`, `{"uri":"file:///workspace/a.ts","range":{"start":{},"end":{}}}`} {
		if _, err := decodeLocations(context.Background(), s, Query{}, []byte(raw), sourceCache{}); err == nil {
			t.Fatalf("accepted missing position fields: %s", raw)
		}
	}
	data, _ = json.Marshal(map[string]any{"uri": "file:///workspace-other/a.ts", "range": good})
	result, err = decodeLocations(context.Background(), s, Query{}, data, sourceCache{})
	if err != nil || result.ExternalTargets != 1 || len(result.Targets) != 0 {
		t.Fatalf("external path mapped inside repository: %+v %v", result, err)
	}
}

func TestPositionEncodings(t *testing.T) {
	lines := []string{"🚀 foo"}
	for _, tc := range []struct {
		encoding, column int
		valid            bool
	}{
		{1, 4, true}, {1, 1, false}, {2, 2, true}, {2, 1, false}, {3, 1, true}, {3, 9, false}, {99, 0, false},
	} {
		if got := validPosition(lines, Position{0, tc.column}, tc.encoding); got != tc.valid {
			t.Fatalf("encoding %d column %d: got %v", tc.encoding, tc.column, got)
		}
	}
}

func TestLSPRefusesServerEditsAndUnexpectedResponses(t *testing.T) {
	frame := func(b string) string { return fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(b), b) }
	input := frame(`{"jsonrpc":"2.0","id":"edit","method":"workspace/applyEdit","params":{"edit":{}}}`) + frame(`{"jsonrpc":"2.0","id":1,"result":null}`)
	var output bytes.Buffer
	c := lspConnection{reader: bufio.NewReader(strings.NewReader(input)), writer: &output}
	if _, err := c.call("textDocument/definition", nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"code":-32601`) || !strings.Contains(output.String(), `"id":"edit"`) {
		t.Fatal("server edit request was not explicitly refused")
	}
	for _, response := range []string{`{"jsonrpc":"2.0","id":99,"result":null}`, `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"failed"}}`, `{"jsonrpc":"2.0","id":1}`} {
		c := lspConnection{reader: bufio.NewReader(strings.NewReader(frame(response))), writer: io.Discard}
		if _, err := c.call("textDocument/definition", nil); err == nil {
			t.Fatal("accepted invalid/error LSP response")
		}
	}
}

func TestLSPFramingRejectsMalformedAndOversized(t *testing.T) {
	for _, input := range []string{
		"Content-Length: 0\r\nContent-Length: 0\r\n\r\n",
		"Content-Length: 99999999999\r\n\r\n",
		"Content-Length: -1\r\n\r\n",
		"Content-Length: 5\r\n\r\n{}",
		"Content-Length: 2\n\n{}",
		strings.Repeat("A", 8193),
	} {
		c := lspConnection{reader: bufio.NewReader(strings.NewReader(input)), writer: io.Discard}
		if _, err := c.receive(); err == nil {
			t.Fatalf("accepted invalid LSP frame %q", input[:min(len(input), 50)])
		}
	}
}
