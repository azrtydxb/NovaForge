#!/usr/bin/env bash
# owned-db-test.sh [go test args...] — run tests against a disposable database
# this run exclusively owns, then drop it.
#
# A few tests inject real faults: deferred-trigger COMMIT failures, constraint
# triggers installed on the service's own tables. Those cannot run against the
# shared dev datastore, because a trigger installed there would change the
# behaviour of every other suite running at the same time and would survive a
# crashed run. Such tests therefore refuse to run unless the database is named
# nf_ci_recovery_*, which only this script creates.
#
# With no arguments it runs the suites that contain owned-database tests.
set -euo pipefail
cd "$(dirname "$0")/.."
source hack/env.sh

: "${TEST_DATABASE_URL:?no dev database discovered; is novaforge-dev deployed?}"
DB="nf_ci_recovery_$(od -An -tx1 -N8 /dev/urandom | tr -d ' \n')"

psql() { kubectl --context "$KUBE_CONTEXT" -n "$NF_DEV_NAMESPACE" exec -i deploy/postgres -- psql -U novaforge -d postgres -v ON_ERROR_STOP=1 "$@"; }

psql -c "CREATE DATABASE $DB" >/dev/null
# The database is dropped whatever happens, including on interrupt: one left
# behind per failed run would fill the dev cluster's disk over a session.
cleanup() {
	kubectl --context "$KUBE_CONTEXT" -n "$NF_DEV_NAMESPACE" exec -i deploy/postgres -- \
		psql -U novaforge -d postgres -c "DROP DATABASE IF EXISTS $DB WITH (FORCE)" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM
# pgvector is created by the services that need it, but the extension has to be
# available in the new database before any migration runs.
kubectl --context "$KUBE_CONTEXT" -n "$NF_DEV_NAMESPACE" exec -i deploy/postgres -- \
	psql -U novaforge -d "$DB" -v ON_ERROR_STOP=1 -c "CREATE EXTENSION IF NOT EXISTS vector" >/dev/null

# Point every datastore-backed test at the owned database. Redis and S3 stay as
# they are: the fault injection here is purely about SQL-level behaviour.
export TEST_DATABASE_URL="${TEST_DATABASE_URL%/*}/$DB?sslmode=disable"
echo "owned-db-test.sh: $DB"

args=("$@")
[ ${#args[@]} -eq 0 ] && args=(./internal/gates/ -count=1)
go test "${args[@]}"
