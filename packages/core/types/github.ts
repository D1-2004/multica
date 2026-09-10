import type { Agent, CoordinatorContract, CreateAgentRequest } from "./agent";

export type GitHubPullRequestState = "open" | "closed" | "merged" | "draft";

/** Aggregated CI status for a PR's current head SHA, computed server-side from
 * the latest check_suite per app. `null` when no completed suite has been seen
 * yet (e.g. PR just opened, or repository has no CI configured).
 *
 * Legacy compat field kept for backend drift; the current PR card derives CI
 * status from `checks_rollup` + counts instead. */
export type GitHubPullRequestChecksConclusion = "passed" | "failed" | "pending";

/** Raw mirror of GitHub's legacy `mergeable_state`. Superseded by the
 * `mergeable` + `merge_state_status` snapshot pair; kept optional for backend
 * drift only. */
export type GitHubMergeableState = string;

/** GitHub's `mergeable` verdict — answers ONLY "is there a conflict". `unknown`
 * is a normal transient value (GitHub computes it lazily); it must render as
 * neither "conflict" nor "ready". */
export type GitHubPullRequestMergeable = "mergeable" | "conflicting" | "unknown";

/** GitHub's `mergeStateStatus`. "Ready to merge" is asserted ONLY from `clean`
 * (which folds in required checks + branch protection); the other values are
 * surfaced faithfully and never inferred into a ready/mergeable claim. */
export type GitHubPullRequestMergeStateStatus =
  | "clean"
  | "dirty"
  | "blocked"
  | "behind"
  | "unstable"
  | "draft"
  | "has_hooks"
  | "unknown";

/** GitHub's overall CI rollup verdict (`statusCheckRollup.state`). `null`/absent
 * means NO checks have been reported yet — it must never render as passed. */
export type GitHubPullRequestChecksRollup =
  | "success"
  | "failure"
  | "pending"
  | "error"
  | "expected";

export interface GitHubInstallation {
  id: string;
  workspace_id: string;
  /** GitHub's numeric installation id — the management handle used by the
   * connect / disconnect flows. Omitted when the caller cannot manage
   * integrations (see `ListGitHubInstallationsResponse.can_manage`). */
  installation_id?: number;
  account_login: string;
  account_type: "User" | "Organization";
  account_avatar_url: string | null;
  created_at: string;
  /** Display name of the workspace member who connected this installation.
   * Optional because older backends and minimum-visibility deployments may
   * omit it; the UI renders the "connected by" line only when present. */
  connected_by?: string;
}

export interface GitHubReusableInstallation {
  /** Internal UUID of the trusted binding in another managed workspace. */
  id: string;
  account_login: string;
  account_type: "User" | "Organization";
  account_avatar_url: string | null;
  source_workspace_id: string;
  source_workspace_name: string;
}

export interface GitHubPullRequest {
  id: string;
  /** Source provider. Older GitHub-only backends omit it. */
  provider?: "github" | "forgejo" | "gitea" | "gitlab";
  workspace_id: string;
  repo_owner: string;
  repo_name: string;
  number: number;
  title: string;
  state: GitHubPullRequestState;
  html_url: string;
  branch: string | null;
  author_login: string | null;
  author_avatar_url: string | null;
  merged_at: string | null;
  closed_at: string | null;
  pr_created_at: string;
  pr_updated_at: string;
  /** Conflict verdict from the GitHub API snapshot. Answers ONLY
   * "is there a conflict"; older backends omit it. */
  mergeable?: GitHubPullRequestMergeable | null;
  /** GitHub's `mergeStateStatus` from the snapshot. Source of the "Ready to
   * merge" claim (only when `clean`); older backends omit it. */
  merge_state_status?: GitHubPullRequestMergeStateStatus | null;
  /** GitHub's overall CI rollup verdict from the snapshot. `null` means no
   * checks only when `snapshot_available === true`; absence alone is not a
   * positive or "no checks" verdict. */
  checks_rollup?: GitHubPullRequestChecksRollup | null;
  /** True only when the GitHub API snapshot feature is enabled and the stored
   * snapshot belongs to this PR's current head. False means the CI/merge
   * snapshot region must be hidden; omitted preserves legacy provider output. */
  snapshot_available?: boolean;
  /** Check counts from the snapshot. Older backends omit these; treat absence
   * as 0. `checks_total` is 0 when no checks have been reported. */
  checks_total?: number;
  checks_passed?: number;
  checks_failed?: number;
  checks_running?: number;
  /** Names of the currently failing checks, for the "…failed · a, b" summary.
   * Older backends omit it; treat absence as an empty list. */
  failed_check_names?: string[];
  /** True when the shown snapshot is stale (GitHub outage / revoked key). The
   * card greys out both status elements and shows the snapshot age. */
  snapshot_stale?: boolean;
  /** RFC3339 timestamp of when the snapshot was fetched, for the stale hint. */
  snapshot_fetched_at?: string | null;
  /** Legacy mirror of GitHub's `mergeable_state`. Optional; superseded by
   * `mergeable` + `merge_state_status`. */
  mergeable_state?: GitHubMergeableState | null;
  /** Legacy aggregated CI conclusion. Optional; superseded by `checks_rollup`. */
  checks_conclusion?: GitHubPullRequestChecksConclusion | null;
  /** Legacy pending-suite count. Optional; superseded by `checks_running`. */
  checks_pending?: number;
  /** Diff stats from GitHub's `pull_request` payload. Older backends omit
   * these fields; we treat 0/0/0 as "unknown" and hide the stats row. */
  additions?: number;
  deletions?: number;
  changed_files?: number;
}

