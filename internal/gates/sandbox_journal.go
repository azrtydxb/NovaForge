package gates

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/novaforge/novaforge/internal/authz"
)

// SandboxJournal is a store slice, not an executor. Only trusted startup may
// construct it. Every instance for a target must use identical operator config;
// there is no request-level target/ceiling override or runtime reconfiguration.
type SandboxJournal struct {
	pool      *pgxpool.Pool
	target    string
	namespace string
	ceiling   int
}

// NewSandboxJournal installs or verifies operator-only pool metadata. It must not
// be exposed as a tenant configuration API. Startup must separately qualify the
// dedicated namespace/security bundle and retain old target recovery mappings.
func NewSandboxJournal(ctx context.Context, pool *pgxpool.Pool, target, namespace string, ceiling int) (*SandboxJournal, error) {
	if pool == nil || strings.TrimSpace(target) == "" || len(validation.IsDNS1123Label(namespace)) != 0 || namespace == "default" || strings.HasPrefix(namespace, "kube-") || ceiling <= 0 {
		return nil, fmt.Errorf("invalid sandbox operator configuration")
	}
	s := &SandboxJournal{pool: pool, target: target, namespace: namespace, ceiling: ceiling}
	tx, err := beginSandboxJournalTx(ctx, pool)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.Background())
	// Operator-only metadata exception: no tenant rows, identifiers or payloads.
	_, err = tx.Exec(ctx, `INSERT INTO gates.sandbox_capacity (target, namespace, ceiling) VALUES ($1,$2,$3) ON CONFLICT (target) DO NOTHING`, target, namespace, ceiling)
	if err != nil {
		return nil, err
	}
	if _, err = s.lockCapacity(ctx, tx); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// lockCapacity touches only operator resource metadata. Never reconstruct this
// aggregate by reading/counting another organization's invocation records.
func (s *SandboxJournal) lockCapacity(ctx context.Context, tx pgx.Tx) (int, error) {
	var namespace string
	var ceiling, reserved int
	err := tx.QueryRow(ctx, `SELECT namespace, ceiling, reserved FROM gates.sandbox_capacity WHERE target=$1 FOR UPDATE`, s.target).Scan(&namespace, &ceiling, &reserved)
	if err != nil {
		return 0, err
	}
	if namespace != s.namespace || ceiling != s.ceiling {
		return 0, ErrSandboxConflict
	}
	return reserved, nil
}

func sandboxOrg(ctx context.Context) (uuid.UUID, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	if scope.OrgID == uuid.Nil {
		return uuid.Nil, errors.New("sandbox journal requires organization scope")
	}
	return scope.OrgID, nil
}

func lockSandboxOrg(ctx context.Context, tx pgx.Tx, org uuid.UUID) (bool, error) {
	_, err := tx.Exec(ctx, `INSERT INTO gates.sandbox_org_fences (org_id) VALUES ($1) ON CONFLICT (org_id) DO NOTHING`, org)
	if err != nil {
		return false, err
	}
	var deleting bool
	err = tx.QueryRow(ctx, `SELECT deleting FROM gates.sandbox_org_fences WHERE org_id=$1 FOR UPDATE`, org).Scan(&deleting)
	return deleting, err
}

const sandboxSelect = `SELECT identity, create_claim, pod_uid, exec_claim, terminal, cleanup, absence, tool_result, accepted_evaluation, released FROM gates.sandbox_invocations WHERE org_id=$1 AND id=$2`

func scanSandbox(row pgx.Row) (SandboxInvocation, error) {
	var v SandboxInvocation
	err := row.Scan(&v.Identity, &v.CreateClaim, &v.PodUID, &v.ExecClaim, &v.Terminal, &v.Cleanup, &v.Absence, &v.ToolResult, &v.AcceptedEvaluation, &v.Released)
	return v, err
}

