#!/usr/bin/env bash
# purge-orgs.sh PATTERN [--yes] — permanently remove every organization whose
# name matches the POSIX regex PATTERN from the kw cluster deployment: its rows
# in every service schema, its bare repositories, its CI logs and artifacts,
# and the users who belonged to nothing else.
#
# BREAK-GLASS ONLY. An organization is deleted through the platform: its owner
# calls DELETE /api/v1/orgs/{org} (the Orgs screen's danger zone, or
# hack/delete-org.sh, which the e2e scripts use on exit), identity announces the
# deletion, and every service removes its own share from its own schema,
# object storage and disk. This script reaches into every service's schema at
# once — the one thing no service may do — and exists for what the API cannot
# reach: an organization whose owner's credential is gone, organizations left
# over from before the API existed, or a deployment whose event bus is down. It
# also removes users who belonged to nothing else, which the API deliberately
# never does. It is one transaction: a failure part-way leaves the database
# untouched. A schema added after this script was last edited is not in it.
#
# Without --yes it prints what would be removed and exits.
set -euo pipefail
cd "$(dirname "$0")/.."
source hack/env.sh >/dev/null

pattern="${1:?usage: purge-orgs.sh PATTERN [--yes]}"
confirm="${2:-}"
NS="${NF_NAMESPACE:-novaforge}"
REL="${REL:-novaforge}"
k() { kubectl --context "$KUBE_CONTEXT" -n "$NS" "$@"; }

pg=$(k get pods -l "app=$REL-postgres" -o name 2>/dev/null | head -1)
[ -n "$pg" ] || pg=$(k get pods -o name | grep -- "-postgres-" | head -1)
psql() { k exec -i "$pg" -- psql -U novaforge -d novaforge -v ON_ERROR_STOP=1 -At "$@"; }

# The regex travels as a psql variable (:'pattern'), quoted by psql itself, so
# nothing in it is ever spliced into SQL text.
ids=$(printf '%s\n' "select id from identity.organizations where name ~ :'pattern' order by created_at;" |
	psql -v pattern="$pattern")
if [ -z "$ids" ]; then
	echo "no organization matches /$pattern/"
	exit 0
fi
printf '%s\n' "select name from identity.organizations where name ~ :'pattern' order by created_at;" |
	psql -v pattern="$pattern" | sed 's/^/  /'
echo "$(echo "$ids" | wc -l | tr -d ' ') organization(s) match /$pattern/"
if [ "$confirm" != "--yes" ]; then
	echo "dry run — pass --yes to delete them"
	exit 0
fi

# Collect object keys before the rows naming them are gone.
jobs=$(printf '%s\n' "select j.id from ci.workflow_jobs j join ci.workflow_runs r on r.id = j.run_id
	join identity.organizations o on o.id = r.org_id where o.name ~ :'pattern';" | psql -v pattern="$pattern")

psql -v pattern="$pattern" <<'SQL'
begin;
create temp table doomed on commit drop as
	select id from identity.organizations where name ~ :'pattern';
-- Users whose every membership is in a doomed organization, and who had at
-- least one: a user belonging to a surviving organization is never touched.
create temp table doomed_users on commit drop as
	select m.user_id as id from identity.org_members m
	group by m.user_id
	having bool_and(m.org_id in (select id from doomed));

-- Children first where a foreign key does not cascade.
delete from agents.agent_runs where org_id in (select id from doomed);
delete from agents.tool_calls where org_id in (select id from doomed);
delete from agents.agents where org_id in (select id from doomed);
delete from approvals.approval_requests where org_id in (select id from doomed);
delete from ci.artifacts where org_id in (select id from doomed);
delete from ci.workflow_runs where org_id in (select id from doomed);
delete from ci.runners where org_id in (select id from doomed);
delete from gates.gate_evaluations where org_id in (select id from doomed);
delete from gitplatform.capability_grants where org_id in (select id from doomed);
delete from gitplatform.repositories where org_id in (select id from doomed);
delete from graph.code_chunks where org_id in (select id from doomed);
delete from graph.graph_nodes where org_id in (select id from doomed);
update knowledge.knowledge_entries set superseded_by = null
	where org_id in (select id from doomed);
delete from knowledge.knowledge_entries where org_id in (select id from doomed);
delete from retention.retention_policies where org_id in (select id from doomed);
delete from reviews.runs where org_id in (select id from doomed);
delete from secrets.secret_leases where org_id in (select id from doomed);
delete from secrets.secret_values where org_id in (select id from doomed);
delete from work.maintenance_proposals where org_id in (select id from doomed);
delete from work.work_item_comments where org_id in (select id from doomed);
update work.work_items set parent_id = null where org_id in (select id from doomed);
delete from work.work_items where org_id in (select id from doomed);
delete from identity.organizations where id in (select id from doomed);
delete from identity.users where id in (select id from doomed_users);
commit;
SQL

# Bare repositories live under <root>/<org id>/ on the shared volume.
git_pod=$(k get pods -o name | grep -- "-git-platform-" | head -1)
root=$(k exec "$git_pod" -- printenv GIT_DATA_DIR)
for id in $ids; do
	k exec "$git_pod" -- rm -rf "$root/$id"
done

# CI logs are keyed by job, artifacts by organization.
minio_pod=$(k get pods -o name | grep -- "-minio-" | head -1)
if [ -n "$minio_pod" ]; then
	# The bucket name is fixed in cmd/ci-runner (artifactsBucket). MinIO in
	# single-drive mode keeps each object as a directory under /data, so
	# removing the directory removes the object.
	bucket=novaforge-ci
	for id in $ids; do
		k exec "$minio_pod" -- sh -c "rm -rf /data/$bucket/artifacts/$id" || true
	done
	for job in $jobs; do
		k exec "$minio_pod" -- sh -c "rm -rf /data/$bucket/logs/$job.txt" || true
	done
fi
echo "purged $(echo "$ids" | wc -l | tr -d ' ') organization(s)"
