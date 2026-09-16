// Package swarm decomposes an epic Work Item into dependency-ordered
// subtasks across specialized agents (planner.go) and schedules Agent Runs
// for the ones that are ready (scheduler.go). No new service: it is a
// component of the work-reviews and agent-runtime services, operating on
// Work Items that already live in the work schema.
package swarm

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/azrtydxb/go-ai-sdk/ai"
	"github.com/azrtydxb/go-ai-sdk/provider"

	"github.com/novaforge/novaforge/internal/work"
)

// Bundle is the assembled context a planner hands the model when
// decomposing an epic: per-Work-Item retrieval across lexical, symbol,
// dependency, semantic, history, and test signals (section 17 of the
// spec), reranked and budget-bounded — never a whole-repository dump.
//
// internal/ctxasm — the package that would normally own this type — does
// not exist yet in this worktree, so Bundle is defined here, minimally, as
// the flattened text the planner actually needs: a short summary per
// relevant document. When ctxasm exists, this type should be replaced by a
// type alias to (or a thin adapter over) ctxasm.Bundle rather than kept as
// a second definition.
type Bundle struct {
	// Documents is assembled context, one entry per retrieved
	// document/snippet, already reranked and budget-bounded by whatever
	// assembled it. The planner treats each entry as opaque text.
	Documents []string
}

// Subtask is one unit of work a Decompose call proposes: a title and goal,
// its work item type, which specialized agent role should perform it, the
// keys of the subtasks it depends on (within the same decomposition), and
// its own key — a short, stable, decomposition-local identifier used to
// express DependsOn and to make Materialise idempotent.
type Subtask struct {
	Key       string   `json:"key"`
	Title     string   `json:"title"`
	Goal      string   `json:"goal"`
	Type      string   `json:"type" jsonschema:"enum=architecture|bug|documentation|feature|incident|refactor|research|security|tech_debt|upgrade"`
	AgentRole string   `json:"agentRole"`
	DependsOn []string `json:"dependsOn"`
}

// Planner decomposes epics into dependency-ordered subtasks and
// materialises them as child Work Items.
type Planner struct {
	Model provider.LanguageModel
	Work  *work.Store

	// KnownRoles is the set of agent roles a subtask's AgentRole may name —
	// normally every role declared in the repository's
	// .novaforge/agents configuration (repoconfig.Config.Agents[*].Role).
	// A subtask naming any other role is refused rather than materialised,
	// since there would be no agent able to pick it up.
	KnownRoles map[string]bool

	// ProviderOptions carries deployment-configured wire parameters for the
	// model server (see agentrun.ParseProviderOptions). A reasoning model
	// left thinking will spend the whole token budget before emitting the
	// structured decomposition, so this is what a deployment uses to turn
	// that off without NovaForge naming a provider.
	ProviderOptions map[string]any
}

// NewPlanner builds a Planner bound to model, store, and the set of agent
// roles the repository has configured.
func NewPlanner(model provider.LanguageModel, store *work.Store, knownRoles []string) *Planner {
	roles := make(map[string]bool, len(knownRoles))
	for _, r := range knownRoles {
		roles[r] = true
	}
	return &Planner{Model: model, Work: store, KnownRoles: roles}
}

// decomposeMaxTokens bounds the decomposition response. A reasoning model
// asked for structured output with no ceiling will happily spend its whole
// context window thinking, which turns a fifteen-second call into one that
// never returns inside any sane request deadline. A decomposition is a
// handful of short objects; this is several times what one needs.
const decomposeMaxTokens = 4096

// decomposeMaxTokensValue is decomposeMaxTokens as an addressable value,
// which is the shape go-ai-sdk's optional parameters take.
var decomposeMaxTokensValue = decomposeMaxTokens

// decomposeSystemPrompt instructs the model to decompose one epic into
// dependency-ordered subtasks assigned to specialized agent roles.
const decomposeSystemPrompt = `You are the NovaForge swarm planner. Decompose the given epic Work Item ` +
	`into a small set of dependency-ordered subtasks, each assigned to a specialized agent role. ` +
	`Each subtask needs a short stable "key" (lowercase, hyphenated, unique within this decomposition), ` +
	`a "title", a "goal" describing what must be true when it is done, a work item "type" ` +
	`(one of feature, bug, refactor, security, tech_debt, research, architecture, upgrade, incident, documentation), ` +
	`an "agentRole" naming who should do it, and "dependsOn", the keys of subtasks that must complete first. ` +
	`"type" and "agentRole" are different fields drawn from different lists: never put a role name in "type". ` +
	`Order subtasks so that database and schema work precedes the backend work built on it, ` +
	`backend work precedes admin configuration and frontend work that calls it, ` +
	`and documentation and integration tests come last, depending on everything they exercise.`

