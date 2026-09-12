// Package edge terminates the public REST API. It is contract-first: every
// route mounted here has an operation in api/openapi.yaml, and a test fails the
// build if the two drift apart.
package edge

import "net/http"

// Route is one mounted endpoint. The router and the OpenAPI document are both
// generated from this table, so they cannot disagree.
type Route struct {
	Method  string
	Pattern string
	OpID    string
	Summary string
}

// Routes is the complete edge surface.
func Routes() []Route {
	return []Route{
		{http.MethodPost, "/api/v1/auth/register", "register", "Register a user"},
		{http.MethodPost, "/api/v1/auth/login", "login", "Log in, optionally with a TOTP code"},
		{http.MethodPost, "/api/v1/auth/logout", "logout", "Invalidate the current session"},

		{http.MethodGet, "/api/v1/user", "getCurrentUser", "The authenticated user"},
		{http.MethodGet, "/api/v1/user/tokens", "listTokens", "List personal access tokens"},
		{http.MethodPost, "/api/v1/user/tokens", "createToken", "Create a personal access token"},
		{http.MethodDelete, "/api/v1/user/tokens/{id}", "deleteToken", "Revoke a personal access token"},
		{http.MethodGet, "/api/v1/user/ssh-keys", "listSSHKeys", "List SSH public keys"},
		{http.MethodPost, "/api/v1/user/ssh-keys", "addSSHKey", "Add an SSH public key"},
		{http.MethodDelete, "/api/v1/user/ssh-keys/{id}", "deleteSSHKey", "Remove an SSH public key"},
		{http.MethodPost, "/api/v1/user/2fa/setup", "setup2FA", "Begin TOTP enrolment"},
		{http.MethodPost, "/api/v1/user/2fa/verify", "verify2FA", "Confirm TOTP enrolment"},

		{http.MethodGet, "/api/v1/orgs", "listOrgs", "Organizations the caller belongs to"},
		{http.MethodPost, "/api/v1/orgs", "createOrg", "Create an organization"},
		{http.MethodGet, "/api/v1/orgs/{org}", "getOrg", "One organization"},
		{http.MethodGet, "/api/v1/orgs/{org}/members", "listOrgMembers", "Organization members"},
		{http.MethodPost, "/api/v1/orgs/{org}/members", "addOrgMember", "Add an organization member"},

		{http.MethodGet, "/api/v1/orgs/{org}/repos", "listRepos", "Repositories in an organization"},
		{http.MethodPost, "/api/v1/orgs/{org}/repos", "createRepo", "Create a repository"},
		{http.MethodGet, "/api/v1/orgs/{org}/repos/{repo}", "getRepo", "One repository"},
		{http.MethodDelete, "/api/v1/orgs/{org}/repos/{repo}", "deleteRepo", "Delete a repository"},
		{http.MethodGet, "/api/v1/orgs/{org}/repos/{repo}/branches", "listBranches", "Branches"},
		{http.MethodGet, "/api/v1/orgs/{org}/repos/{repo}/tags", "listTags", "Tags"},
		{http.MethodGet, "/api/v1/orgs/{org}/repos/{repo}/commits/{ref}", "listCommits", "Commit history for a ref"},
		{http.MethodGet, "/api/v1/orgs/{org}/repos/{repo}/tree/{ref}/*", "getTree", "Tree listing at a path"},
		{http.MethodGet, "/api/v1/orgs/{org}/repos/{repo}/blob/{ref}/*", "getBlob", "File contents"},
		{http.MethodGet, "/api/v1/orgs/{org}/repos/{repo}/diff", "getDiff", "Unified diff between two refs"},

		{http.MethodGet, "/api/v1/orgs/{org}/repos/{repo}/work", "listWorkItems", "Work Items"},
		{http.MethodPost, "/api/v1/orgs/{org}/repos/{repo}/work", "createWorkItem", "Create a Work Item"},
		{http.MethodGet, "/api/v1/orgs/{org}/repos/{repo}/work/{key}", "getWorkItem", "One Work Item"},

		{http.MethodGet, "/api/v1/orgs/{org}/dashboard", "getDashboard", "What needs human attention"},
		{http.MethodGet, "/api/v1/orgs/{org}/repos/{repo}/work/{key}/subtasks", "listSubtasks", "An epic's subtasks and their readiness"},
		{http.MethodPost, "/api/v1/orgs/{org}/repos/{repo}/work/{key}/decompose", "decomposeEpic", "Break an epic into dependency-ordered subtasks"},

		{http.MethodGet, "/api/v1/orgs/{org}/repos/{repo}/runs", "listRuns", "Engineering Runs"},
		{http.MethodPost, "/api/v1/orgs/{org}/repos/{repo}/runs", "createRun", "Open an Engineering Run"},
		{http.MethodGet, "/api/v1/orgs/{org}/repos/{repo}/runs/{number}", "getRun", "One Engineering Run"},
		{http.MethodGet, "/api/v1/orgs/{org}/repos/{repo}/runs/{number}/proof", "getRunProof", "Per-gate proof for a run"},
		{http.MethodPost, "/api/v1/orgs/{org}/repos/{repo}/runs/{number}/reviews", "submitReview", "Submit a review verdict"},
		{http.MethodPost, "/api/v1/orgs/{org}/repos/{repo}/runs/{number}/merge", "mergeRun", "Merge, if the gates allow it"},

		{http.MethodGet, "/api/v1/orgs/{org}/repos/{repo}/ci/runs", "listCIRuns", "CI runs for a repository"},
		{http.MethodGet, "/api/v1/orgs/{org}/repos/{repo}/ci/runs/{id}", "getCIRun", "One CI run and its jobs"},
		{http.MethodGet, "/api/v1/orgs/{org}/repos/{repo}/ci/logs", "getLatestJobLogs", "The newest run's first job log"},
		{http.MethodGet, "/api/v1/orgs/{org}/repos/{repo}/ci/jobs/{id}/logs", "getJobLogs", "One job's log"},
		{http.MethodGet, "/api/v1/orgs/{org}/repos/{repo}/ci/artifacts", "listLatestArtifacts", "The newest run's artifacts"},
		{http.MethodGet, "/api/v1/orgs/{org}/repos/{repo}/ci/jobs/{id}/artifacts", "listJobArtifacts", "One job's artifacts"},

		{http.MethodGet, "/healthz", "healthz", "Readiness"},
	}
}
