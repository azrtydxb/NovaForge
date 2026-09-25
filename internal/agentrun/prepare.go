package agentrun

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	graphv1 "github.com/novaforge/novaforge/gen/novaforge/graph/v1"
	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
	"github.com/novaforge/novaforge/internal/agents"
	"github.com/novaforge/novaforge/internal/repoconfig"
)

// MaxBriefBytes bounds the opening brief. Context is assembled per Work Item
// and never dumped whole, and even assembled context is only worth the part
// of a model's window it leaves for the work itself.
const MaxBriefBytes = 32_000

// Bounds on the parts of the brief read straight from the repository.
const (
	contextTokenBudget  = 3_000
	maxContextDocs      = 12
	maxContextDocBytes  = 6_000
	maxSnippetBytes     = 2_000
	maxInstructionBytes = 4_000
)

// Services are the platform services a run is prepared from.
type Services struct {
	Git  gitv1.GitServiceClient
	Work workv1.WorkServiceClient
	// Graph assembles context and holds project knowledge. A deployment
	// without it prepares runs without either, and the brief says so.
	Graph graphv1.GraphServiceClient
}

// Plan is everything decided about a run before its first model turn.
type Plan struct {
	// Ref is the commit the repository's configuration was read at: the
	// default branch's head when the run was prepared.
	Ref    string
	Branch string

	// Agent is the repository's definition governing this agent, or nil.
	Agent *repoconfig.AgentDef
	// Model is the model the definition pins; "" means the deployment's.
	Model string
	// Tools is the complete set of tools to offer; nil means every tool.
	Tools []string

	WallclockLimit  time.Duration
	TokenLimit      int64
	CostLimitMicros int64

	Brief string
}