// Decompose asks the model to decompose epic into dependency-ordered
// subtasks, given the assembled context in bundle. The result is validated
// — every subtask's agent role must be known, and the dependency graph
// among the proposed subtasks (by key) must be acyclic — before it is
// returned; nothing is written to the database yet, that is Materialise's
// job.
func (p *Planner) Decompose(ctx context.Context, epic work.Item, bundle Bundle) ([]Subtask, error) {
	if p.Model == nil {
		return nil, fmt.Errorf("swarm: planner has no model configured")
	}

	prompt := buildDecomposePrompt(epic, bundle)
	result, err := ai.GenerateText(ctx, ai.GenerateTextOpts{
		Model:           p.Model,
		System:          p.systemPrompt(),
		Prompt:          prompt,
		Output:          ai.OutputArray[Subtask](),
		MaxTokens:       &decomposeMaxTokensValue,
		ProviderOptions: p.ProviderOptions,
	})
	if err != nil {
		return nil, fmt.Errorf("swarm: decompose epic %s: %w", epic.Key, err)
	}
	subs, err := ai.OutputAs[[]Subtask](result)
	if err != nil {
		return nil, fmt.Errorf("swarm: decode decomposition for epic %s: %w", epic.Key, err)
	}
	if len(subs) == 0 {
		return nil, fmt.Errorf("swarm: decomposition of epic %s produced no subtasks", epic.Key)
	}

	if err := p.validate(subs); err != nil {
		// One wrong field must not discard a decomposition that is otherwise
		// sound: the model is told exactly what it got wrong and asked once
		// more. A second failure is reported rather than retried forever.
		subs, err = p.retryDecompose(ctx, prompt, err)
		if err != nil {
			return nil, err
		}
	}
	if err := checkAcyclic(subs); err != nil {
		return nil, fmt.Errorf("swarm: decomposition of epic %s refused: %w", epic.Key, err)
	}

	return subs, nil
}

// systemPrompt is the instruction the model sees, with the two closed sets
// it must choose from spelled out. The model cannot pick a legal value
// unless it is told which values are legal, and both lists are read from
// the packages that own them rather than copied.
func (p *Planner) systemPrompt() string {
	return decomposeSystemPrompt +
		"\nThe \"type\" of every subtask must be exactly one of: " + strings.Join(work.SortedTypes(), ", ") + "." +
		"\nThe \"agentRole\" of every subtask must be exactly one of: " + p.allowedRoles() + "."
}

// retryDecompose asks once more, quoting the exact violation. A model that
// confuses two adjacent enumerations usually corrects itself when told
// which field was wrong; one retry bounds the cost of finding out.
func (p *Planner) retryDecompose(ctx context.Context, prompt string, cause error) ([]Subtask, error) {
	result, err := ai.GenerateText(ctx, ai.GenerateTextOpts{
		Model: p.Model,
		System: p.systemPrompt() +
			"\nYour previous answer was rejected: " + cause.Error() +
			"\nReturn the same decomposition with that corrected.",
		Prompt:          prompt,
		Output:          ai.OutputArray[Subtask](),
		MaxTokens:       &decomposeMaxTokensValue,
		ProviderOptions: p.ProviderOptions,
	})
	if err != nil {
		return nil, fmt.Errorf("swarm: retry after %v: %w", cause, err)
	}
	subs, err := ai.OutputAs[[]Subtask](result)
	if err != nil {
		return nil, fmt.Errorf("swarm: decode retried decomposition: %w", err)
	}
	if err := p.validate(subs); err != nil {
		return nil, err
	}
	return subs, nil
}

// validate rejects a decomposition naming an agent role or a Work Item type
// outside the legal sets. Types are checked here rather than left to
// Materialise's insert, because failing at insert time rejects the
// decomposition after some of its children have already been written.
func (p *Planner) validate(subs []Subtask) error {
	if err := p.validateRoles(subs); err != nil {
		return err
	}
	for _, s := range subs {
		if !work.IsValidType(s.Type) {
			return fmt.Errorf("swarm: subtask %q has type %q, which is not one of: %s",
				s.Key, s.Type, strings.Join(work.SortedTypes(), ", "))
		}
	}
	return nil
}

// buildDecomposePrompt renders epic and the assembled context bundle into
// the user-turn text sent to the model.
func buildDecomposePrompt(epic work.Item, bundle Bundle) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Epic %s: %s\n\nGoal: %s\n", epic.Key, epic.Type, epic.Goal)
	if len(epic.Acceptance) > 0 {
		b.WriteString("\nAcceptance criteria:\n")
		for _, a := range epic.Acceptance {
			fmt.Fprintf(&b, "- %s\n", a)
		}
	}
	if len(bundle.Documents) > 0 {
		b.WriteString("\nRelevant context:\n")
		for _, d := range bundle.Documents {
			fmt.Fprintf(&b, "---\n%s\n", d)
		}
	}
	return b.String()
}

// validateRoles rejects any subtask naming an agent role absent from
// p.KnownRoles. When KnownRoles is nil (no roles configured at all), every
// named role is unknown — a Planner with no configured roles cannot vouch
// for any of them.
func (p *Planner) validateRoles(subs []Subtask) error {
	for _, s := range subs {
		if !p.KnownRoles[s.AgentRole] {
			return fmt.Errorf("swarm: unknown agent role %q in subtask %q", s.AgentRole, s.Key)
		}
	}
	return nil
}

