package graph

import (
	"crypto/sha256"
	"fmt"
)

// FileEvidence binds parsed graph data to exact source and root module bytes.
// Missing evidence (including legacy rows) cannot support absence-based scans.
// Complete means extraction succeeded, not that static references capture all
// dynamic behavior or external consumers.
type FileEvidence struct {
	ContentHash string
	ModuleHash  string
	Complete    bool
}

// SourceDigest fingerprints bytes without storing another copy of source code.
func SourceDigest(content []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(content))
}
