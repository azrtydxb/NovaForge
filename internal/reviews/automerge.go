package reviews

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// AutoMergePolicy is the repository- or org-level policy AutoMerger.Consider
// evaluates before ever touching Merger.Merge. Every field defaults to the
// most conservative reading of its zero value: Enabled false refuses
// everything, and a zero MaxFilesChanged or empty AllowedTypes/ForbiddenPaths
// means "no cap" / "no restriction" for that dimension specifically — the
// caller opts into each constraint by setting it, rather than every
// unconfigured field silently blocking merge.
type AutoMergePolicy struct {
	Enabled         bool
	MaxFilesChanged int
	AllowedTypes    []string
	ForbiddenPaths  []string
}

// ImpactResolver resolves the change impact (files changed, paths touched)
// for a run. Production wiring backs this with ComputeImpact via a git
// client; tests supply a stub.
type ImpactResolver func(ctx context.Context, run Run) (Impact, error)

// WorkItemTypeResolver resolves the work item type behind a run, checked
// against AutoMergePolicy.AllowedTypes. Production wiring backs this with
// the work service's gRPC API; tests supply a stub.
type WorkItemTypeResolver func(ctx context.Context, run Run) (string, error)

// AutoMerger decides whether a run may be merged automatically under
// Policy, and — only when policy allows it — merges it through the exact
// same Merger.Merge (and therefore the exact same gates.Controller.MayMerge
// and independent-approval check) a human clicking "merge" would use.
// AutoMerger adds a policy gate IN FRONT of that path; it does not add, and
// cannot add, a way around it. A run refused by the gate controller or by
// the independent-approval check is refused identically whether Consider
// or a human triggered the attempt.
type AutoMerger struct {
	Store  *Store
	Merger *Merger
	Policy AutoMergePolicy

	Impact       ImpactResolver
	WorkItemType WorkItemTypeResolver

	// Method is the merge method Consider passes to Merger.Merge ("merge",
	// "squash", or "rebase"). Empty defaults to "merge".
	Method string
}

// Consider evaluates runID against Policy and, if every policy check
// passes, calls Merger.Merge. merged is true only when Merger.Merge itself
// succeeded; every refusal — by policy or by Merger.Merge — returns
// merged=false with a human-readable reason and a nil error. A non-nil
// error means Consider could not even evaluate the policy (e.g. it could
// not look up the run or resolve its impact), not that merge was refused.
func (a *AutoMerger) Consider(ctx context.Context, runID uuid.UUID) (merged bool, reason string, err error) {
	if !a.Policy.Enabled {
		return false, "auto-merge is disabled by policy", nil
	}

	run, err := a.Store.GetRun(ctx, runID)
	if err != nil {
		return false, "", fmt.Errorf("auto-merge: get run %s: %w", runID, err)
	}

	if a.Impact == nil {
		return false, "", fmt.Errorf("auto-merge: no ImpactResolver configured")
	}
	impact, err := a.Impact(ctx, run)
	if err != nil {
		return false, "", fmt.Errorf("auto-merge: resolve impact for run %s: %w", runID, err)
	}

	if a.Policy.MaxFilesChanged > 0 && impact.FilesChanged > a.Policy.MaxFilesChanged {
		return false, fmt.Sprintf("changed %d files, above the auto-merge cap of %d", impact.FilesChanged, a.Policy.MaxFilesChanged), nil
	}

	if forbidden, path, hit := firstForbiddenPath(a.Policy.ForbiddenPaths, impact.Paths); hit {
		return false, fmt.Sprintf("touches forbidden path %q (matched by policy rule %q)", path, forbidden), nil
	}

	if len(a.Policy.AllowedTypes) > 0 {
		if a.WorkItemType == nil {
			return false, "", fmt.Errorf("auto-merge: policy restricts AllowedTypes but no WorkItemTypeResolver is configured")
		}
		typ, err := a.WorkItemType(ctx, run)
		if err != nil {
			return false, "", fmt.Errorf("auto-merge: resolve work item type for run %s: %w", runID, err)
		}
		if !containsString(a.Policy.AllowedTypes, typ) {
			return false, fmt.Sprintf("work item type %q is not in the auto-merge allow list", typ), nil
		}
	}

	if a.Merger == nil {
		return false, "", fmt.Errorf("auto-merge: no Merger configured")
	}
	method := a.Method
	if method == "" {
		method = "merge"
	}

	sha, err := a.Merger.Merge(ctx, runID, method)
	if err != nil {
		// Merger.Merge itself refused — through the gate controller or the
		// independent-approval check, exactly as it would for a human.
		// AutoMerger adds no privileged path around this: the refusal
		// reason is Merger.Merge's own.
		return false, err.Error(), nil
	}

	return true, fmt.Sprintf("auto-merged run %s as %s", runID, sha), nil
}

// firstForbiddenPath reports the first (rule, path) pair where path is
// prefixed by rule, if any.
func firstForbiddenPath(rules, paths []string) (rule, path string, hit bool) {
	for _, r := range rules {
		for _, p := range paths {
			if strings.HasPrefix(p, r) {
				return r, p, true
			}
		}
	}
	return "", "", false
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
