package service_test

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
)

// scientific matches a rendered env value Helm printed in exponent notation.
var scientific = regexp.MustCompile(`(?m)^\s+value: "?-?[0-9]+(\.[0-9]+)?e[+-][0-9]+"?\s*$`)

// TestNoEnvValueRendersInScientificNotation guards a whole class of defect, not
// one value. YAML reads a large integer such as 5368709120 as a float, and Helm
// prints it as 5.36870912e+09. Every service parses its numeric settings as
// integers, rejects that form, and falls back to a built-in default — so the
// configured value is silently never read, the codebase's signature defect.
// NF_LFS_MAX_OBJECT_BYTES did exactly this on kw.
//
// proved by: removing int64 from NF_LFS_MAX_OBJECT_BYTES in services.tpl fails
// the kw and oke cases with the offending line.
func TestNoEnvValueRendersInScientificNotation(t *testing.T) {
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm is not installed; the chart cannot be rendered")
	}
	chart := filepath.Join(repoRoot(t), "deploy", "helm", "novaforge")
	for _, values := range []string{"values.yaml", "values-kw.yaml", "values-oke.yaml"} {
		t.Run(values, func(t *testing.T) {
			cmd := exec.Command(helm, "template", "novaforge", chart, "-n", "novaforge",
				"-f", filepath.Join(chart, values), "--set", "runner.orgId=00000000-0000-0000-0000-000000000001")
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("helm template -f %s: %v\n%s", values, err, stderr.String())
			}
			if m := scientific.FindAll(out, -1); len(m) > 0 {
				t.Fatalf("%s renders %d env value(s) in exponent notation, which the services' integer parsing rejects:\n%s\nwrap the value in int64 in the template",
					values, len(m), bytes.Join(m, []byte("\n")))
			}
		})
	}
}