// checkAcyclic reports an error containing "cycle" if the DependsOn graph
// among subs (matched by Key) is not a DAG, and an error if any DependsOn
// entry names a key absent from subs. It performs a standard three-color
// depth-first search entirely in memory — no database round trip — so
// Decompose can refuse a cyclic decomposition before anything is written,
// and Materialise can re-check the same invariant before writing any row.
func checkAcyclic(subs []Subtask) error {
	byKey := make(map[string]Subtask, len(subs))
	for _, s := range subs {
		if s.Key == "" {
			return fmt.Errorf("subtask %q has no key", s.Title)
		}
		if _, dup := byKey[s.Key]; dup {
			return fmt.Errorf("duplicate subtask key %q", s.Key)
		}
		byKey[s.Key] = s
	}
	for _, s := range subs {
		for _, dep := range s.DependsOn {
			if _, ok := byKey[dep]; !ok {
				return fmt.Errorf("subtask %q depends on unknown key %q", s.Key, dep)
			}
		}
	}

	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make(map[string]int, len(subs))
	var visit func(key string) error
	visit = func(key string) error {
		switch color[key] {
		case gray:
			return fmt.Errorf("cycle detected at subtask %q", key)
		case black:
			return nil
		}
		color[key] = gray
		for _, dep := range byKey[key].DependsOn {
			if err := visit(dep); err != nil {
				return err
			}
		}
		color[key] = black
		return nil
	}
	for _, s := range subs {
		if err := visit(s.Key); err != nil {
			return err
		}
	}
	return nil
}

// Materialise writes subs as child Work Items of epic: one work.Store.CreateChild
// call per subtask, keyed on (epic.ID, subtask.Key) so calling Materialise
// twice with the same subtask keys yields one set of children, not two,
// followed by a work.Store.AddDependency call per DependsOn edge. The whole
// proposed dependency set is re-validated for cycles before any row is
// written — defense in depth alongside Decompose's own check, since
// Materialise can be called with a subs slice that did not come from
// Decompose (e.g. replayed from storage).
func (p *Planner) Materialise(ctx context.Context, epic work.Item, subs []Subtask) ([]work.Item, error) {
	if len(subs) == 0 {
		return nil, fmt.Errorf("swarm: materialise epic %s: no subtasks", epic.Key)
	}
	if err := checkAcyclic(subs); err != nil {
		return nil, fmt.Errorf("swarm: materialise epic %s refused: %w", epic.Key, err)
	}

	items := make([]work.Item, 0, len(subs))
	idByKey := make(map[string]work.Item, len(subs))
	for _, s := range subs {
		item, err := p.Work.CreateChild(ctx, epic.ID, s.Key, s.AgentRole, work.Item{
			RepoID: epic.RepoID,
			Type:   s.Type,
			Goal:   subtaskGoal(s),
		})
		if err != nil {
			return nil, fmt.Errorf("swarm: materialise subtask %q of epic %s: %w", s.Key, epic.Key, err)
		}
		items = append(items, item)
		idByKey[s.Key] = item
	}

	for _, s := range subs {
		for _, dep := range s.DependsOn {
			if err := p.Work.AddDependency(ctx, idByKey[s.Key].ID, idByKey[dep].ID); err != nil {
				return nil, fmt.Errorf("swarm: link subtask %q -> %q for epic %s: %w", s.Key, dep, epic.Key, err)
			}
		}
	}

	return items, nil
}

// subtaskGoal renders a subtask's title and goal into the single Goal
// string work.Item carries.
func subtaskGoal(s Subtask) string {
	if s.Title == "" {
		return s.Goal
	}
	if s.Goal == "" {
		return s.Title
	}
	return fmt.Sprintf("%s: %s", s.Title, s.Goal)
}

// DefaultAgentRoles is the role set a decomposition may draw on when the
// repository declares no agents of its own under .novaforge/agents. A
// Planner refuses any subtask naming a role it does not know, so without a
// fallback every epic in a repository that has not yet been configured
// would be undecomposable — which is exactly the repository most in need of
// being broken down. These six cover the phases the planner is asked to
// order: design, implementation, the three independent review dimensions,
// and the documentation that trails them.
var DefaultAgentRoles = []string{
	"architect",
	"implementer",
	"reviewer",
	"security",
	"test",
	"documentation",
}

// allowedRoles renders p.KnownRoles as a sorted, comma-separated list for
// the prompt. The model cannot pick a legal role unless it is told which
// roles are legal: leaving it to guess is what made validateRoles reject
// otherwise sound decompositions.
func (p *Planner) allowedRoles() string {
	roles := make([]string, 0, len(p.KnownRoles))
	for r := range p.KnownRoles {
		roles = append(roles, r)
	}
	sort.Strings(roles)
	return strings.Join(roles, ", ")
}
