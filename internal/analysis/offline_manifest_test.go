package analysis

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestAdvisoryManifestStrictValidity(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	base := `{"schema_version":1,"generated_at":"2026-09-17T11:00:00Z","valid_until":"2026-09-17T13:00:00Z","ecosystems":{"Go":"` + strings.Repeat("a", 64) + `"}}`
	if _, err := parseAdvisoryManifest([]byte(base), now); err != nil {
		t.Fatal(err)
	}
	bad := map[string]string{
		"unknown":             strings.Replace(base, `"schema_version":1`, `"unknown":1,"schema_version":1`, 1),
		"case alias":          strings.Replace(base, "schema_version", "SCHEMA_VERSION", 1),
		"duplicate":           strings.Replace(base, `"schema_version":1`, `"schema_version":1,"schema_version":1`, 1),
		"duplicate ecosystem": strings.Replace(base, `"Go":`, `"Go":"`+strings.Repeat("b", 64)+`","Go":`, 1),
		"null":                "null", "empty": "{}", "trailing": base + "{}",
		"version":           strings.Replace(base, `"schema_version":1`, `"schema_version":2`, 1),
		"wrong type":        strings.Replace(base, `"schema_version":1`, `"schema_version":"1"`, 1),
		"expired boundary":  strings.Replace(base, "13:00:00Z", "12:00:00Z", 1),
		"future":            strings.Replace(base, "11:00:00Z", "12:00:01Z", 1),
		"traversal":         strings.Replace(base, `"Go":`, `"../Go":`, 1),
		"unknown ecosystem": strings.Replace(base, `"Go":`, `"Unrecognized":`, 1),
		"digest":            strings.Replace(base, strings.Repeat("a", 64), strings.Repeat("A", 64), 1),
		"empty ecosystems":  strings.Replace(base, `"Go":"`+strings.Repeat("a", 64)+`"`, "", 1),
		"too large":         base + strings.Repeat(" ", advisoryManifestLimit),
	}
	for name, raw := range bad {
		t.Run(name, func(t *testing.T) {
			if m, err := parseAdvisoryManifest([]byte(raw), now); err == nil {
				t.Fatalf("invalid manifest accepted: %+v", m)
			}
		})
	}
	if _, err := parseAdvisoryManifest([]byte(base), time.Time{}); err == nil {
		t.Fatal("missing trusted clock accepted")
	}
}

func TestOfflineResultRequiresCoveredPackages(t *testing.T) {
	for _, test := range []struct {
		name, body string
		exit       int
	}{
		{"foreign ecosystem", `{"results":[{"packages":[{"package":{"name":"foo","version":"1","ecosystem":"npm"}}]}]}`, 0},
		{"no packages", `{"results":[]}`, 0},
		{"no lockfiles", `{"results":[]}`, 128},
		{"scanner outage", `{"results":[{"packages":[{"package":{"name":"foo","version":"1","ecosystem":"Go"}}]}]}`, 127},
		{"finding exit without finding", `{"results":[{"packages":[{"package":{"name":"foo","version":"1","ecosystem":"Go"}}]}]}`, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, _ := json.Marshal(advisoryManifest{SchemaVersion: 1, GeneratedAt: time.Now().Add(-time.Hour), ValidUntil: time.Now().Add(time.Hour), Ecosystems: map[string]string{"Go": strings.Repeat("a", 64)}})
			run := func(ctx context.Context, dir, name string, args ...string) ([]byte, int, error) {
				if name == "sh" && args[2] == "advisory-manifest" {
					return raw, 0, nil
				}
				if name == "sh" {
					return []byte("100\n" + strings.Repeat("a", 64) + "  " + OfflineAdvisoryRoot + "/osv-scalibr/Go/all.zip\n"), 0, nil
				}
				return []byte(test.body), test.exit, nil
			}
			if found, err := Vulnerabilities(context.Background(), run, t.TempDir()); err == nil {
				t.Fatalf("unavailable evidence accepted: %+v", found)
			}
		})
	}
}

func TestOfflineScanCannotOutliveManifest(t *testing.T) {
	validUntil := time.Now().Add(time.Second)
	raw, _ := json.Marshal(advisoryManifest{SchemaVersion: 1, GeneratedAt: time.Now().Add(-time.Hour), ValidUntil: validUntil, Ecosystems: map[string]string{"Go": strings.Repeat("a", 64)}})
	calls := 0
	run := func(ctx context.Context, dir, name string, args ...string) ([]byte, int, error) {
		if name == "sh" && args[2] == "advisory-manifest" {
			return raw, 0, nil
		}
		if name == "sh" {
			return []byte("100\n" + strings.Repeat("a", 64) + "  " + OfflineAdvisoryRoot + "/osv-scalibr/Go/all.zip\n"), 0, nil
		}
		calls++
		time.Sleep(time.Until(validUntil) + time.Millisecond)
		return []byte(`{"results":[{"packages":[{"package":{"name":"foo","version":"1","ecosystem":"Go"}}]}]}`), 0, nil
	}
	if found, err := Vulnerabilities(context.Background(), run, t.TempDir()); err == nil || calls != 1 {
		t.Fatalf("expired result accepted or scanner not reached: %+v %v calls=%d", found, err, calls)
	}
}
