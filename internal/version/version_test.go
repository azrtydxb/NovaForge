package version_test

import (
	"strings"
	"testing"

	"github.com/novaforge/novaforge/internal/version"
)

func TestRequireRejectsOldGit(t *testing.T) {
	err := version.Require("git", "2.40.0")
	if err != nil && !strings.Contains(err.Error(), "git") {
		t.Fatalf("want git in error, got %v", err)
	}
}

func TestRequireMissingBinary(t *testing.T) {
	if err := version.Require("definitely-not-a-binary", "1.0.0"); err == nil {
		t.Fatal("want error for missing binary")
	}
}

func TestRequireRejectsTooNewMinimum(t *testing.T) {
	err := version.Require("git", "99.0.0")
	if err == nil {
		t.Fatal("want error when installed git is older than the required minimum")
	}
	if !strings.Contains(err.Error(), "older than required") {
		t.Fatalf("want 'older than required' in error, got %v", err)
	}
}
