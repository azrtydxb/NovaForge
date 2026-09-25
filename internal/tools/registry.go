// Package tools implements the sixteen typed agent tools: the audited
// surface agents call instead of shell access. Every call is bounded by the
// run's budget, recorded in the append-only audit log before it executes,
// and authorized against the run's capability grant.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/novaforge/novaforge/internal/agents"
	"github.com/novaforge/novaforge/internal/capability"
)

// Handler executes one tool call against rt with the raw JSON arguments the
// model supplied, and returns the raw JSON result to feed back to the
// model.
type Handler func(ctx context.Context, rt Runtime, argsJSON []byte) ([]byte, error)

// CapCheck authorizes one tool call against rt before its Handler runs. Most
// tools need no capability restriction beyond the generic grant on rt and
// register with a nil CapCheck; git.commit is the concrete example the spec
// calls out: it refuses an out-of-scope branch through capability.CanWriteRef,
// identically to the git transport.
type CapCheck func(rt Runtime, argsJSON []byte) error

// GitClient is the subset of the git-platform service's capabilities the
// repo.* and git.* tools need. It is a hand-written interface rather than
// the generated gRPC client because the generated GitService (see
// proto/novaforge/git/v1/git.proto, which this package does not own) has no
// search, symbol, dependency, or commit-creation RPCs yet; a concrete
// adapter satisfying GitClient — backed by that gRPC client plus whatever
// additional RPCs the git-platform service later grows — is wired in at
// agent-runtime service composition, outside this package's scope.
type GitClient interface {
	Search(ctx context.Context, repo, query string) ([]SearchHit, error)
	ReadFile(ctx context.Context, repo, ref, path string) ([]byte, error)
	GetSymbol(ctx context.Context, repo, ref, symbol string) (SymbolInfo, error)
	GetDependencies(ctx context.Context, repo, ref, path string) ([]string, error)
	Diff(ctx context.Context, repo, from, to string) (string, error)
	Commit(ctx context.Context, repo, branch, message string, files map[string]string) (string, error)
}

// WorkClient is the subset of the work service's API the work.* tools need.
type WorkClient interface {
	Get(ctx context.Context, workItemID string) (WorkItemSummary, error)
	Comment(ctx context.Context, workItemID, body string) error
}

// CIClient is the subset of the ci-runner service's API the ci.* tools
// need.
type CIClient interface {
	RunTest(ctx context.Context, repo, ref, suite string) (string, error)
	GetLogs(ctx context.Context, jobID string) (string, error)
}

// ReviewsClient is the subset of the work-reviews service's API the
// gate.status tool needs: Engineering Runs own per-gate proof (see
// backend-platform.md S-5), so gate status is read through the reviews
// service rather than the gates service directly.
type ReviewsClient interface {
	GateStatus(ctx context.Context, runID string) (map[string]string, error)
}

// GraphClient is the subset of the engineering-graph service's API the
// architecture.query tool needs.
type GraphClient interface {
	Query(ctx context.Context, query string) ([]string, error)
}

// Runtime carries everything a Handler needs to act on behalf of one agent
// run: its capability grant, its budget, its workspace, and the clients for
// the services the tools translate onto.
type Runtime struct {
	RunID     uuid.UUID
	Grant     capability.Grant
	Budget    *agents.Budget
	Workspace Workspace

	// staged is shared by every copy of the Runtime a Registry hands its
	// handlers, so git.commit sees what workspace.write_file staged.
	staged *stagedFiles

	Git     GitClient
	Work    WorkClient
	CI      CIClient
	Reviews ReviewsClient
	Graph   GraphClient
	// Knowledge records what a run concluded as project knowledge.
	Knowledge KnowledgeClient
}

type toolEntry struct {
	handler  Handler
	capCheck CapCheck
}

// Registry dispatches tool calls for one agent run: Call fixes the order
// budget check, audit Record, capability check, handler, audit Complete —
// a handler is never reached when an earlier step fails.
type Registry struct {
	rt    Runtime
	audit *agents.AuditLog
	tools map[string]toolEntry
	specs map[string]Spec
}

// NewRegistry builds a Registry bound to rt and audit, and registers the
// sixteen built-in tools.
func NewRegistry(rt Runtime, audit *agents.AuditLog) *Registry {
	rt.staged = &stagedFiles{paths: make(map[string]struct{})}
	r := &Registry{rt: rt, audit: audit, tools: make(map[string]toolEntry)}
	registerRepoTools(r)
	registerWorkspaceTools(r)
	registerGitTools(r)
	registerCITools(r)
	registerWorkTools(r)
	registerKnowledgeTools(r)
	return r
}

// Restrict removes every tool not named in allowed, so it is neither offered
// to the model nor dispatchable: a call to a removed tool is refused as an
// unknown tool before anything is audited or executed. A nil allowed leaves
// the registry unrestricted; an empty, non-nil one removes every tool.
//
// This is how a repository's .novaforge/agents definition governs what an
// agent may do. Offering a tool the definition leaves out, even one whose
// call would later be refused, invites the model to plan around it.
func (r *Registry) Restrict(allowed []string) {
	if allowed == nil {
		return
	}
	keep := make(map[string]bool, len(allowed))
	for _, name := range allowed {
		keep[name] = true
	}
	for name := range r.tools {
		if !keep[name] {
			delete(r.tools, name)
		}
	}
}

// Register adds a tool with no capability restriction beyond the generic
// grant on the Registry's Runtime. It exists mainly for tests exercising
// the dispatch pipeline in isolation.
func (r *Registry) Register(name string, h Handler) {
	r.tools[name] = toolEntry{handler: h}
}

