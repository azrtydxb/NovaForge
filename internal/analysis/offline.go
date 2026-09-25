package analysis

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// OfflineAdvisoryRoot is image-owned and read-only in the analysis sandbox.
// Repositories cannot select a cache, override its metadata, or refresh it over
// the network. Image publication supplies an explicit validity interval.
const OfflineAdvisoryRoot = "/opt/analysis/osv"
const advisoryManifestLimit = 64 << 10
const advisoryZIPLimit = 512 << 20

var advisoryDigest = regexp.MustCompile(`^[a-f0-9]{64}$`)

// Fixed ecosystem-to-directory spelling prevents manifest paths from escaping
// the trusted image. Unrepresented ecosystems are unavailable, never clean.
var advisoryEcosystems = map[string]string{
	"Go": "Go", "npm": "npm", "PyPI": "PyPI", "Maven": "Maven",
	"crates.io": "crates.io", "NuGet": "NuGet", "Packagist": "Packagist",
	"RubyGems": "RubyGems", "Pub": "Pub", "Hex": "Hex", "Hackage": "Hackage",
}

type advisoryManifest struct {
	SchemaVersion int               `json:"schema_version"`
	GeneratedAt   time.Time         `json:"generated_at"`
	ValidUntil    time.Time         `json:"valid_until"`
	Ecosystems    map[string]string `json:"ecosystems"`
}

func parseAdvisoryManifest(raw []byte, now time.Time) (advisoryManifest, error) {
	var m advisoryManifest
	if len(raw) > advisoryManifestLimit || now.IsZero() {
		return m, fmt.Errorf("advisory manifest size or trusted clock unavailable")
	}
	if err := uniqueJSON(json.NewDecoder(bytes.NewReader(raw)), 0); err != nil {
		return m, err
	}
	// Struct decoding accepts case-insensitive aliases; the image manifest
	// contract deliberately has one spelling for every required field.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return m, err
	}
	if len(fields) != 4 {
		return m, fmt.Errorf("advisory manifest requires exactly four fields")
	}
	for _, key := range []string{"schema_version", "generated_at", "valid_until", "ecosystems"} {
		if _, ok := fields[key]; !ok {
			return m, fmt.Errorf("missing advisory manifest field %s", key)
		}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return m, fmt.Errorf("advisory manifest: %w", err)
	}
	if dec.Decode(new(any)) != io.EOF {
		return m, fmt.Errorf("trailing advisory manifest data")
	}
	if m.SchemaVersion != 1 || m.GeneratedAt.IsZero() || m.ValidUntil.IsZero() || m.GeneratedAt.After(now) || !now.Before(m.ValidUntil) || !m.GeneratedAt.Before(m.ValidUntil) {
		return m, fmt.Errorf("advisory snapshot is missing, future-dated, or expired")
	}
	if len(m.Ecosystems) == 0 || len(m.Ecosystems) > len(advisoryEcosystems) {
		return m, fmt.Errorf("advisory ecosystem coverage unavailable")
	}
	for ecosystem, digest := range m.Ecosystems {
		if advisoryEcosystems[ecosystem] == "" || !advisoryDigest.MatchString(digest) {
			return m, fmt.Errorf("invalid advisory ecosystem or digest %q", ecosystem)
		}
	}
	return m, nil
}

