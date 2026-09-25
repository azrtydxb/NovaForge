package gates

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"k8s.io/apimachinery/pkg/util/validation"
)

var (
	ErrSandboxConflict   = errors.New("sandbox immutable evidence conflicts")
	ErrSandboxCapacity   = errors.New("sandbox outstanding obligation ceiling reached")
	ErrSandboxFenced     = errors.New("sandbox admission fenced for deletion")
	ErrSandboxTransition = errors.New("sandbox lifecycle transition refused")
	ErrSandboxClaimed    = errors.New("sandbox dispatch already claimed; replay forbidden")
)

// SandboxIntent carries no tenant-selected capacity or execution target. IDs must
// be allocated before reservation so a lost acknowledgement can be read back.
// Digests identify evidence; archives, commands and credentials are not stored.
type SandboxIntent struct {
	InvocationID   uuid.UUID
	AttemptID      uuid.UUID
	RunID          uuid.UUID
	RepoID         uuid.UUID
	Gate           string
	Tool           string
	SourceSHA      string
	PolicySHA      string
	SnapshotDigest string
	ImageDigest    string
	CommandDigest  string
	Container      string
	Containers     []string
}

// SandboxIdentity is the exact durable authority for every transition. The org
// comes from authz; target/namespace come from the journal's operator config.
type SandboxIdentity struct {
	SandboxIntent
	OrgID     uuid.UUID
	Target    string
	Namespace string
	PodName   string
}

// SandboxCommandDigest hashes JSON argv, not ambiguous space-joined text. The
// caller must hash the actual argv dispatched, including any shell wrapper.
func SandboxCommandDigest(argv []string) (string, error) {
	if len(argv) == 0 || argv[0] == "" {
		return "", ErrSandboxConflict
	}
	for _, arg := range argv {
		// JSON replaces invalid UTF-8 with U+FFFD, collapsing distinct argv
		// bytes to the same identity. Refuse those inputs before encoding.
		if !utf8.ValidString(arg) || strings.ContainsRune(arg, 0) {
			return "", ErrSandboxConflict
		}
	}
	b, err := json.Marshal(argv)
	if err != nil {
		return "", err
	}
	d := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(d[:]), nil
}

func sandboxDigest(s string) bool {
	if !strings.HasPrefix(s, "sha256:") || len(s) != 71 {
		return false
	}
	_, err := hex.DecodeString(s[7:])
	return err == nil && s == strings.ToLower(s)
}

func sandboxSHA(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil && s == strings.ToLower(s)
}

func (i SandboxIntent) validate() error {
	if i.InvocationID == uuid.Nil || i.AttemptID == uuid.Nil || i.RunID == uuid.Nil || i.RepoID == uuid.Nil ||
		!utf8.ValidString(i.Gate) || !utf8.ValidString(i.Tool) ||
		strings.TrimSpace(i.Gate) == "" || strings.TrimSpace(i.Tool) == "" ||
		!sandboxSHA(i.SourceSHA) || !sandboxSHA(i.PolicySHA) || !sandboxDigest(i.SnapshotDigest) ||
		!sandboxDigest(i.ImageDigest) || !sandboxDigest(i.CommandDigest) ||
		len(i.Containers) == 0 || !slices.Contains(i.Containers, i.Container) {
		return fmt.Errorf("invalid sandbox intent: %w", ErrSandboxConflict)
	}
	for n, name := range i.Containers {
		if len(validation.IsDNS1123Label(name)) != 0 || (n > 0 && i.Containers[n-1] >= name) {
			return fmt.Errorf("container set must be valid, sorted and unique: %w", ErrSandboxConflict)
		}
	}
	return nil
}

// Receipts are observations supplied by trusted executor/observer adapters, not
// proof manufactured by this store. The adapters must validate full pod identity
// and all containers at the Kubernetes boundary before persisting them.
type SandboxContainerTerminal struct {
	Name       string
	ExitCode   int32
	FinishedAt time.Time
}

type SandboxTerminalReceipt struct {
	PodUID     string
	Phase      string
	ObservedAt time.Time
	Containers []SandboxContainerTerminal
}

// SandboxCleanupReceipt attests an acknowledged UID-preconditioned Delete AFTER
// durable terminal observation. A timeout/NotFound before that is not a receipt.
type SandboxCleanupReceipt struct {
	PodUID         string
	DeleteID       uuid.UUID
	AcknowledgedAt time.Time
}

type SandboxAbsenceReceipt struct {
	PodUID     string
	DeleteID   uuid.UUID
	ObservedAt time.Time
}

// A result digest denotes captured tool output, never inferred pod success.
// Lifecycle recovery can settle an invocation without ever creating this receipt.
type SandboxToolResult struct {
	PodUID       string
	OutputDigest string
	ExitCode     int32
	ObservedAt   time.Time
}

type SandboxInvocation struct {
	Identity           SandboxIdentity
	CreateClaim        *uuid.UUID
	PodUID             *string
	ExecClaim          *uuid.UUID
	Terminal           *SandboxTerminalReceipt
	Cleanup            *SandboxCleanupReceipt
	Absence            *SandboxAbsenceReceipt
	ToolResult         *SandboxToolResult
	AcceptedEvaluation *uuid.UUID
	Released           bool
}