// registerWithCap adds a tool whose call must additionally pass capCheck
// before its handler runs. It is unexported: the built-in tools that need a
// capability restriction (currently only git.commit) register through it
// from within this package; Register is the public, unrestricted form.
func (r *Registry) registerWithCap(name string, capCheck CapCheck, h Handler) {
	r.tools[name] = toolEntry{handler: h, capCheck: capCheck}
}

// registerExternal adds a tool whose description and schema come from outside
// this package — an external MCP server's — so the model is told what that
// server said the tool takes. It is dispatched by Call like every other tool:
// budget, audit, handler, audit.
func (r *Registry) registerExternal(name string, spec Spec, h Handler) {
	r.tools[name] = toolEntry{handler: h}
	if r.specs == nil {
		r.specs = make(map[string]Spec)
	}
	r.specs[name] = spec
}

// Spec is what the model is told about name: an external tool's own
// description and schema, or the built-in entry in Specs.
func (r *Registry) Spec(name string) Spec {
	if s, ok := r.specs[name]; ok {
		return s
	}
	return SpecFor(name)
}

// KnownToolNames returns the registered registered tool names, sorted,
// without requiring a live Runtime or audit log. It exists for validating
// configuration — see internal/repoconfig, which checks a repository's
// .novaforge/agents/*.yaml tool lists against it — against the tools the
// platform actually implements, without those callers needing to assemble
// a real dispatchable Registry.
func KnownToolNames() []string {
	return NewRegistry(Runtime{}, nil).Names()
}

// Names returns every registered tool name, sorted.
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Audit outcomes a tool call can end with.
const (
	// OutcomeOK: the handler ran and returned a result.
	OutcomeOK = "ok"
	// OutcomeError: the handler ran and failed.
	OutcomeError = "error"
	// OutcomeDenied: the run's capability grant refused the call; the
	// handler never ran.
	OutcomeDenied = "denied"
	// OutcomeRefused: the call could not be dispatched at all — the tool does
	// not exist, or the run's budget was already spent.
	OutcomeRefused = "refused"
)

// Call dispatches name for runID with argsJSON, in the fixed order: audit
// Record, tool lookup, budget check, capability check, handler, audit
// Complete. Any failing step stops the pipeline before the next one runs, so
// a handler is never reached when the tool is unknown, the budget is
// exhausted or the capability check refuses the call.
//
// Recording comes first. Every call the model asks for is a call the agent
// made, and the log is only "every call" if a refused one is in it too: an
// unknown tool and a call over budget used to return before anything was
// written, so a run that asked for a shell read exactly like one that never
// did.
func (r *Registry) Call(ctx context.Context, runID uuid.UUID, name string, argsJSON []byte) ([]byte, error) {
	if r.audit == nil {
		return nil, ErrAuditUnavailable
	}
	auditName := name
	if _, ok := r.tools[name]; !ok {
		auditName = "unregistered"
		for _, known := range KnownToolNames() {
			if known == name {
				auditName = name
				break
			}
		}
	}
	recordCtx, stopRecord := context.WithTimeout(ctx, 2*time.Second)
	defer stopRecord()
	// A built-in tool's arguments are described by Specs, so which of them are
	// identifiers and which are content is known; an external server's are not.
	_, builtin := Specs[auditName]
	auditID, err := r.audit.Record(recordCtx, agents.Entry{
		RunID:       runID,
		Tool:        auditName,
		ArgsJSON:    AuditArgs(auditName, builtin, normalizeArgs(argsJSON)),
		ArgsAudited: true,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: start receipt unavailable", ErrAuditUnavailable)
	}
	// The outcome is written even when the run's context has been cancelled
	// part-way through the call: under that context the write failed, and the
	// calls of exactly the runs someone chose to stop stayed "pending".
	complete := func(outcome, errText string) error {
		var err error
		for attempt := 0; attempt < 3; attempt++ {
			writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 500*time.Millisecond)
			err = r.audit.Complete(writeCtx, auditID, outcome, errText)
			cancel()
			if err == nil {
				return nil
			}
		}
		return ErrAuditUnavailable
	}
	fail := func(outcome string, callErr error) ([]byte, error) {
		if err := complete(outcome, callErr.Error()); err != nil {
			return nil, errors.Join(callErr, ErrAuditUnavailable)
		}
		return nil, callErr
	}

	entry, ok := r.tools[name]
	if !ok {
		return fail(OutcomeRefused, fmt.Errorf("unknown tool %q", name))
	}

	if r.rt.Budget != nil {
		if err := r.rt.Budget.Check(); err != nil {
			return fail(OutcomeRefused, err)
		}
	}

	if entry.capCheck != nil {
		if err := entry.capCheck(r.rt, argsJSON); err != nil {
			return fail(OutcomeDenied, err)
		}
	}

	result, err := entry.handler(ctx, r.rt, argsJSON)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return fail("cancelled", err)
		}
		return fail(OutcomeError, err)
	}

	if err := complete(OutcomeOK, ""); err != nil {
		return nil, fmt.Errorf("audit complete: %w", err)
	}
	return result, nil
}

var ErrAuditUnavailable = errors.New("tool audit persistence unavailable")

// normalizeArgs stores an explicit JSON null (rather than an empty byte
// slice, which is not valid JSON) when a tool call carries no arguments.
func normalizeArgs(argsJSON []byte) []byte {
	if len(argsJSON) == 0 {
		return []byte("null")
	}
	return argsJSON
}

// unmarshalArgs decodes argsJSON into v, wrapping any error with the tool
// name for a clearer audit trail.
func unmarshalArgs(tool string, argsJSON []byte, v any) error {
	if len(argsJSON) == 0 {
		return nil
	}
	if err := json.Unmarshal(argsJSON, v); err != nil {
		return fmt.Errorf("%s: invalid arguments: %w", tool, err)
	}
	return nil
}
