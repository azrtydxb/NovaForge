// Shapes returned by the NovaForge REST edge. These mirror api/openapi.yaml,
// which is generated from the edge's own route table — when a handler's JSON
// changes, this file is what has to change with it.

export interface User {
  id: string;
  username: string;
  email: string;
  totp_enabled: boolean;
}

export interface Org {
  id: string;
  name: string;
}

export interface OrgMember {
  user_id: string;
  username: string;
  role: string;
}

/** A named group inside an organization carrying a role. The role is the team's
 * own and bounds its members: a team granting "member" grants that much even to an
 * owner, because a narrower grant that widened to its strongest member would not
 * be narrower. */
export interface Team {
  id: string;
  org_id: string;
  name: string;
  role: string;
  member_ids: string[];
}

/** A grant of one repository to a person or a team. Exactly one of user_id and
 * team_id is set; both are always present so a reader can tell them apart without
 * inferring from a missing key. */
export interface RepoCollaborator {
  user_id: string;
  team_id: string;
  role: string;
}

export interface Repo {
  id: string;
  name: string;
  org_id: string;
  default_branch: string;
  /** An archived repository serves reads and refuses writes. */
  archived: boolean;
  /** Set when this repository was forked from another; empty otherwise. */
  parent_repo_id: string;
}

export interface Ref {
  name: string;
  sha: string;
  kind: string;
}

export interface Commit {
  sha: string;
  message: string;
  author_name: string;
  author_email: string;
  at: string;
}

export interface TreeEntry {
  name: string;
  kind: string;
  sha: string;
  mode: string;
  size: number;
}

/** A registered endpoint outside the platform, told when something happens in a
 * repository. There is no secret field on purpose: the platform never sends one
 * back, so `has_secret` is all a reader can be told about it. */
export interface Hook {
  id: string;
  repo_id: string;
  url: string;
  /** The events this hook asked for. Empty means every event. */
  events: string[];
  active: boolean;
  has_secret: boolean;
  created_at: string;
}

/** One attempt to call one endpoint. `status_code` is 0 when there was no HTTP
 * response at all — a refused connection, a timeout — and `error` then says why,
 * which is why `delivered` is sent rather than left to the screen to infer. */
export interface HookDelivery {
  id: string;
  hook_id: string;
  event: string;
  status_code: number;
  error: string;
  attempt: number;
  at: string;
  delivered: boolean;
}

export interface WorkItem {
  id: string;
  key: string;
  type: string;
  goal: string;
  state: string;
  acceptance: string[];
  constraints: string[];
  required_gates: string[];
  assignee_id: string;
  assignee_kind: string;
  repo_id: string;
  created_at: string;
  /** Sent only when one Work Item is read: a maintenance proposal no person
   * has approved. Agent-runtime refuses to start a run against it. */
  awaiting_approval?: boolean;
  maintenance_proposal?: boolean;
  execution_claimed?: boolean;
}

/** MaintenanceProposal is a scanner finding that became a Work Item. decision
 * is "" while it awaits a person's approval, then "approved" or "dismissed". */
export interface MaintenanceProposal {
  fingerprint: string;
  work_item_key: string;
  work_item_goal: string;
  work_item_type: string;
  state: string;
  resolved: boolean;
  decision: "" | "approved" | "dismissed";
  decided_by: string;
  decided_at: string;
  dismiss_reason: string;
  assignee_id: string;
  assignee_kind: string;
}

export interface Subtask extends WorkItem {
  ready: boolean;
}

/** DashboardException is one thing the platform says needs a person. It is
 * the platform's own list — deriving a second one in the client would be a
 * second opinion about what is wrong. */
export interface DashboardException {
  key: string;
  title: string;
  state: string;
  reason: string;
}

export interface Dashboard {
  exceptions: DashboardException[];
  /** Work Items that reached done since midnight UTC. Unavailable is not zero:
   * zero would read as "nothing was done today". */
  completed_today: number;
  completed_today_available: boolean;
  agents_running: number;
  ready_to_auto_merge: number;
  need_human_review: number;
  architecture_decisions: number;
  gate_failures: number;
  agents_blocked: number;
}

