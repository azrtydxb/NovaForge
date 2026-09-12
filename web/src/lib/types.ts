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

export interface Repo {
  id: string;
  name: string;
  org_id: string;
  default_branch: string;
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