// Prepare reads what a run needs before its first turn: the repository's
// .novaforge configuration at the default branch's head, the Work Item, the
// repository's context documents, and context assembled for the Work Item —
// code, tests and recorded project knowledge.
//
// A configuration that cannot be read or parsed is an error, and the run must
// not start: running with no configuration would silently hand the agent
// every tool its repository took away. Context that cannot be assembled is
// not an error — a stale or unreachable index degrades the brief — but the
// brief says what is missing rather than letting its absence pass for "there
// was nothing relevant".
//
// Before this, repoconfig.Load had no production caller and AssembleContext
// had none either: a run's brief was the ids of its run, work item and
// repository, and nothing the repository or earlier runs knew reached it.
func Prepare(ctx context.Context, svc Services, run agents.Run, agent agents.Agent) (Plan, error) {
	plan := Plan{
		WallclockLimit:  run.WallclockLimit,
		TokenLimit:      run.TokenLimit,
		CostLimitMicros: run.CostLimitMicros,
	}
	var gaps []string

	repo, err := svc.Git.GetRepo(ctx, &gitv1.GetRepoRequest{Name: run.RepoID.String()})
	if err != nil {
		return Plan{}, fmt.Errorf("resolve repository %s: %w", run.RepoID, err)
	}
	plan.Branch = repo.GetRepo().GetDefaultBranch()
	plan.Ref = plan.Branch
	head, err := svc.Git.ListCommits(ctx, &gitv1.ListCommitsRequest{Repo: run.RepoID.String(), Ref: plan.Branch, Limit: 1})
	switch {
	case status.Code(err) == codes.NotFound:
		// An empty repository: nothing to configure, nothing to read.
	case err != nil:
		return Plan{}, fmt.Errorf("resolve %s's head: %w", plan.Branch, err)
	case len(head.GetCommits()) == 1:
		plan.Ref = head.GetCommits()[0].GetSha()
	}

	cfg, err := repoconfig.Load(ctx, svc.Git, run.OrgID, run.RepoID, plan.Ref)
	if err != nil {
		return Plan{}, err
	}
	if def := cfg.AgentFor(agent.Name, agent.Role); def != nil {
		plan.Agent = def
		plan.Model = def.Model
		plan.Tools = def.Tools
		plan.WallclockLimit = stricterDuration(plan.WallclockLimit, time.Duration(def.Budget.WallclockSeconds)*time.Second)
		plan.TokenLimit = stricter(plan.TokenLimit, def.Budget.Tokens)
		plan.CostLimitMicros = stricter(plan.CostLimitMicros, def.Budget.CostMicros)
	}

	var item *workv1.WorkItem
	if run.WorkClaimRequired || run.GrantID != uuid.Nil {
		item, err = run.FrozenWorkItem()
		if err != nil {
			return Plan{}, err
		}
	} else if run.WorkItemID != uuid.Nil {
		resp, err := svc.Work.GetItem(ctx, &workv1.GetItemRequest{Id: run.WorkItemID.String()})
		if err != nil {
			gaps = append(gaps, fmt.Sprintf("the work item could not be read (%v); read it with work.get", err))
		} else {
			item = resp.GetItem()
		}
	}

	var docs []contextDoc
	paths := append([]string(nil), cfg.ContextDocs...)
	sort.Strings(paths)
	for i, p := range paths {
		if i == maxContextDocs {
			gaps = append(gaps, fmt.Sprintf("%d further context documents under .novaforge/context were not included", len(paths)-maxContextDocs))
			break
		}
		text, err := repoconfig.ReadContextDoc(ctx, svc.Git, run.RepoID, plan.Ref, p)
		if err != nil {
			gaps = append(gaps, fmt.Sprintf("%s could not be read: %v", p, err))
			continue
		}
		docs = append(docs, contextDoc{path: p, text: text})
	}

	var bundle *graphv1.ContextBundle
	switch {
	case svc.Graph == nil:
		gaps = append(gaps, "this deployment has no engineering-graph service, so no code or project knowledge was assembled")
	case item == nil:
		gaps = append(gaps, "no context was assembled, because the work item could not be read")
	default:
		resp, err := svc.Graph.AssembleContext(ctx, &graphv1.AssembleContextRequest{
			RepoId: run.RepoID.String(), WorkItemKey: item.GetKey(), TokenBudget: contextTokenBudget,
		})
		if err != nil {
			gaps = append(gaps, fmt.Sprintf("context assembly failed (%v), so related code and recorded project knowledge are missing; repo.search and architecture.query can still find them", err))
		} else {
			bundle = resp.GetBundle()
		}
	}

	plan.Brief = renderBrief(run, plan, item, cfg.Project, docs, bundle, gaps)
	return plan, nil
}

// stricter combines a run's limit with a repository's: a repository sets a
// default for a run that has none, and may tighten a run's limit, but never
// loosen it. Zero means "not set" on either side.
func stricter(run, repo int64) int64 {
	if repo <= 0 {
		return run
	}
	if run <= 0 || repo < run {
		return repo
	}
	return run
}

func stricterDuration(run, repo time.Duration) time.Duration {
	return time.Duration(stricter(int64(run), int64(repo)))
}

type contextDoc struct {
	path string
	text string
}

// briefWriter appends sections to the brief until MaxBriefBytes is spent,
// truncating the section that crosses it. Sections are written most
// important first, so what is dropped is what matters least.
type briefWriter struct {
	b    strings.Builder
	full bool
}

func (w *briefWriter) write(s string) {
	if w.full {
		return
	}
	const marker = "\n[brief truncated at its size bound]\n"
	room := MaxBriefBytes - w.b.Len() - len(marker)
	if len(s) <= room {
		w.b.WriteString(s)
		return
	}
	if room > 0 {
		w.b.WriteString(truncateUTF8(s, room))
	}
	w.b.WriteString(marker)
	w.full = true
}

func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && n < len(s) && (s[n]&0xC0) == 0x80 {
		n--
	}
	return s[:n]
}

func clipBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return truncateUTF8(s, n) + fmt.Sprintf("\n[%d more bytes not shown]", len(s)-n)
}