export interface Agent {
  id: string;
  org_id: string;
  name: string;
  role: string;
  model_ref: string;
  enabled: boolean;
  /** What the agent is doing now. Empty means idle — the platform says so
   * rather than the client inferring it from an absent run. */
  current_run_id: string;
  current_work_item_key: string;
  busy_since: string;
}

export interface AgentRun {
  id: string;
  agent_id: string;
  work_item_id: string;
  sponsor_id: string;
  branch: string;
  state: string;
  started_at: string;
  ended_at: string;
  wallclock_limit_seconds: number;
  token_limit: number;
  /** Zero means the run has no cost limit. */
  cost_limit_micros: number;
  tokens_used: number;
  cost_used_micros: number;
  /** Why a run that did not succeed ended; empty otherwise. */
  end_reason: string;
}

export interface EngineeringRun {
  id: string;
  number: number;
  title: string;
  state: string;
  source_ref: string;
  target_ref: string;
  author_id: string;
  author_kind: string;
  agent_name: string;
  model_name: string;
  work_item_id: string;
  created_at: string;
}

export interface ProofRecord {
  gate: string;
  status: string;
  detail: string;
  recorded_at: string;
}

/** Approval is one decision a change needs from a person, raised by the gate
 * controller from what the change's diff does — never from what the run says
 * about itself. It is bound to the head it saw: a later push supersedes it. */
export interface Approval {
  id: string;
  run_id: string;
  action: string;
  action_name: string;
  decision: "pending" | "approved" | "denied" | "superseded" | string;
  reason: string;
  paths: string[];
  head_sha: string;
  comment: string;
  author_id: string;
  author_kind: string;
  decided_by: string;
  decided_at: string;
  created_at: string;
  /** run is present in the organization inbox, where the request must say
   * which change it is about. */
  run?: {
    number: number;
    title: string;
    repo: string;
    state: string;
    author_kind: string;
    agent_name: string;
    source_ref: string;
  };
}

export interface ApprovalList {
  approvals: Approval[];
  can_decide: boolean;
  viewer_id: string;
}

export interface CIRun {
  id: string;
  repo_id: string;
  commit_sha: string;
  ref: string;
  status: string;
  created_at: string;
}

export interface CIJob {
  id: string;
  name: string;
  status: string;
  detail: string;
  /** Set for an agent job, which runs as an Agent Run rather than on a runner. */
  agent_role: string;
  agent_run_id: string;
  work_item_key: string;
}

export interface Artifact {
  id: string;
  name: string;
  size_bytes: number;
}

export interface PersonalToken {
  id: string;
  name: string;
  scopes: string[];
  expires_at: string;
}

export interface SSHKey {
  id: string;
  title: string;
  fingerprint: string;
}

/** One gate the platform understands, as a repository's default branch
 * configures it. `enabled` is the file's `required` flag: an enabled gate
 * blocks a merge until it passes. */
export interface GateConfig {
  name: string;
  declared: boolean;
  enabled: boolean;
  path: string;
  params: Record<string, unknown>;
}

/** An external MCP server an organization has been asked to let agents use.
 * `url` is the endpoint for streamable_http, or the launch command for stdio. */
export interface McpServer {
  id: string;
  name: string;
  url: string;
  transport: "stdio" | "streamable_http";
  description: string;
  status: "pending" | "approved" | "rejected" | "revoked";
  requested_by: string;
  decided_by: string;
  decided_at: string;
  reason: string;
  created_at: string;
}

/** The Engineering Run a gate change became. */
export interface GateProposal {
  run_id: string;
  run_number: number;
  branch: string;
  commit_sha: string;
  title: string;
}

/** What an on-demand maintenance scan found. A scanner that could not run is
 * named, so a scan that skipped one is not read as a clean result. */
export interface ScanResult {
  findings: number;
  proposed_work_item_keys: string[];
  scanner_errors: string[];
}

/** One downloadable file on a release. The object's storage key is deliberately
 * not part of the API, so there is nothing here for a client to construct. */
export interface ReleaseAsset {
  id: string;
  name: string;
  size_bytes: number;
  content_type: string;
}

/** A tag published for download. This is not a CI artifact: an artifact belongs
 * to one job and is evidence of a run, while a release belongs to a version and
 * is the thing a team hands out. */
export interface Release {
  id: string;
  repo_id: string;
  tag: string;
  name: string;
  body: string;
  created_at: string;
  assets: ReleaseAsset[];
}
