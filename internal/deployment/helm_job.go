package deployment

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path"
	"regexp"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

var valueKey = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_.-]{0,127}$`)

// HelmEvidence is the entire public output of a deployment job. A Helm release
// includes manifests and values (possibly Secrets), so raw stdout/stderr must
// never become job logs or attempt evidence.
type HelmEvidence struct {
	State     string `json:"state"`
	Release   string `json:"release"`
	Namespace string `json:"namespace"`
	Revision  int    `json:"revision"`
	Binding   string `json:"binding"`
}

type helmRelease struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Version   int    `json:"version"`
	Info      struct {
		Status      string `json:"status"`
		Description string `json:"description"`
	} `json:"info"`
}

// Do not embed bytes.Buffer: its promoted ReadFrom lets io.Copy bypass Write
// and therefore the bound, including when os/exec captures process stdout.
type boundedOutput struct{ buffer bytes.Buffer }

func (b *boundedOutput) Bytes() []byte { return b.buffer.Bytes() }

func (b *boundedOutput) Write(p []byte) (int, error) {
	if b.buffer.Len()+len(p) > 16<<20 {
		return 0, errors.New("Helm output exceeds evidence limit")
	}
	return b.buffer.Write(p)
}

// RunHelmJob is the trusted executor image's entrypoint. It executes Helm only
// inside the isolated job, using a mounted OpenBao-issued kubeconfig. The image
// owns the chart and this wrapper; repository commands are never executed here.
// The caller must exit nonzero when this returns an error, without printing raw
// Helm output. Args are produced solely by HelmExecutor's fixed configuration.
func RunHelmJob(ctx context.Context, args []string, out io.Writer) error {
	if len(args) != 8 {
		return errors.New("invalid Helm job arguments")
	}
	mode, release, chart, namespace, key, digest, binding, previous := args[0], args[1], args[2], args[3], args[4], args[5], args[6], args[7]
	if (mode != "execute" && mode != "observe") || len(validation.IsDNS1123Label(release)) > 0 || len(release) > 53 || len(validation.IsDNS1123Label(namespace)) > 0 || !strings.HasPrefix(chart, "/charts/") || path.Clean(chart) != chart || !strings.HasSuffix(chart, ".tgz") || !valueKey.MatchString(key) || !artifactDigest.MatchString(digest) || binding == "" || len(binding) > 256 || previous == "" || len(previous) > 256 {
		return errors.New("invalid Helm job arguments")
	}
	invoke := func(argv ...string) (helmRelease, error) {
		cmd := exec.CommandContext(ctx, "helm", argv...)
		var stdout boundedOutput
		cmd.Stdout = &stdout
		cmd.Stderr = io.Discard
		if err := cmd.Run(); err != nil {
			return helmRelease{}, errors.New("Helm did not return verifiable release evidence")
		}
		var got helmRelease
		if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
			return helmRelease{}, errors.New("Helm returned invalid release evidence")
		}
		return got, nil
	}
	common := []string{"--namespace", namespace, "--kubeconfig", "/credentials/config", "--output", "json"}
	observe := func() (helmRelease, error) { return invoke(append([]string{"status", release}, common...)...) }
	emit := func(got helmRelease) error {
		evidence := HelmEvidence{State: StateUncertain, Release: release, Namespace: namespace, Binding: binding}
		if got.Name == release && got.Namespace == namespace && got.Info.Description == binding && got.Version > 0 {
			evidence.Revision = got.Version
			switch got.Info.Status {
			case "deployed":
				evidence.State = StateSucceeded
			case "failed":
				evidence.State = StateFailed
			}
		}
		if err := json.NewEncoder(out).Encode(evidence); err != nil {
			return err
		}
		if evidence.State != StateSucceeded {
			return fmt.Errorf("deployment observation: %s", evidence.State)
		}
		return nil
	}
	before, beforeErr := observe()
	if mode == "observe" {
		return emit(before)
	}
	if beforeErr == nil && before.Info.Description == binding {
		// A delivery attempt is never submitted twice, even if it failed.
		return emit(before)
	}
	// A retry must not roll back a later deployment. If the previous attempt
	// cannot be identified, observation is required instead of a blind upgrade.
	if previous != "-" && (beforeErr != nil || before.Info.Description != previous || before.Info.Status != "failed") {
		return emit(helmRelease{})
	}
	argv := []string{"upgrade", "--install", release, chart, "--set-string", key + "=" + digest, "--description", binding, "--wait", "--wait-for-jobs", "--timeout", "240s"}
	got, upgradeErr := invoke(append(argv, common...)...)
	if upgradeErr == nil {
		return emit(got)
	}
	// A failed transport/process may still have applied the release. Only an
	// exact observed operation can classify the outcome; never trust exit 1 alone.
	got, _ = observe()
	return emit(got)
}
