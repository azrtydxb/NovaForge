// Package version reports the version of external binaries NovaForge shells
// out to, so a service fails at startup rather than at first use.
package version

import (
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

var semverRe = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

// Current returns the NovaForge build version. It is overridden at link time
// with -ldflags "-X github.com/novaforge/novaforge/internal/version.current=...".
var current = "dev"

// Current returns the NovaForge build version.
func Current() string { return current }

// Require reports an error unless bin is on PATH and reports a version of at
// least min. min must be a three-part version such as "2.40.0".
func Require(bin, min string) error {
	out, err := exec.Command(bin, "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s not found: %w", bin, err)
	}
	got := semverRe.FindStringSubmatch(string(out))
	if got == nil {
		return fmt.Errorf("%s: cannot parse version from %q", bin, strings.TrimSpace(string(out)))
	}
	want := semverRe.FindStringSubmatch(min)
	if want == nil {
		return fmt.Errorf("%s: invalid minimum version %q", bin, min)
	}
	for i := 1; i <= 3; i++ {
		g, _ := strconv.Atoi(got[i])
		w, _ := strconv.Atoi(want[i])
		if g > w {
			return nil
		}
		if g < w {
			return fmt.Errorf("%s %s is older than required %s", bin, got[0], min)
		}
	}
	return nil
}
