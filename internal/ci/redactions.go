package ci

import (
	"sync"

	"github.com/google/uuid"

	"github.com/novaforge/novaforge/internal/redact"
)

// Redactions holds, per running job, the credential values that must never
// reach its log. The pump registers them when it dispatches the job, the log
// path masks them before a line is stored, and the job's terminal status
// forgets them.
//
// Values stay in memory, never in the retry journal. Replica changes or
// restarts lose this context, so the RPC path checks durable job classification
// and suppresses credential-bearing output rather than trusting an empty map.
type Redactions struct {
	mu   sync.Mutex
	jobs map[uuid.UUID]*redact.Redactor
}

// NewRedactions builds an empty registry.
func NewRedactions() *Redactions {
	return &Redactions{jobs: map[uuid.UUID]*redact.Redactor{}}
}

// Register records values to mask in jobID's log.
func (r *Redactions) Register(jobID uuid.UUID, values []string) {
	if r == nil || len(values) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.jobs[jobID] = redact.New(values)
}

// Line masks jobID's registered values in line.
func (r *Redactions) Line(jobID uuid.UUID, line string) string {
	if r == nil {
		return line
	}
	r.mu.Lock()
	red := r.jobs[jobID]
	r.mu.Unlock()
	return red.Line(line)
}

// Forget drops jobID's values once it can print nothing more.
func (r *Redactions) Forget(jobID uuid.UUID) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.jobs, jobID)
}

// SuppressedCredentialOutput is observable evidence of unavailable masking,
// never an empty successful log or an assertion that the supplied text was safe.
const SuppressedCredentialOutput = "[output suppressed: credential masking context unavailable]"

func (r *Redactions) mask(jobID uuid.UUID, line string, sensitive bool) string {
	if r == nil {
		if sensitive {
			return SuppressedCredentialOutput
		}
		return line
	}
	r.mu.Lock()
	red := r.jobs[jobID]
	r.mu.Unlock()
	if red == nil && sensitive {
		return SuppressedCredentialOutput
	}
	return red.Line(line)
}
