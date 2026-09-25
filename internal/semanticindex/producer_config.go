package semanticindex

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
)

// ProducerConfig is operator-owned. Repository contents cannot select an image,
// executable, command, environment, namespace, mount or network destination.
type ProducerConfig struct {
	Image           string `json:"image"`
	Architecture    string `json:"architecture"`
	CPUMilli        int64  `json:"cpu_milli"`
	MemoryMiB       int64  `json:"memory_mib"`
	StorageMiB      int64  `json:"storage_mib"`
	DeadlineSeconds int64  `json:"deadline_seconds"`
}

var immutableImage = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9./:_-]*@sha256:[a-f0-9]{64}$`)

func ParseProducerConfig(data []byte) (ProducerConfig, error) {
	var c ProducerConfig
	if len(data) > 16384 {
		return c, fmt.Errorf("semantic producer config exceeds 16KiB")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return c, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return c, fmt.Errorf("trailing producer configuration")
	}
	return c, c.Validate()
}
func (c ProducerConfig) Validate() error {
	if !immutableImage.MatchString(c.Image) {
		return fmt.Errorf("semantic producer requires an immutable image digest")
	}
	if c.Architecture != "arm64" && c.Architecture != "amd64" {
		return fmt.Errorf("unsupported producer architecture")
	}
	if c.CPUMilli < 100 || c.CPUMilli > 8000 || c.MemoryMiB < 256 || c.MemoryMiB > 8192 || c.StorageMiB < 128 || c.StorageMiB > 8192 || c.DeadlineSeconds < 30 || c.DeadlineSeconds > 600 {
		return fmt.Errorf("semantic producer resource bounds invalid")
	}
	return nil
}

const ProducerRoot = "file:///work/source"

// ExecutionManifest binds external build inputs as well as source. The immutable
// image includes dependencies/toolchains; no downloads are allowed at runtime.
func (c ProducerConfig) ExecutionManifest() ([]byte, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Contract    string
		Config      ProducerConfig
		Root        string
		Commands    map[string][]string
		Environment []string
	}{"semantic-producer-v1", c, ProducerRoot, producerCommands, producerEnvironment})
}
func (c ProducerConfig) ExecutionDigest() (string, error) {
	b, e := c.ExecutionManifest()
	if e != nil {
		return "", e
	}
	return digest(b), nil
}

var producerCommands = map[string][]string{
	"go":         {"scip-go", "index", "--repository-remote=repository", "--module-version=snapshot"},
	"typescript": {"scip-typescript", "index", "--no-progress-bar"},
	"python":     {"scip-python", "index", "--project-name", "repository", "--project-version", "snapshot", "--quiet"},
	"converter":  {"scip", "print", "--json", "/work/source/index.scip"},
	"lsp":        {"gopls", "serve"},
}
var producerEnvironment = []string{"PATH=/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin", "HOME=/work/home", "TMPDIR=/work/tmp", "GOCACHE=/work/cache", "GOMODCACHE=/work/modules", "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local", "CGO_ENABLED=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}

var producerTools = map[string]struct{ Name, Version string }{
	"go": {"scip-go", "0.2.7"}, "typescript": {"scip-typescript", "0.4.0"}, "python": {"scip-python", "0.6.6"},
}
