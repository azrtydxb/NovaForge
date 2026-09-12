package runner

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"path"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
)

// Artifact is one file collected from a finished job.
type Artifact struct {
	Name    string
	Content []byte
}

// CollectArtifacts copies the declared paths out of the job's pod.
//
// The job runs isolated, so its output cannot simply be read from disk: the
// files are tarred inside the pod and streamed out over the exec API, which is
// what kubectl cp does. Only declared paths are taken — collecting everything a
// job wrote would ship its whole working tree, credentials included.
func (p *PodExecutor) CollectArtifacts(ctx context.Context, podName string, paths []string) ([]Artifact, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	if p.RestConfig == nil {
		return nil, fmt.Errorf("no kubernetes rest config, cannot collect artifacts")
	}

	// -C the checkout so names come back relative, and ignore a missing path
	// rather than failing the whole collection for one absent file.
	args := []string{"tar", "cf", "-", "-C", "/workspace/repo"}
	args = append(args, paths...)

	req := p.Client.CoreV1().RESTClient().Post().
		Resource("pods").Name(podName).Namespace(p.Namespace).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Command: args,
			Stdout:  true,
			Stderr:  true,
		}, scheme.ParameterCodec)

	exec, err := remotecommand.NewSPDYExecutor(p.RestConfig, "POST", req.URL())
	if err != nil {
		return nil, fmt.Errorf("prepare artifact copy: %w", err)
	}
	var stdout, stderr bytes.Buffer
	if err := exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: &stdout, Stderr: &stderr,
	}); err != nil {
		return nil, fmt.Errorf("copy artifacts: %w — %s", err, strings.TrimSpace(stderr.String()))
	}
	return readTar(&stdout)
}

func readTar(r io.Reader) ([]Artifact, error) {
	tr := tar.NewReader(r)
	var out []Artifact
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return out, fmt.Errorf("read artifact archive: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			return out, fmt.Errorf("read artifact %s: %w", hdr.Name, err)
		}
		out = append(out, Artifact{Name: path.Base(hdr.Name), Content: body})
	}
}
