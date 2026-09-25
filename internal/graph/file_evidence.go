package graph

import (
	"crypto/sha256"
	"fmt"
)

// FileEvidence binds parsed graph data to exact source and nearest module bytes.
// Missing evidence (including legacy rows) cannot support absence-based scans.
// Complete means extraction succeeded, not that static references capture all
// dynamic behavior or external consumers.
type FileEvidence struct {
	ContentHash string
	ModuleHash  string
	// ModulePath is the literal repository-relative nearest ancestor go.mod;
	// empty means known absent. Hash alone cannot detect a moved go.mod.
	ModulePath string
	Complete   bool
}

// SourceDigest fingerprints bytes without storing another copy of source code.
func SourceDigest(content []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(content))
}
