#!/usr/bin/env bash
# delete-org.sh ORG EDGE_URL [XDG_CONFIG_HOME] — delete an organization the way
# its owner does, through DELETE /api/v1/orgs/{org}: identity announces the
# deletion and every service removes its own share of the organization's data.
#
# The e2e scripts call this on exit with the credential of the account that
# created the organization (the nf login stored under XDG_CONFIG_HOME). Only
# when that path is unavailable — no stored credential, the edge unreachable,
# or the API refusing — does it fall back to hack/purge-orgs.sh, the
# break-glass operator tool that deletes at the storage layer.
set -euo pipefail
cd "$(dirname "$0")/.."

org="${1:?usage: delete-org.sh ORG EDGE_URL [XDG_CONFIG_HOME]}"
edge="${2:?usage: delete-org.sh ORG EDGE_URL [XDG_CONFIG_HOME]}"
config="${3:-${XDG_CONFIG_HOME:-}}"

token=""
if [ -n "$config" ] && [ -f "$config/novaforge/config.json" ]; then
	token="$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1])).get("token",""))' "$config/novaforge/config.json" 2>/dev/null || true)"
fi

if [ -n "$token" ]; then
	body="$(python3 -c 'import json,sys;print(json.dumps({"confirm_name":sys.argv[1]}))' "$org")"
	if curl -fsS -X DELETE "$edge/api/v1/orgs/$org" \
		-H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
		-d "$body" >/dev/null; then
		echo "deleted $org through the API"
		exit 0
	fi
	echo "the API did not delete $org; falling back to hack/purge-orgs.sh" >&2
fi
exec ./hack/purge-orgs.sh "^$org\$" --yes
