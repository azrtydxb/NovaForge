package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/authz"
)

const StreamDeploymentSucceeded = "stream:deployment:succeeded"
const DeploymentEventWorker = "graph-deployment-events"

// DeploymentSuccessEvent mirrors delivery's SuccessEvent wire contract without
// importing its executor/credential implementation into this owning service.
// Delivery attests observation, not artifact-to-commit lineage or current state.
type DeploymentSuccessEvent struct {
	EvidenceID           string    `json:"evidence_id"`
	OrgID                uuid.UUID `json:"org_id"`
	RepoID               uuid.UUID `json:"repo_id"`
	OperationID          uuid.UUID `json:"operation_id"`
	RunID                uuid.UUID `json:"run_id"`
	Artifact             string    `json:"artifact"`
	Target               string    `json:"target"`
	Destination          string    `json:"destination"`
	TargetRevision       string    `json:"target_revision"`
	LatestExecuteAttempt int       `json:"latest_execute_attempt"`
	ExternalID           string    `json:"external_id"`
}

var observedArtifactDigest = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

func (e DeploymentSuccessEvent) Validate() error {
	if e.OrgID == uuid.Nil || e.RepoID == uuid.Nil || e.OperationID == uuid.Nil || e.RunID == uuid.Nil || e.LatestExecuteAttempt < 1 || e.EvidenceID != e.OperationID.String()+":"+strconv.Itoa(e.LatestExecuteAttempt) || !observedArtifactDigest.MatchString(e.Artifact) || e.Target == "" || e.Destination == "" || e.TargetRevision == "" || e.ExternalID == "" {
		return fmt.Errorf("invalid deployment success evidence")
	}
	return nil
}

// RecordDeploymentSuccess atomically projects immutable observed evidence under
// the same repository/org deletion fence as source indexing. A duplicate ID may
// repeat exact evidence, never retarget it. RunID is metadata, not a commit edge.
func (s *Store) RecordDeploymentSuccess(ctx context.Context, e DeploymentSuccessEvent) error {
	scope, err := authz.FromContext(ctx)
	if err != nil || scope.OrgID == uuid.Nil || scope.OrgID != e.OrgID || scope.ActorKind != "service" || scope.ActorID != uuid.Nil || scope.ServiceName != DeploymentEventWorker || scope.PlatformWorker != "" {
		return fmt.Errorf("deployment projection requires named organization worker")
	}
	if err = e.Validate(); err != nil {
		return err
	}
	attrs := map[string]string{"provenance": "observed", "producer": "deployment", "evidence_id": e.EvidenceID, "operation_id": e.OperationID.String(), "run_id": e.RunID.String(), "artifact": e.Artifact, "target": e.Target, "destination": e.Destination, "target_revision": e.TargetRevision, "latest_execute_attempt": strconv.Itoa(e.LatestExecuteAttempt), "external_id": e.ExternalID, "absence_safe": "false"}
	raw, err := json.Marshal(attrs)
	if err != nil {
		return err
	}
	if len(raw) > 64<<10 {
		return fmt.Errorf("deployment evidence exceeds 64KiB")
	}
	// Reserve opaque identity before projection, including events first received
	// after deletion. It deliberately survives purge or a failed graph write; it
	// confers no operational evidence. The unique row serializes competing repos.
	// Use the held index connection when present (and reject released sessions),
	// so a one-connection pool cannot deadlock while already holding a fence.
	q, err := s.indexReader(ctx, scope.OrgID, e.RepoID)
	if err != nil {
		return err
	}
	var originalRepo uuid.UUID
	if err = q.QueryRow(ctx, `INSERT INTO graph.deployment_evidence_identities(org_id,evidence_id,repo_id)
 VALUES($1,$2,$3) ON CONFLICT(org_id,evidence_id) DO UPDATE
 SET repo_id=deployment_evidence_identities.repo_id RETURNING repo_id`, scope.OrgID, e.EvidenceID, e.RepoID).Scan(&originalRepo); err != nil {
		return err
	}
	if originalRepo != e.RepoID {
		return fmt.Errorf("deployment evidence belongs to another repository")
	}
	tx, err := beginIndexWrite(ctx, s.pool, scope.OrgID, e.RepoID)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Do not use upsertNode: immutable event replay must not overwrite conflicting
	// evidence, including the same operation presented under another repository.
	key := "observed-deployment:" + e.EvidenceID
	if _, err = tx.Exec(ctx, `INSERT INTO graph.graph_nodes(id,org_id,repo_id,kind,key,attrs) VALUES($1,$2,$3,'deployment',$4,$5::jsonb) ON CONFLICT(org_id,kind,key) DO NOTHING`, uuid.New(), scope.OrgID, e.RepoID, key, raw); err != nil {
		return err
	}
	var deploymentID uuid.UUID
	var exact bool
	if err = tx.QueryRow(ctx, `SELECT id, repo_id=$3 AND attrs=$4::jsonb FROM graph.graph_nodes WHERE org_id=$1 AND kind='deployment' AND key=$2`, scope.OrgID, key, e.RepoID, raw).Scan(&deploymentID, &exact); err != nil {
		return err
	}
	if !exact {
		return fmt.Errorf("conflicting deployment evidence replay")
	}
	artifact, err := upsertNode(ctx, tx, scope.OrgID, e.RepoID, true, Node{OrgID: scope.OrgID, Kind: "artifact", Key: e.RepoID.String() + ":observed-artifact:" + e.Artifact, Attrs: map[string]string{"digest": e.Artifact, "provenance": "observed", "producer": "deployment", "absence_safe": "false"}})
	if err != nil {
		return err
	}
	if err = upsertEdge(ctx, tx, Edge{FromID: artifact.ID, ToID: deploymentID, Kind: "deployed_as"}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
