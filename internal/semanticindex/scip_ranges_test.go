package semanticindex

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

func TestSCIPTypedRangesRealConverter(t *testing.T) {
	requireTool(t, "scip")
	// SCIP 0.9 added typed range oneofs. Use the installed converter itself to
	// establish its JSON shape; language fixtures cover compiler identities.
	field := func(number protowire.Number, value []byte) []byte {
		return protowire.AppendBytes(protowire.AppendTag(nil, number, protowire.BytesType), value)
	}
	tool := append(field(1, []byte("fixture")), field(2, []byte("1"))...)
	meta := append(field(2, tool), field(3, []byte("file:///workspace"))...)
	occurrence := append(field(2, []byte("local 0")), []byte{24, 1}...)
	occurrence = append(occurrence, field(8, []byte{16, 6, 24, 7})...)
	doc := append(field(1, []byte("a.ts")), field(2, occurrence)...)
	doc = append(doc, 48, 1) // UTF-8 position encoding
	binary := append(field(1, meta), field(2, doc)...)
	dir := t.TempDir()
	file := filepath.Join(dir, "index.scip")
	if err := os.WriteFile(file, binary, 0600); err != nil {
		t.Fatal(err)
	}
	data := runTool(t, dir, "scip", "print", "--json", file)
	s := fixtureSnapshot()
	result, err := ImportSCIP(context.Background(), s, fixtureEvidence(t, s), bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Documents) != 1 || len(result.Documents[0].Occurrences) != 1 || result.Documents[0].Occurrences[0].Range != (Range{Position{0, 6}, Position{0, 7}}) {
		t.Fatalf("typed range lost through real SCIP converter: %+v", result)
	}
}

func TestSCIPTypedMultilineAndAmbiguity(t *testing.T) {
	s := fixtureSnapshot()
	for _, tc := range []struct {
		name, occurrence string
		valid            bool
	}{
		{"multiline", `{"TypedRange":{"MultiLineRange":{"start_line":0,"start_character":6,"end_line":1,"end_character":0}},"symbol":"local 0"}`, true},
		{"disagreement", `{"range":[0,0,1],"TypedRange":{"SingleLineRange":{"start_character":6,"end_character":7}},"symbol":"local 0"}`, false},
		{"unknown variant", `{"TypedRange":{"UnknownRange":{}},"symbol":"local 0"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := `{"metadata":{"tool_info":{"name":"fixture","version":"1"},"project_root":"file:///workspace"},"documents":[{"relative_path":"a.ts","position_encoding":1,"occurrences":[` + tc.occurrence + `]}]}`
			_, err := ImportSCIP(context.Background(), s, fixtureEvidence(t, s), strings.NewReader(input))
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
		})
	}
}
