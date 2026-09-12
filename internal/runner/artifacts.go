package runner

import (
	"archive/tar"
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"path"
	"strings"
)

// Artifact is one file collected from a finished job.
type Artifact struct {
	Name    string
	Content []byte
}

// Artifacts are emitted by the job itself, between these markers, as a
// base64-encoded tar on stdout.
//
// The obvious approach — exec into the pod afterwards and tar the files out,
// the way kubectl cp works — cannot work here: by the time a job has finished
// its container has terminated, and there is nothing left to exec into. So the
// job captures its own declared outputs as its last act, while it is still
// alive, and the runner lifts them out of the stream it is already reading.
const (
	artifactsBegin = "::novaforge-artifacts-begin::"
	artifactsEnd   = "::novaforge-artifacts-end::"
)

// maxArtifactPayload caps the encoded blob a job may emit. Artifacts travel
// through the log stream, so an unbounded one would be a way to flood it.
const maxArtifactPayload = 48 << 20

// artifactCaptureScript returns the shell appended to a job's command to emit
// its declared artifacts. Missing paths are skipped rather than failing the
// job, which has already succeeded by this point.
func artifactCaptureScript(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	var quoted []string
	for _, p := range paths {
		quoted = append(quoted, "'"+strings.ReplaceAll(p, "'", `'\''`)+"'")
	}
	return fmt.Sprintf(`
__nf_existing=""
for __nf_p in %s; do
  if [ -e "$__nf_p" ]; then __nf_existing="$__nf_existing $__nf_p"; fi
done
if [ -n "$__nf_existing" ]; then
  echo %q
  tar cf - $__nf_existing | base64 | tr -d '\n'
  echo
  echo %q
fi
`, strings.Join(quoted, " "), artifactsBegin, artifactsEnd)
}

// extractArtifacts pulls the encoded payload out of a job's output lines and
// returns the files it carries, plus the lines with the payload removed so a
// reader of the log never sees the blob.
func extractArtifacts(lines []string) ([]Artifact, []string, error) {
	var (
		clean   []string
		payload strings.Builder
		inBlock bool
	)
	for _, l := range lines {
		switch {
		case l == artifactsBegin:
			inBlock = true
		case l == artifactsEnd:
			inBlock = false
		case inBlock:
			if payload.Len()+len(l) > maxArtifactPayload {
				return nil, clean, fmt.Errorf("artifact payload exceeds %d bytes", maxArtifactPayload)
			}
			payload.WriteString(l)
		default:
			clean = append(clean, l)
		}
	}
	if payload.Len() == 0 {
		return nil, clean, nil
	}
	raw, err := base64.StdEncoding.DecodeString(payload.String())
	if err != nil {
		return nil, clean, fmt.Errorf("decode artifact payload: %w", err)
	}
	arts, err := readTar(bytes.NewReader(raw))
	return arts, clean, err
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

// ExtractArtifactsForTest and ArtifactCaptureScriptForTest expose the two
// halves of artifact capture to the package's external tests, which exercise
// the round trip without needing a cluster.
func ExtractArtifactsForTest(lines []string) ([]Artifact, []string, error) {
	return extractArtifacts(lines)
}

// ArtifactCaptureScriptForTest returns the shell appended to a job's command.
func ArtifactCaptureScriptForTest(paths []string) string { return artifactCaptureScript(paths) }