// Reserve persists intent and charges capacity in one transaction. Exact retries
// read back the original record (including after a deletion fence); they never
// imply permission to dispatch. Any error, including ambiguous Commit, forbids
// effects until exact readback. Pod names are derived from never-reused IDs.
func (s *SandboxJournal) Reserve(ctx context.Context, intent SandboxIntent) (SandboxInvocation, error) {
	org, err := sandboxOrg(ctx)
	if err != nil {
		return SandboxInvocation{}, err
	}
	if err := intent.validate(); err != nil {
		return SandboxInvocation{}, err
	}
	identity := SandboxIdentity{SandboxIntent: intent, OrgID: org, Target: s.target, Namespace: s.namespace, PodName: "nf-gate-" + intent.InvocationID.String()}
	tx, err := beginSandboxJournalTx(ctx, s.pool)
	if err != nil {
		return SandboxInvocation{}, err
	}
	defer tx.Rollback(context.Background())
	reserved, err := s.lockCapacity(ctx, tx)
	if err != nil {
		return SandboxInvocation{}, err
	}
	deleting, err := lockSandboxOrg(ctx, tx, org)
	if err != nil {
		return SandboxInvocation{}, err
	}
	v, err := scanSandbox(tx.QueryRow(ctx, sandboxSelect, org, intent.InvocationID))
	if err == nil {
		if !reflect.DeepEqual(v.Identity, identity) {
			return SandboxInvocation{}, ErrSandboxConflict
		}
		return v, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return SandboxInvocation{}, err
	}
	var runFenced bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM gates.sandbox_run_fences WHERE org_id=$1 AND run_id=$2)`, org, intent.RunID).Scan(&runFenced); err != nil {
		return SandboxInvocation{}, err
	}
	if deleting || runFenced {
		return SandboxInvocation{}, ErrSandboxFenced
	}
	if reserved >= s.ceiling {
		return SandboxInvocation{}, ErrSandboxCapacity
	}
	if err = ensureSandboxAttempt(ctx, tx, identity); err != nil {
		return SandboxInvocation{}, err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO gates.sandbox_invocations (id,org_id,run_id,target,namespace,pod_name,identity,attempt_id) VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT DO NOTHING`, intent.InvocationID, org, intent.RunID, s.target, s.namespace, identity.PodName, identity, intent.AttemptID)
	if err != nil {
		return SandboxInvocation{}, err
	}
	// A foreign ID collision is rejected without looking up the foreign row.
	if tag.RowsAffected() != 1 {
		return SandboxInvocation{}, ErrSandboxConflict
	}
	// Operator metadata only; this increment commits with the tenant-scoped intent.
	if _, err = tx.Exec(ctx, `UPDATE gates.sandbox_capacity SET reserved=reserved+1 WHERE target=$1`, s.target); err != nil {
		return SandboxInvocation{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return SandboxInvocation{}, err
	}
	return SandboxInvocation{Identity: identity}, nil
}

// ReadExact cannot grant create/exec authority. A persisted claim, even one whose
// acknowledgement was lost, can never be redeemed for a second dispatch.
func (s *SandboxJournal) ReadExact(ctx context.Context, identity SandboxIdentity) (SandboxInvocation, error) {
	org, err := sandboxOrg(ctx)
	if err != nil {
		return SandboxInvocation{}, err
	}
	if identity.OrgID != org || identity.Target != s.target || identity.Namespace != s.namespace {
		return SandboxInvocation{}, ErrSandboxConflict
	}
	v, err := scanSandbox(s.pool.QueryRow(ctx, sandboxSelect, org, identity.InvocationID))
	if err != nil {
		return SandboxInvocation{}, err
	}
	if !reflect.DeepEqual(identity, v.Identity) {
		return SandboxInvocation{}, ErrSandboxConflict
	}
	return v, nil
}

// change serializes settlement with pool admission. All tenant row access still
// carries authenticated org scope; the capacity lock is operator metadata only.
func (s *SandboxJournal) change(ctx context.Context, identity SandboxIdentity, admission bool, apply func(*SandboxInvocation) error) error {
	org, err := sandboxOrg(ctx)
	if err != nil {
		return err
	}
	if identity.OrgID != org || identity.Target != s.target || identity.Namespace != s.namespace {
		return ErrSandboxConflict
	}
	tx, err := beginSandboxJournalTx(ctx, s.pool)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	if _, err = s.lockCapacity(ctx, tx); err != nil {
		return err
	}
	if admission {
		deleting, err := lockSandboxOrg(ctx, tx, org)
		if err != nil {
			return err
		}
		var runFenced bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM gates.sandbox_run_fences WHERE org_id=$1 AND run_id=$2)`, org, identity.RunID).Scan(&runFenced); err != nil {
			return err
		}
		if deleting || runFenced {
			return ErrSandboxFenced
		}
	}
	v, err := scanSandbox(tx.QueryRow(ctx, sandboxSelect+` FOR UPDATE`, org, identity.InvocationID))
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(identity, v.Identity) {
		return ErrSandboxConflict
	}
	wasReleased := v.Released
	wasAccepted := v.AcceptedEvaluation != nil
	if err = apply(&v); err != nil {
		return err
	}
	if !wasAccepted && v.AcceptedEvaluation != nil {
		if err = ensureSandboxEvaluation(ctx, tx, identity, *v.AcceptedEvaluation); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE gates.sandbox_invocations SET create_claim=$3,pod_uid=$4,exec_claim=$5,terminal=$6,cleanup=$7,absence=$8,tool_result=$9,accepted_evaluation=$10,released=$11 WHERE org_id=$1 AND id=$2`, org, identity.InvocationID, v.CreateClaim, v.PodUID, v.ExecClaim, v.Terminal, v.Cleanup, v.Absence, v.ToolResult, v.AcceptedEvaluation, v.Released)
	if err != nil {
		return err
	}
	if !wasReleased && v.Released {
		// Only this atomic terminal+absence transition releases operator capacity.
		tag, err := tx.Exec(ctx, `UPDATE gates.sandbox_capacity SET reserved=reserved-1 WHERE target=$1 AND reserved>0`, s.target)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrSandboxConflict
		}
	}
	return tx.Commit(ctx)
}
