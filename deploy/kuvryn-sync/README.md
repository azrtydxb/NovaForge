# NovaForge on kw with Kuvryn Sync

Kuvryn Sync renders `deploy/helm/novaforge` from `main`, using `values-kw.yaml`,
and reconciles the `novaforge` namespace. The first release keeps the qualified
application tag `c8ade67`, analysis digest and semantic producer configuration.
The public Git repository requires no Git credential.

## Release an application change

```sh
# Commit application changes first. BuildKit runs on kw; no local Docker.
./hack/build-images.sh
./hack/promote-kw.sh "$(git rev-parse --short HEAD)"
# Review and commit the resulting values-kw.yaml change, then push main.
git add deploy/helm/novaforge/values-kw.yaml
git commit -m 'Promote verified NovaForge images on kw'
git push origin main
```

Promotion refuses an unknown commit or edited values file. It verifies every
service and the analysis sandbox at both Nexus connectors, requires matching
manifest digests, and records the analysis digest with the application tag.
Build/promotion remain explicit operator steps: this repository currently has
no image-building CI workflow. Merging application code alone does not select an
unbuilt image. Configuration changes in the chart or kw values sync directly.
The separately qualified semantic-producer digest remains in its operator Secret;
changing that toolchain requires its own qualification.

`hack/deploy.sh` refuses while a Sync Application exists in the namespace,
including when it cannot establish ownership. Do not use Helm upgrades or
`kubectl set image` alongside Sync. Revert the promotion/configuration commit to
roll back. Sync can also roll back a failed health check; an image rollback does
not undo database migrations.

## Ownership and credentials

The chart references `novaforge-secrets`; it never renders a Secret in kw mode.
Its existing service keys are preserved. The bundled databases additionally
reference `POSTGRES_PASSWORD`, `MINIO_ROOT_USER` and `MINIO_ROOT_PASSWORD` in that
Secret, copied from their existing settings during migration. The registry pull
Secret, OpenBao configuration, semantic producer configuration and cert-manager's
TLS Secret also remain under their existing owners.

`rbac.yaml` grants Sync access only to rendered namespace resources and health
observations. It cannot read/write Secrets, manage RBAC, delete PVCs or touch
another namespace. Runtime service accounts and permissions remain operator-owned
in `runtime-rbac.yaml`, excluded from the Application's Helm render. Apply changes
to those permissions separately, after reviewing their scope. This separation
keeps runtime RBAC changes out of automatic reconciliation. These are direct API
permissions: trusted Git can still change workloads that use the existing runtime
identities and mounted Secrets. Repository write access remains deployment authority.

All three persistent claims carry `sync.kuvryn.io/prune: disabled`; the deployer
also lacks PVC delete permission. The Application uses `deletionPolicy: Orphan`.
The namespace, existing PV bindings and load-balancer addresses are retained.
PostgreSQL/MinIO use zero surge because each shares one ReadWriteOnce volume.
Changing datastore pod configuration entails a brief single-instance restart.

## One-time handover from Helm

These steps are an operator migration, not the normal release path. Preserve
private backups outside Git and verify the exact rendered diff before takeover.
Never run `helm uninstall`: that would delete the running resources and claims.

1. Back up PostgreSQL, existing Helm values/manifests/release records and the
   service Secret to an access-restricted location. Record PVC UIDs/bound volumes.
   Add the three datastore Secret aliases with their existing values; never
   generate replacement passwords during adoption.
2. Publish the chart, sanitized kw values and Sync definitions on `main`.
3. Render and validate the exact published configuration:

   ```sh
   helm template novaforge deploy/helm/novaforge -n novaforge \
     -f deploy/helm/novaforge/values-kw.yaml > /tmp/novaforge-desired.yaml
   kubectl --context kw -n novaforge diff --server-side \
     --field-manager=kuvryn-sync --force-conflicts -f /tmp/novaforge-desired.yaml
   kubectl --context kw -n novaforge apply --server-side --dry-run=server \
     --field-manager=kuvryn-sync --force-conflicts -f /tmp/novaforge-desired.yaml
   ```

   The initial diff is only datastore credential references/zero-surge strategy
   and PVC protection. Keep diff output private: old pod environments can contain
   credentials. No application image, database content, endpoint or volume change
   is part of this handover.

4. Apply `rbac.yaml` and `repository.yaml`. Confirm Repository readiness. Transfer
   only rendered objects from legacy field managers, then apply the reviewed
   desired state using `kuvryn-sync` as field manager. Clearing managed fields is
   a one-time explicit ownership transfer, not a recurring force-sync policy:

   ```sh
   for object in $(kubectl --context kw -n novaforge get \
       -f /tmp/novaforge-desired.yaml -o name); do
     kubectl --context kw -n novaforge patch "$object" --type=merge \
       -p '{"metadata":{"managedFields":[{}]}}'
   done
   kubectl --context kw -n novaforge apply --server-side \
     --field-manager=kuvryn-sync --force-conflicts -f /tmp/novaforge-desired.yaml
   ```

5. After backing up their complete contents, retire only Helm's release-record
   Secrets (`owner=helm,name=novaforge`). Preserve all application/operator Secrets.
   Apply `application.yaml`; subsequent reconciliations use `conflictPolicy: fail`.
6. Verify Synced/Healthy, all deployments ready, unchanged PVC identities, existing
   data access, operator Secrets unchanged and the direct-Helm guard. Run the
   deployed Git, work/CI, graph, agent, GUI and air-gap suites. Retain the applied
   Git revision and qualification receipts.

```sh
kubectl --context kw -n novaforge get repositories.sync.kuvryn.io,applications.sync.kuvryn.io
kubectl --context kw -n novaforge get revisions.sync.kuvryn.io --sort-by=.metadata.creationTimestamp
```

The kw console is `https://sync.kw.watteel.lab`. Self-healing reconciles drift where field ownership permits it. A manual merge
patch can acquire separate Update ownership, even with the same manager name;
`conflictPolicy: fail` then refuses takeover until the operator resolves that
conflict. This behavior was verified with a metadata-only qualification probe. Suspending an Application
alone does not permit the legacy deployment script to overwrite its resources;
an emergency ownership transfer must be explicit and reviewed.