// renderBrief writes the opening brief: what the run is for, then the
// repository's own configuration and context, then what earlier work
// decided, then the code assembled for this Work Item.
func renderBrief(run agents.Run, plan Plan, item *workv1.WorkItem, project repoconfig.Project, docs []contextDoc, bundle *graphv1.ContextBundle, gaps []string) string {
	w := &briefWriter{}
	w.write(OpeningBrief(run))
	if plan.Branch != "" {
		w.write(fmt.Sprintf("The workspace holds %s at %s.\n", plan.Branch, plan.Ref))
	}

	if item != nil {
		var s strings.Builder
		fmt.Fprintf(&s, "\n## Work item %s (%s)\nGoal: %s\n", item.GetKey(), item.GetType(), item.GetGoal())
		if len(item.GetAcceptance()) > 0 {
			s.WriteString("Acceptance criteria — the run is verified against these:\n")
			for _, a := range item.GetAcceptance() {
				fmt.Fprintf(&s, "- %s\n", a)
			}
		}
		if len(item.GetConstraints()) > 0 {
			s.WriteString("Constraints:\n")
			for _, c := range item.GetConstraints() {
				fmt.Fprintf(&s, "- %s\n", c)
			}
		}
		w.write(s.String())
	}

	if project.Name != "" || project.Description != "" {
		var s strings.Builder
		s.WriteString("\n## Project (.novaforge/project.yaml)\n")
		if project.Name != "" {
			fmt.Fprintf(&s, "Name: %s\n", project.Name)
		}
		if project.Description != "" {
			fmt.Fprintf(&s, "%s\n", strings.TrimSpace(project.Description))
		}
		w.write(s.String())
	}

	if plan.Agent != nil {
		var s strings.Builder
		fmt.Fprintf(&s, "\n## Your definition (%s)\n", plan.Agent.Path)
		if plan.Tools != nil {
			fmt.Fprintf(&s, "This repository allows you these tools only: %s.\n", strings.Join(plan.Tools, ", "))
		}
		if plan.Agent.Instructions != "" {
			fmt.Fprintf(&s, "%s\n", clipBytes(strings.TrimSpace(plan.Agent.Instructions), maxInstructionBytes))
		}
		w.write(s.String())
	}

	if bundle != nil && len(bundle.GetKnowledge()) > 0 {
		var s strings.Builder
		s.WriteString("\n## Project knowledge recorded by earlier work\n" +
			"Follow these unless the work item says otherwise; record a new decision with knowledge.record if you depart from one.\n")
		for _, k := range bundle.GetKnowledge() {
			fmt.Fprintf(&s, "- [%s] %s: %s\n", k.GetKind(), k.GetTitle(), clipBytes(strings.TrimSpace(k.GetBody()), maxSnippetBytes))
		}
		w.write(s.String())
	}

	for _, d := range docs {
		w.write(fmt.Sprintf("\n## Context document %s\n%s\n", d.path, clipBytes(strings.TrimSpace(d.text), maxContextDocBytes)))
	}

	if bundle != nil && len(bundle.GetFiles()) > 0 {
		w.write("\n## Code related to this work item\nAssembled for this work item from the repository's index — not the whole repository; read files with repo.read_file before changing them.\n")
		for _, f := range bundle.GetFiles() {
			loc := f.GetPath()
			if loc == "" {
				loc = "history"
			} else if f.GetStartLine() > 0 {
				loc = fmt.Sprintf("%s:%d-%d", loc, f.GetStartLine(), f.GetEndLine())
			}
			w.write(fmt.Sprintf("### %s (%s)\n%s\n", loc, f.GetSignal(), clipBytes(f.GetText(), maxSnippetBytes)))
		}
	}
	if bundle != nil && len(bundle.GetTests()) > 0 {
		w.write("\n## Tests covering related code\n- " + strings.Join(bundle.GetTests(), "\n- ") + "\n")
	}

	if len(gaps) > 0 {
		w.write("\n## Context that is missing\n- " + strings.Join(gaps, "\n- ") + "\n")
	}
	return w.b.String()
}