// encoding/json otherwise accepts duplicate keys, allowing conflicting validity
// or digest fields to look well-formed depending on which reader consumes them.
func uniqueJSON(dec *json.Decoder, depth int) error {
	if depth > 8 {
		return fmt.Errorf("advisory manifest nesting exceeds bound")
	}
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for dec.More() {
			key, err := dec.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return fmt.Errorf("duplicate or invalid advisory manifest key")
			}
			seen[name] = true
			if err := uniqueJSON(dec, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for dec.More() {
			if err := uniqueJSON(dec, depth+1); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("invalid advisory manifest delimiter")
	}
	_, err = dec.Token()
	return err
}

// The path arguments here are built only from the fixed image path and fixed
// ecosystem directory map. Test every ancestor for symlinks before reading.
const advisoryReadScript = `for p in "$@"; do [ ! -L "$p" ] || exit 70; done; [ -f "$1" ] || exit 71; exec head -c 65537 -- "$1"`
const advisoryHashScript = `for p in "$@"; do [ ! -L "$p" ] || exit 70; done; [ -f "$1" ] || exit 71; size=$(wc -c < "$1") || exit 72; [ "$size" -gt 0 ] && [ "$size" -le 536870912 ] || exit 73; printf '%s\n' "$size"; exec sha256sum -- "$1"`

func checkedAdvisories(ctx context.Context, run Exec, dir string) (advisoryManifest, error) {
	path := OfflineAdvisoryRoot + "/manifest.json"
	out, exit, err := run(ctx, dir, "sh", "-c", advisoryReadScript, "advisory-manifest", path, "/opt", "/opt/analysis", OfflineAdvisoryRoot)
	if err != nil {
		return advisoryManifest{}, err
	}
	if exit != 0 {
		return advisoryManifest{}, fmt.Errorf("offline advisory manifest unavailable (exit %d)", exit)
	}
	m, err := parseAdvisoryManifest(out, time.Now().UTC())
	if err != nil {
		return m, err
	}
	keys := make([]string, 0, len(m.Ecosystems))
	for key := range m.Ecosystems {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var total int64
	for _, key := range keys {
		ecosystemDir := OfflineAdvisoryRoot + "/osv-scalibr/" + advisoryEcosystems[key]
		path := ecosystemDir + "/all.zip"
		out, exit, err := run(ctx, dir, "sh", "-c", advisoryHashScript, "advisory-hash", path, "/opt", "/opt/analysis", OfflineAdvisoryRoot, OfflineAdvisoryRoot+"/osv-scalibr", ecosystemDir)
		if err != nil {
			return m, err
		}
		if exit != 0 || len(out) > 1024 {
			return m, fmt.Errorf("offline %s database unavailable or exceeds bound (exit %d)", key, exit)
		}
		sizeLine, hashLine, ok := strings.Cut(string(out), "\n")
		size, parseErr := strconv.ParseInt(strings.TrimSpace(sizeLine), 10, 64)
		expected := m.Ecosystems[key] + "  " + path + "\n"
		if !ok || parseErr != nil || size <= 0 || size > advisoryZIPLimit || hashLine != expected {
			return m, fmt.Errorf("offline %s database integrity mismatch", key)
		}
		total += size
		if total > 2<<30 {
			return m, fmt.Errorf("offline advisory databases exceed total bound")
		}
	}
	return m, nil
}

func offlineVulnerabilities(ctx context.Context, run Exec, dir string) ([]Vulnerability, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if run == nil {
		return nil, fmt.Errorf("offline vulnerability executor missing")
	}
	manifest, err := checkedAdvisories(ctx, run, dir)
	if err != nil {
		return nil, err
	}
	// --offline alone disables all remote access; --offline-vulnerabilities
	// alone would still allow resolution to call deps.dev. Reachability is
	// disabled conservatively: every affected dependency remains a finding.
	out, exit, err := run(ctx, dir, "env", "-i", "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "HOME=/tmp", "OSV_SCANNER_LOCAL_DB_CACHE_DIRECTORY="+OfflineAdvisoryRoot,
		"osv-scanner", "scan", "source", "--offline", "--no-call-analysis=go,rust", "--no-ignore", "--all-packages", "--all-vulns", "--config", "/dev/null", "--format", "json", "-r", ".")
	if err != nil {
		return nil, err
	}
	// OSV can return well-formed package JSON on extraction/database errors
	// (notably exit 127). Parsing that as no advisories used to pass the gate.
	if exit != 0 && exit != 1 {
		return nil, fmt.Errorf("offline vulnerability scan unavailable (exit %d)", exit)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if now.Before(manifest.GeneratedAt) || !now.Before(manifest.ValidUntil) {
		return nil, fmt.Errorf("advisory snapshot invalidated during scan")
	}
	if len(out) > 8<<20 {
		return nil, fmt.Errorf("offline vulnerability evidence exceeds bound")
	}
	var report osvReport
	if err := json.Unmarshal(out, &report); err != nil {
		return nil, fmt.Errorf("invalid offline vulnerability evidence: %w", err)
	}
	packages := 0
	for _, result := range report.Results {
		for _, p := range result.Packages {
			packages++
			if manifest.Ecosystems[p.Package.Ecosystem] == "" || p.Package.Name == "" || p.Package.Version == "" {
				return nil, fmt.Errorf("offline advisory coverage unavailable for scanned package")
			}
		}
	}
	if packages == 0 {
		return nil, fmt.Errorf("offline vulnerability scan contains no package evidence")
	}
	findings, err := parseOSV(out, dir)
	if err != nil {
		return nil, err
	}
	if (exit == 1) != (len(findings) > 0) {
		return nil, fmt.Errorf("offline scanner exit and findings disagree")
	}
	return findings, nil
}
