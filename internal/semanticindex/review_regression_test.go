package semanticindex

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
	"testing"
)

// Allocation ceilings are deliberately modest: hostile fixtures remain below
// 2 MB and never try to exhaust the test process. The large margin tolerates
// runtime bookkeeping, but not materializing every over-limit element.
func reviewAllocated(f func()) uint64 {
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

func TestReviewSCIPDecodeCollectionBounds(t *testing.T) {
	s := fixtureSnapshot()
	e := fixtureEvidence(t, s)
	for _, tc := range []struct {
		name, input string
		ceiling     uint64
	}{
		{"documents", `{"documents":[` + strings.Repeat(`{},`, 100000) + `{}]}`, 12 << 20},
		{"occurrences", `{"documents":[{"relative_path":"a.ts","occurrences":[` + strings.Repeat(`{},`, MaxOccurrences+10000) + `{}]}]}`, 60 << 20},
		{"range", `{"documents":[{"relative_path":"a.ts","occurrences":[{"range":[` + strings.Repeat(`0,`, 100000) + `0]}]}]}`, 4 << 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var err error
			used := reviewAllocated(func() { _, err = ImportSCIP(context.Background(), s, e, strings.NewReader(tc.input)) })
			t.Logf("allocated %d bytes for %d-byte fixture", used, len(tc.input))
			if err == nil || !strings.Contains(err.Error(), "limit") {
				t.Errorf("expected early collection limit, got %v", err)
			}
			if used > tc.ceiling {
				t.Errorf("decode allocated %d > ceiling %d", used, tc.ceiling)
			}
		})
	}
}

func TestReviewLSPDecodeCollectionBounds(t *testing.T) {
	s := fixtureSnapshot()
	input := []byte(`[` + strings.Repeat(`{},`, 100000) + `{}]`)
	var err error
	used := reviewAllocated(func() { _, err = decodeLocations(context.Background(), s, Query{}, input, sourceCache{}) })
	t.Logf("targets allocated %d", used)
	if err == nil || !strings.Contains(err.Error(), "limit") {
		t.Errorf("expected target limit, got %v", err)
	}
	if used > 4<<20 {
		t.Errorf("target decode allocated %d", used)
	}
	params := `{"jsonrpc":"2.0","id":2,"method":"workspace/configuration","params":{"items":` + string(input) + `}}`
	frame := fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(params), params)
	c := lspConnection{reader: bufio.NewReader(strings.NewReader(frame)), writer: io.Discard}
	used = reviewAllocated(func() { _, err = c.call("initialize", nil) })
	t.Logf("configuration allocated %d", used)
	if err == nil {
		t.Fatal("accepted oversized configuration")
	}
	if used > 5<<20 {
		t.Errorf("configuration decode allocated %d", used)
	}
}

func TestReviewRejectsLossyJSONIdentities(t *testing.T) {
	s := fixtureSnapshot()
	s.Files["\ufffd.ts"] = []byte("x")
	e := fixtureEvidence(t, s)
	for _, symbol := range []string{`bad\ud800`, `bad\ud801`, "bad\xff"} {
		input := `{"metadata":{"project_root":"file:///workspace","tool_info":{"name":"fixture","version":"1"}},"documents":[{"relative_path":"a.ts","occurrences":[{"range":[0,0,1],"symbol":"` + symbol + `"}]}]}`
		if _, err := ImportSCIP(context.Background(), s, e, strings.NewReader(input)); err == nil {
			t.Errorf("accepted malformed symbol %q", symbol)
		}
	}
	for _, path := range []string{`\ud800.ts`, `\ud801.ts`, "\xff.ts", "\ufffd.ts", `\ufffd.ts`} {
		valid := path == "\ufffd.ts" || path == `\ufffd.ts`
		data := []byte(`{"uri":"file:///workspace/` + path + `","range":{"start":{"line":0,"character":0},"end":{"line":0,"character":1}}}`)
		_, err := decodeLocations(context.Background(), s, Query{}, data, sourceCache{})
		if (err == nil) != valid {
			t.Errorf("path %q valid=%v err=%v", path, valid, err)
		}
	}
}

type reviewCancelReader struct {
	io.Reader
	cancel context.CancelFunc
}

func (r reviewCancelReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.cancel()
	return n, err
}
func TestReviewSCIPCancellationAfterRead(t *testing.T) {
	s := fixtureSnapshot()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err := ImportSCIP(ctx, s, fixtureEvidence(t, s), reviewCancelReader{strings.NewReader(scipJSON(t, []any{})), cancel})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled empty result returned %v", err)
	}
}

