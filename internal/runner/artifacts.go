package runner

import (
	"archive/tar"
	"bytes"
	"context"
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
// its declared artifacts. Missing paths or archive errors fail the job rather
// than silently dropping evidence. COPYFILE_DISABLE stops a
// BSD tar (a macOS runner host) adding an AppleDouble "._name" entry per file,
// which arrived as a second, unreadable artifact beside each real one.
func artifactCaptureScript(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	var quoted []string
	for _, p := range paths {
		quoted = append(quoted, "'"+strings.ReplaceAll(p, "'", `'\''`)+"'")
	}
	// Positional parameters preserve spaces and glob characters. Do not pipe
	// tar into base64: POSIX sh reports only the last pipeline command's status,
	// hiding a failed archive behind successful encoding of incomplete output.
	return fmt.Sprintf(`
__nf_status=$?
[ "$__nf_status" -eq 0 ] || exit "$__nf_status"
(
  set -e
  set -- %s
  for __nf_p do
    if [ ! -e "$__nf_p" ]; then
      echo "novaforge: missing declared artifact: $__nf_p" >&2
      exit 1
    fi
  done
  __nf_archive=$(mktemp)
  trap 'rm -f "$__nf_archive"' EXIT
  COPYFILE_DISABLE=1 tar cf - -- "$@" > "$__nf_archive"
  echo %q
  base64 < "$__nf_archive"
  echo
  echo %q
)
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
	if inBlock {
		return nil, clean, fmt.Errorf("artifact payload ended before its closing marker")
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

// filterOutput reads a job's raw output from raw until it is closed, sends
// every ordinary line to logs the moment it arrives, and returns the artifacts
// carried between the capture markers.
//
// Only the marked block is held back. The first version collected the whole
// output and forwarded it after the stream closed, which kept artifacts out of
// the log but also kept every line out of it until the job had ended — a live
// log that could only ever be read afterwards.
func filterOutput(ctx context.Context, raw <-chan string, logs chan<- string) ([]Artifact, error) {
	var (
		payload  strings.Builder
		inBlock  bool
		overflow bool
		stopped  bool
	)
	for line := range raw {
		// Once ctx is done the rest is drained, never forwarded, so the
		// producer is not left blocked on a send nobody receives.
		if stopped {
			continue
		}
		switch {
		case line == artifactsBegin:
			inBlock = true
		case line == artifactsEnd:
			inBlock = false
		case inBlock:
			if payload.Len()+len(line) > maxArtifactPayload {
				overflow = true
				continue
			}
			payload.WriteString(line)
		default:
			select {
			case logs <- line:
			case <-ctx.Done():
				stopped = true
			}
		}
	}
	if stopped {
		return nil, ctx.Err()
	}
	if overflow {
		return nil, fmt.Errorf("artifact payload exceeds %d bytes", maxArtifactPayload)
	}
	if inBlock {
		return nil, fmt.Errorf("artifact payload ended before its closing marker")
	}
	if payload.Len() == 0 {
		return nil, nil
	}
	decoded, err := base64.StdEncoding.DecodeString(payload.String())
	if err != nil {
		return nil, fmt.Errorf("decode artifact payload: %w", err)
	}
	return readTar(bytes.NewReader(decoded))
}

// FilterOutputForTest exposes filterOutput to the package's external tests.
func FilterOutputForTest(ctx context.Context, raw <-chan string, logs chan<- string) ([]Artifact, error) {
	return filterOutput(ctx, raw, logs)
}
