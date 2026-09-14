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
		{http.MethodPost, "/api/v1/orgs/{org}/repos/{repo}/branches", "createBranch", "Create a branch"},
		{http.MethodGet, "/api/v1/orgs/{org}/repos/{repo}/tags", "listTags", "Tags"},
		{http.MethodGet, "/api/v1/orgs/{org}/repos/{repo}/commits/{ref}", "listCommits", "Commit history for a ref"},
		{http.MethodGet, "/api/v1/orgs/{org}/repos/{repo}/tree/{ref}/*", "getTree", "Tree listing at a path"},
		{http.MethodGet, "/api/v1/orgs/{org}/repos/{repo}/blob/{ref}/*", "getBlob", "File contents"},
		{http.MethodGet, "/api/v1/orgs/{org}/repos/{repo}/diff", "getDiff", "Unified diff between two refs"},

		{http.MethodGet, "/api/v1/orgs/{org}/repos/{repo}/work", "listWorkItems", "Work Items"},
		{http.MethodPost, "/api/v1/orgs/{org}/repos/{repo}/work", "createWorkItem", "Create a Work Item"},
		{http.MethodGet, "/api/v1/orgs/{org}/repos/{repo}/work/{key}", "getWorkItem", "One Work Item"},
		{http.MethodGet, "/api/v1/orgs/{org}/repos/{repo}/work/{key}/comments", "listWorkComments", "A Work Item's discussion"},
		{http.MethodPost, "/api/v1/orgs/{org}/repos/{repo}/work/{key}/comments", "addWorkComment", "Comment on a Work Item"},
		{http.MethodPost, "/api/v1/orgs/{org}/repos/{repo}/work/{key}/assign", "assignWorkItem", "Assign a Work Item"},

		{http.MethodGet, "/api/v1/orgs/{org}/dashboard", "getDashboard", "What needs human attention"},
		{http.MethodGet, "/api/v1/orgs/{org}/repos/{repo}/work/{key}/subtasks", "listSubtasks", "An epic's subtasks and their readiness"},
		{http.MethodPost, "/api/v1/orgs/{org}/repos/{repo}/work/{key}/decompose", "decomposeEpic", "Break an epic into dependency-ordered subtasks"},

		{http.MethodGet, "/api/v1/orgs/{org}/agents", "listAgents", "Agents in an organization"},
		{http.MethodPost, "/api/v1/orgs/{org}/agents", "createAgent", "Define an agent"},
		{http.MethodPost, "/api/v1/orgs/{org}/repos/{repo}/agent-runs", "startAgentRun", "Start an Agent Run against a Work Item"},
		{http.MethodGet, "/api/v1/orgs/{org}/agents/stats", "agentStats", "Per-agent run history"},
		{http.MethodGet, "/api/v1/orgs/{org}/agent-runs/{id}", "getAgentRun", "One Agent Run"},
		{http.MethodDelete, "/api/v1/orgs/{org}/agent-runs/{id}", "cancelAgentRun", "Cancel an Agent Run"},
		{http.MethodGet, "/api/v1/orgs/{org}/repos/{repo}/work/{key}/agent-runs", "listWorkItemAgentRuns", "The Agent Runs started against a Work Item"},

		{http.MethodGet, "/api/v1/orgs/{org}/repos/{repo}/runs", "listRuns", "Engineering Runs"},
		{http.MethodPost, "/api/v1/orgs/{org}/repos/{repo}/runs", "createRun", "Open an Engineering Run"},
		{http.MethodGet, "/api/v1/orgs/{org}/repos/{repo}/runs/{number}", "getRun", "One Engineering Run"},
		{http.MethodGet, "/api/v1/orgs/{org}/repos/{repo}/runs/{number}/proof", "getRunProof", "Per-gate proof for a run"},
		{http.MethodPost, "/api/v1/orgs/{org}/repos/{repo}/runs/{number}/reviews", "submitReview", "Submit a review verdict"},
		{http.MethodPost, "/api/v1/orgs/{org}/repos/{repo}/runs/{number}/merge", "mergeRun", "Merge, if the gates allow it"},
		{http.MethodGet, "/api/v1/orgs/{org}/repos/{repo}/runs/{number}/plan", "getRunPlan", "A run's plan"},
		{http.MethodGet, "/api/v1/orgs/{org}/repos/{repo}/runs/{number}/tools", "getRunToolCalls", "The tool calls the agent run behind this run made"},

		{http.MethodGet, "/api/v1/orgs/{org}/repos/{repo}/ci/runs", "listCIRuns", "CI runs for a repository"},
		{http.MethodPost, "/api/v1/orgs/{org}/repos/{repo}/ci/runs", "triggerCIRun", "Run the repository's workflow at a ref"},
		{http.MethodGet, "/api/v1/orgs/{org}/repos/{repo}/ci/runs/{id}", "getCIRun", "One CI run and its jobs"},
		{http.MethodGet, "/api/v1/orgs/{org}/repos/{repo}/ci/logs", "getLatestJobLogs", "The newest run's first job log"},
		{http.MethodGet, "/api/v1/orgs/{org}/repos/{repo}/ci/jobs/{id}/logs", "getJobLogs", "One job's log"},
		{http.MethodGet, "/api/v1/orgs/{org}/repos/{repo}/ci/artifacts", "listLatestArtifacts", "The newest run's artifacts"},
		{http.MethodGet, "/api/v1/orgs/{org}/repos/{repo}/ci/jobs/{id}/artifacts", "listJobArtifacts", "One job's artifacts"},

		{http.MethodGet, "/api/v1/orgs/{org}/repos/{repo}/knowledge", "searchKnowledge", "Project knowledge for a repository"},
		{http.MethodGet, "/api/v1/orgs/{org}/repos/{repo}/search", "searchCode", "Search a repository's indexed code by meaning"},
		{http.MethodGet, "/api/v1/orgs/{org}/repos/{repo}/graph/symbol", "getSymbolRelations", "A symbol and what it relates to"},
		{http.MethodGet, "/api/v1/orgs/{org}/repos/{repo}/maintenance", "listMaintenanceProposals", "What the maintenance scanners proposed"},
		{http.MethodGet, "/api/v1/orgs/{org}/repos/{repo}/gates", "listGateConfig", "Every gate, as the default branch configures it"},
		{http.MethodPost, "/api/v1/orgs/{org}/repos/{repo}/gates/{gate}/proposals", "proposeGateChange", "Propose a gate change as an Engineering Run; never writes the default branch"},
		{http.MethodPost, "/api/v1/orgs/{org}/repos/{repo}/maintenance/{fingerprint}/approve", "approveMaintenanceProposal", "Approve a maintenance proposal and assign its Work Item"},
		{http.MethodPost, "/api/v1/orgs/{org}/repos/{repo}/maintenance/{fingerprint}/dismiss", "dismissMaintenanceProposal", "Dismiss a maintenance proposal, with a reason"},

		{http.MethodGet, "/api/v1/orgs/{org}/secrets", "listSecrets", "Secrets registered for an organization, names only"},
		{http.MethodGet, "/api/v1/orgs/{org}/leases", "listLeases", "Credentials currently brokered to runs"},
		{http.MethodDelete, "/api/v1/orgs/{org}/leases/{id}", "revokeLease", "End a live lease immediately"},

		{http.MethodGet, "/api/v1/approvals/policy", "approvalPolicy", "What the platform requires before each action"},
		{http.MethodGet, "/api/v1/mcp/tools", "mcpTools", "The MCP tools this deployment exposes"},
		{http.MethodGet, "/api/v1/orgs/{org}/mcp/servers", "listMcpServers", "External MCP servers registered for an organization"},
		{http.MethodPost, "/api/v1/orgs/{org}/mcp/servers", "requestMcpServer", "Ask for an external MCP server to be approved"},
		{http.MethodPost, "/api/v1/orgs/{org}/mcp/servers/{id}/decision", "decideMcpServer", "Approve or reject a pending MCP server (owner or admin)"},
		{http.MethodDelete, "/api/v1/orgs/{org}/mcp/servers/{id}", "revokeMcpServer", "Revoke an approved MCP server (owner or admin)"},

		{http.MethodGet, "/healthz", "healthz", "Readiness"},
	}
}
