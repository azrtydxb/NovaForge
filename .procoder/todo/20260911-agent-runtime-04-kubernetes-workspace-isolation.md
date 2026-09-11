# agent-runtime 04: Kubernetes workspace isolation

Status: open
Created: 2026-09-11

## Description

Plan step 4 of `.procoder/plans/agent-runtime.md`, which exists to: Make AI agents first-class contributors: agent identities with provenance, Agent Runs executing in isolated per-run Kubernetes namespaces, a typed and fully audited tool surface instead of shell access, and all model access routed through go-ai-sdk so no provider-specific logic enters NovaForge.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `internal/workspace/k8s.go`, `internal/workspace/k8s_test.go`

Interfaces: produces `workspace.Provisioner` with `Create(ctx context.Context, runID uuid.UUID, spec Spec) (Workspace, error)`, `Destroy(ctx context.Context, runID uuid.UUID) error`, and `Reap(ctx context.Context, olderThan time.Duration) (int, error)`, where `Spec` is `{Image string, Env map[string]string, CPULimit, MemLimit string, RepoPVC string}` and `Workspace` is `{Namespace string, PodName string}`. Namespaces are named `nf-run-<runID>`.

## Acceptance criteria

- [ ] Write the failing test `internal/workspace/k8s_test.go` using `k8s.io/client-go/kubernetes/fake`: `func TestCreateMakesNamespacedPod(t *testing.T)` asserts a namespace `nf-run-<id>` and a pod inside it are created, and that the pod carries the label `novaforge.io/run-id=<id>`; `func TestCreateAppliesDenyAllNetworkPolicy(t *testing.T)` asserts a NetworkPolicy exists in the namespace with an empty pod selector and no ingress rules; `func TestDestroyRemovesNamespace(t *testing.T)` asserts the namespace is gone after `Destroy`; `func TestReapRemovesOrphanedNamespaces(t *testing.T)` creates a namespace labelled with a creation timestamp two hours old and asserts `Reap(ctx, time.Hour)` deletes it and returns 1. Run `go test ./internal/workspace/` — expect FAIL with "undefined: workspace.Provisioner".
- [ ] Add `go get k8s.io/client-go` and implement `Create`: create the namespace with labels `novaforge.io/run-id` and `novaforge.io/created-at`, apply a default-deny NetworkPolicy, apply a ResourceQuota from `Spec`, then create the pod mounting the repository PVC read-only.
- [ ] Implement `Reap` listing namespaces with the `novaforge.io/run-id` label whose `novaforge.io/created-at` is older than the threshold and deleting them, so a crashed controller never leaks workspaces.
- [ ] Run `go test ./internal/workspace/` — expect PASS.
- [ ] Commit as `feat: provision isolated per-run kubernetes workspaces`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the task open. -->