export interface ListGitHubInstallationsResponse {
  installations: GitHubInstallation[];
  /** Existing bindings from other workspaces the same caller can manage.
   * Optional for compatibility with older servers. */
  reusable_installations?: GitHubReusableInstallation[];
  /** Whether the deployment has GitHub App credentials configured. When false, the Connect button is hidden / disabled. */
  configured: boolean;
  /** Whether the server can mint installation tokens to browse repositories.
   * Older backends omit this field; callers must treat absence as false. */
  repository_browse_configured?: boolean;
  /** Whether the caller can connect / disconnect installations. Non-admin
   * members get `false` along with installations that omit `installation_id`.
   * Older backends predating MUL-2413 omit the field; treat absence as
   * `false` for read-only safety. */
  can_manage?: boolean;
}

export interface GitHubConnectResponse {
  /** The GitHub App install URL the browser should open. Empty when `configured` is false. */
  url?: string;
  configured: boolean;
}

export interface GitHubAgentRepository {
  installation_id: string;
  full_name: string;
  private: boolean;
  default_branch: string;
  html_url: string;
}

export interface ListGitHubAgentRepositoriesResponse {
  repositories: GitHubAgentRepository[];
}

export interface GitHubAgentSkillPreview {
  enabled?: boolean;
  source_path: string;
  name: string;
  description: string;
  file_count: number;
}

export interface GitHubAgentPreviewRequest {
  installation_id: string;
  repository: string;
  ref?: string;
}

export interface GitHubAgentPreview {
  definition?: Record<string, unknown>;
  requirements?: import("./agent-package").AgentPackageRequirements;
  preview_id?: string;
  expires_at?: string;
  repository_url?: string;
  installation_id: string;
  repository: string;
  ref: string;
  resolved_sha: string;
  name: string;
  description: string;
  instructions: string;
  coordinator_contract?: CoordinatorContract | null;
  skills: GitHubAgentSkillPreview[];
  compatible_providers: string[];
  warnings: string[];
  blockers: string[];
}

export interface CreateGitHubAgentRequest extends CreateAgentRequest {
  preview_id?: string;
  installation_id?: string;
  repository?: string;
  ref?: string;
  resolved_sha?: string;
}

export interface AgentSource {
  repository_url?: string;
  can_sync?: boolean;
  configuration_scope?: string[];
  agent_id: string;
  source_type: "github" | string;
  installation_id: string | null;
  repository: string;
  ref: string;
  manifest_path: string;
  synced_commit_sha: string;
  sync_status: "ready" | "failed" | "disconnected" | string;
  last_sync_error: string | null;
  last_sync_attempt_at: string | null;
  last_synced_at: string;
  github_connected: boolean;
}

export interface CreateGitHubAgentResponse {
  agent: Agent;
  source: AgentSource;
  warnings: string[];
}

export interface SyncAgentSourceResponse {
  source: AgentSource;
  changed: boolean;
  warnings: string[];
}

export interface AgentSourceFileChange {
  path: string;
  status: string;
  before: string | null;
  after: string | null;
  before_sha?: string;
  after_sha?: string;
  before_mode?: string;
  after_mode?: string;
}

export interface AgentSourceSyncPreview {
  requirements?: import("./agent-package").AgentPackageRequirements;
  preview_id: string;
  expires_at: string;
  repository_url: string;
  ref: string;
  base_sha: string;
  resolved_sha: string;
  git_changes: AgentSourceFileChange[];
  configuration_changes: AgentSourceFileChange[];
  warnings: string[];
  changed: boolean;
}

export interface AgentSourceBranches {
  repository: string;
  repository_url: string;
  default_branch: string;
  branches: { name: string; commit: { sha: string }; protected: boolean }[];
}

export interface GitHubRepository {
  id: number;
  full_name: string;
  html_url: string;
  clone_url: string;
  description: string | null;
  private: boolean;
  archived: boolean;
  default_branch: string;
}

export interface ListGitHubRepositoriesResponse {
  repositories: GitHubRepository[];
  total_count: number;
  next_page: number | null;
}