func TestReviewLSPRepeatedTargetPreprocessing(t *testing.T) {
	s := fixtureSnapshot()
	s.Files["a.ts"] = bytes.Repeat([]byte("\n"), 16<<10)
	item := `{"uri":"file:///workspace/a.ts","range":{"start":{"line":0,"character":0},"end":{"line":0,"character":0}}}`
	data := []byte(`[` + strings.Repeat(item+`,`, 199) + item + `]`)
	var err error
	used := reviewAllocated(func() {
		var result Resolution
		result, err = decodeLocations(context.Background(), s, Query{}, data, sourceCache{})
		if len(result.Targets) != 200 {
			t.Errorf("targets=%d", len(result.Targets))
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("repeated target allocation: %d", used)
	if used > 5<<20 {
		t.Fatalf("target preprocessing repeated: %d bytes", used)
	}
}

// Trigger real context cancellation at a deterministic traversal checkpoint,
// without racing a timer against the host's speed.
type reviewTraversalContext struct {
	context.Context
	cancel    context.CancelFunc
	remaining int
}

func (c *reviewTraversalContext) Err() error {
	c.remaining--
	if c.remaining <= 0 {
		c.cancel()
	}
	return c.Context.Err()
}
func reviewCancelAfter(t *testing.T, checks int) context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &reviewTraversalContext{ctx, cancel, checks}
}
func TestReviewCancellationDuringDecoding(t *testing.T) {
	t.Run("SCIP", func(t *testing.T) {
		input := []byte(`{"documents":[` + strings.Repeat(`{},`, 999) + `{}]}`)
		_, err := decodeSCIP(reviewCancelAfter(t, 100), input)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("decoding ignored cancellation: %v", err)
		}
	})
	t.Run("Unicode scan", func(t *testing.T) {
		err := validateJSONStrings(reviewCancelAfter(t, 3), []byte(`"`+strings.Repeat("x", 32768)+`"`))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("scan ignored cancellation: %v", err)
		}
	})
	t.Run("LSP target traversal", func(t *testing.T) {
		s := fixtureSnapshot()
		item := `{"uri":"file:///workspace/a.ts","range":{"start":{"line":0,"character":0},"end":{"line":0,"character":0}}}`
		data := []byte(`[` + strings.Repeat(item+`,`, MaxQueries-1) + item + `]`)
		// Raw-array decoding needs MaxQueries checks plus ~60 string-scan
		// checks. This budget reaches cancellation inside target validation.
		_, err := decodeLocations(reviewCancelAfter(t, MaxQueries+200), s, Query{}, data, sourceCache{})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("target traversal ignored cancellation: %v", err)
		}
	})
	t.Run("empty LSP result", func(t *testing.T) {
		_, err := decodeLocations(reviewCancelAfter(t, 1), fixtureSnapshot(), Query{}, []byte("null"), sourceCache{})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("empty result ignored cancellation: %v", err)
		}
	})
}

func TestReviewLSPSourceCacheAcrossResponses(t *testing.T) {
	s := fixtureSnapshot()
	s.Files["a.ts"] = bytes.Repeat([]byte("\n"), 16<<10)
	data := []byte(`{"uri":"file:///workspace/a.ts","range":{"start":{"line":0,"character":0},"end":{"line":0,"character":0}}}`)
	cache := sourceCache{}
	used := reviewAllocated(func() {
		for i := 0; i < 200; i++ {
			result, err := decodeLocations(context.Background(), s, Query{}, data, cache)
			if err != nil || len(result.Targets) != 1 || result.Targets[0].ContentHash != digest(s.Files["a.ts"]) {
				t.Fatalf("lost target/hash: %+v %v", result, err)
			}
		}
	})
	t.Logf("session preprocessing allocation: %d", used)
	if used > 5<<20 {
		t.Fatalf("preprocessing repeated across responses: %d bytes", used)
	}
}

func TestReviewSCIPCompactRangeCoordinates(t *testing.T) {
	s := fixtureSnapshot()
	e := fixtureEvidence(t, s)
	for _, coordinates := range [][]any{{0, 0, 1}, {0, 0, 0, 1}} {
		// Every null would otherwise become zero and still form a valid range,
		// including a zero-length range when the end character becomes zero.
		for nullAt := -1; nullAt < len(coordinates); nullAt++ {
			t.Run(fmt.Sprintf("length_%d/null_at_%d", len(coordinates), nullAt), func(t *testing.T) {
				values := append([]any(nil), coordinates...)
				if nullAt >= 0 {
					values[nullAt] = nil
				}
				input := scipJSON(t, []any{map[string]any{
					"relative_path": "a.ts", "position_encoding": 1,
					"occurrences": []any{map[string]any{"range": values, "symbol": "local 0"}},
				}})
				result, err := ImportSCIP(context.Background(), s, e, strings.NewReader(input))
				if nullAt >= 0 {
					if err == nil {
						t.Fatalf("accepted null coordinate %d in %s", nullAt, input)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if result.AbsenceSafe || len(result.Documents) != 1 || len(result.Documents[0].Occurrences) != 1 {
					t.Fatalf("lost occurrence or claimed absence safety: %+v", result)
				}
				if got := result.Documents[0].Occurrences[0].Range; got != (Range{Position{0, 0}, Position{0, 1}}) {
					t.Fatalf("numeric zero coordinates changed: %+v", got)
				}
			})
		}
	}
}

func TestReviewSCIPDiagnosticsShape(t *testing.T) {
	s := fixtureSnapshot()
	for _, tc := range []struct {
		raw    string
		valid  bool
		status string
	}{
		{`[ ]`, true, "encoding_unspecified"}, {`null`, true, "encoding_unspecified"},
		{`[{}]`, true, "diagnostics"}, {`{}`, false, ""}, {`"bad"`, false, ""},
	} {
		input := `{"metadata":{"project_root":"file:///workspace","tool_info":{"name":"fixture","version":"1"}},"documents":[{"relative_path":"a.ts","occurrences":[{"range":[0,0,1],"diagnostics":` + tc.raw + `}]}]}`
		result, err := ImportSCIP(context.Background(), s, fixtureEvidence(t, s), strings.NewReader(input))
		if (err == nil) != tc.valid {
			t.Errorf("diagnostics %s valid=%v err=%v", tc.raw, tc.valid, err)
		}
		if tc.valid && err == nil && result.Documents[0].Status != tc.status {
			t.Errorf("diagnostics %s status %s", tc.raw, result.Documents[0].Status)
		}
	}
}
