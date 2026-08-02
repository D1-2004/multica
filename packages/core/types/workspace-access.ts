export const WORKSPACE_ACCESS_CAPABILITIES = [
  "deployment.manage",
  "deployment.retire",
  "trace.read",
] as const;

export type WorkspaceAccessCapability = (typeof WORKSPACE_ACCESS_CAPABILITIES)[number];
export type WorkspaceAccessResourceScope = "own_agents" | "workspace";
export type WorkspaceAccessGrantStatus = "active" | "disabled";

export interface WorkspaceAccessGrant {
  id: string;
  workspace_id: string;
  name: string;
  capabilities: WorkspaceAccessCapability[];
  resource_scope: WorkspaceAccessResourceScope;
  status: WorkspaceAccessGrantStatus;
  version: number;
  created_at: string;
  updated_at: string;
  disabled_at: string | null;
}

export interface WorkspaceAccessToken {
  id: string;
  grant_id: string;
  name: string;
  token_prefix: string;
  expires_at: string | null;
  last_used_at: string | null;
  created_at: string;
  revoked_at: string | null;
}

export interface CreateWorkspaceAccessTokenResponse extends WorkspaceAccessToken {
  token: string;
}

export interface CreateWorkspaceAccessGrantRequest {
  name: string;
  capabilities: WorkspaceAccessCapability[];
  resource_scope: WorkspaceAccessResourceScope;
}

export interface UpdateWorkspaceAccessGrantRequest extends CreateWorkspaceAccessGrantRequest {
  version: number;
}

export interface CreateWorkspaceAccessTokenRequest {
  name: string;
  expires_at: string | null;
}
