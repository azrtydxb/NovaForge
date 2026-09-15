package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	graphv1 "github.com/novaforge/novaforge/gen/novaforge/graph/v1"
	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
)

// KnowledgeNote is one entry of project knowledge an agent records: a
// decision, a pattern, an incident, a correction, or an operational note.
type KnowledgeNote struct {
	Kind  string `json:"kind"`
	Title string `json:"title"`
	Body  string `json:"body"`
	// Key identifies the entry within the repository. Recording again under
	// the same key replaces the entry, which is how a decision is revised.
	Key string `json:"key,omitempty"`
}

// KnowledgeClient records project knowledge for the run's repository.
type KnowledgeClient interface {
	Record(ctx context.Context, note KnowledgeNote) (string, error)
}

// knowledgeKinds are the kinds the knowledge schema accepts.
var knowledgeKinds = map[string]bool{
	"decision": true, "pattern": true, "incident": true, "correction": true, "operational": true,
}

type knowledgeRecordResult struct {
	ID  string `json:"id"`
	Key string `json:"key"`
}

func registerKnowledgeTools(r *Registry) {
	r.Register("knowledge.record", knowledgeRecordHandler)
}

// knowledgeRecordHandler records what a run concluded as project knowledge,
// where a later run's context assembly finds it. A work.comment is part of
// one Work Item's discussion and is read by the people on that item; a
// decision recorded here outlives the item and reaches every later run the
// decision is relevant to.
func knowledgeRecordHandler(ctx context.Context, rt Runtime, argsJSON []byte) ([]byte, error) {
	var note KnowledgeNote
	if err := unmarshalArgs("knowledge.record", argsJSON, &note); err != nil {
		return nil, err
	}
	if rt.Knowledge == nil {
		return nil, fmt.Errorf("knowledge.record: this deployment has no engineering-graph service to record knowledge in")
	}
	note.Kind = strings.TrimSpace(note.Kind)
	note.Title = strings.TrimSpace(note.Title)
	note.Body = strings.TrimSpace(note.Body)
	if !knowledgeKinds[note.Kind] {
		return nil, fmt.Errorf("knowledge.record: kind %q is not one of decision, pattern, incident, correction, operational", note.Kind)
	}
	if note.Title == "" || note.Body == "" {
		return nil, fmt.Errorf("knowledge.record: a title and a body are both required")
	}
	if note.Key == "" {
		note.Key = KnowledgeKey(note.Kind, note.Title)
	}
	id, err := rt.Knowledge.Record(ctx, note)
	if err != nil {
		return nil, fmt.Errorf("knowledge.record: %w", err)
	}
	return json.Marshal(knowledgeRecordResult{ID: id, Key: note.Key})
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// KnowledgeKey derives an entry's key from its kind and title, so recording
// the same decision twice revises one entry instead of adding a second.
func KnowledgeKey(kind, title string) string {
	slug := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(title), "-"), "-")
	if len(slug) > 80 {
		slug = strings.TrimRight(slug[:80], "-")
	}
	if slug == "" {
		slug = "untitled"
	}
	return kind + "/" + slug
}

// NewKnowledgeClient records knowledge through the engineering-graph
// service for one run's repository, naming the run as the entry's source.
func NewKnowledgeClient(graph graphv1.GraphServiceClient, repoID, runID string) KnowledgeClient {
	if graph == nil {
		return nil
	}
	return &graphKnowledge{graph: graph, repoID: repoID, runID: runID}
}

type graphKnowledge struct {
	graph  graphv1.GraphServiceClient
	repoID string
	runID  string
}

func (g *graphKnowledge) Record(ctx context.Context, note KnowledgeNote) (string, error) {
	resp, err := g.graph.RecordKnowledge(ctx, &graphv1.RecordKnowledgeRequest{
		RepoId:      g.repoID,
		Key:         note.Key,
		Kind:        note.Kind,
		Title:       note.Title,
		Body:        note.Body,
		SourceRunId: g.runID,
	})
	if err != nil {
		return "", err
	}
	return resp.GetId(), nil
}

// NewWorkClient satisfies WorkClient over the work service's gRPC client.
// work.get and work.comment are both answered by WorkService RPCs; a comment
// an agent writes lands on the Work Item's own thread, attributed to the
// agent, which is where a reader looks for it.
func NewWorkClient(work workv1.WorkServiceClient) WorkClient {
	return &grpcWork{work: work}
}

type grpcWork struct {
	work workv1.WorkServiceClient
}

func (a *grpcWork) Get(ctx context.Context, workItemID string) (WorkItemSummary, error) {
	resp, err := a.work.GetItem(ctx, &workv1.GetItemRequest{Id: workItemID})
	if err != nil {
		return WorkItemSummary{}, fmt.Errorf("get work item %s: %w", workItemID, err)
	}
	item := resp.GetItem()
	return WorkItemSummary{
		ID:          item.GetId(),
		Key:         item.GetKey(),
		Type:        item.GetType(),
		Goal:        item.GetGoal(),
		State:       item.GetState(),
		Acceptance:  item.GetAcceptance(),
		Constraints: item.GetConstraints(),
	}, nil
}

func (a *grpcWork) Comment(ctx context.Context, workItemID, body string) error {
	if _, err := a.work.AddComment(ctx, &workv1.AddCommentRequest{WorkItemId: workItemID, Body: body}); err != nil {
		return fmt.Errorf("comment on work item %s: %w", workItemID, err)
	}
	return nil
}
