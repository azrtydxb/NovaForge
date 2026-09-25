package reviews

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// ReviewConfig is operator-owned: callers may select neither models nor limits.
// Concurrency is per organization, enforced in PostgreSQL across replicas.
type ReviewConfig struct {
	// Explicit owner selection is required by startup preflight; the ordinary
	// inference endpoint does not imply managed execution support.
	ExecutionOwnerURL     string `json:"execution_owner_url"`
	WallclockSeconds      int    `json:"wallclock_seconds"`
	MaxInputBytes         int    `json:"max_input_bytes"`
	MaxOutputTokens       int    `json:"max_output_tokens"`
	MaxConcurrentRequests int    `json:"max_concurrent_requests"`
	MaxRoles              int    `json:"max_roles"`
}

func ParseReviewConfig(raw []byte) (ReviewConfig, error) {
	var c ReviewConfig
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return c, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return c, fmt.Errorf("one review config object required")
	}
	return c, c.Validate()
}
func (c ReviewConfig) Validate() error {
	if c.WallclockSeconds < 1 || c.WallclockSeconds > 600 || c.MaxInputBytes < 1 || c.MaxInputBytes > 1<<20 || c.MaxOutputTokens < 1 || c.MaxOutputTokens > 16384 || c.MaxConcurrentRequests < 1 || c.MaxConcurrentRequests > 16 || c.MaxRoles < 1 || c.MaxRoles > 4 {
		return fmt.Errorf("review limits must be positive and within wallclock600s/input1MiB/output16384/concurrency16/roles4")
	}
	return nil
}
