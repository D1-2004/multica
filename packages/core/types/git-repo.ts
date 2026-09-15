import type { Agent, CoordinatorContract } from "./agent";

export interface GitAgentSkillPreview {
  enabled?: boolean;
  source_path: string;
  name: string;
  description: string;
  file_count: number;
}

export interface GitAgentPreviewRequest {
  connection_id?: string;
  repository: string;
  ref?: string;
}

export interface GitAgentPreview {
  definition?: Record<string, unknown>;
  requirements?: import("./agent-package").AgentPackageRequirements;
  preview_id?: string;
  expires_at?: string;
  repository_url?: string;
  connection_id: string;
  repository: string;
  ref: string;
  resolved_sha: string;
  name: string;
  description: string;
  instructions: string;
  coordinator_contract?: CoordinatorContract | null;
  skills: GitAgentSkillPreview[];
  compatible_providers: string[];
  warnings: string[];
  blockers: string[];
}

export interface AgentSource {
  repository_url?: string;
  can_sync?: boolean;
  configuration_scope?: string[];
  agent_id: string;
  source_type: "git" | string;
  connection_id: string | null;
  repository: string;
  ref: string;
  manifest_path: string;
  synced_commit_sha: string;
  sync_status: "ready" | "failed" | "disconnected" | string;
  last_sync_error: string | null;
  last_sync_attempt_at: string | null;
  last_synced_at: string;
  connected: boolean;
}

export interface CreateAgentPackageResponse {
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
  rollback_of?: string;
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
  connection_id?: string;
  repository: string;
  repository_url: string;
  default_branch: string;
  branches: { name: string; commit: { sha: string }; protected: boolean }[];
  tags?: { name: string; commit: { sha: string } }[];
}

export interface AgentPublication {
  id: string;
  source_type: string;
  repository_url: string;
  ref: string;
  commit_sha: string;
  published_at: string;
  published_by: string;
  author_name: string;
  changed: boolean;
  rollback_of: string;
  has_configuration_snapshot: boolean;
  initial_publication: boolean;
}

export interface AgentPublicationList {
  publications: AgentPublication[];
  next_cursor: string | null;
}


export interface GitConnection { id: string; provider: string; account_login: string; created_at: string; }
export interface GitConnections { connections: GitConnection[]; token_connections_available: boolean; }
export interface ConnectGitRepositoryRequest { repository_url: string; token: string; connection_id?: string; }

export interface GitRepositoryIdentity { repository_url: string; provider: string; connections: GitConnection[]; }
