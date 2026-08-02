export const WORKSPACE_ACCESS_CAPABILITIES = [
  "deployment.manage",
  "deployment.retire",
  "trace.read",
] as const;

export type WorkspaceAccessCapability = (typeof WORKSPACE_ACCESS_CAPABILITIES)[number];
export type WorkspaceAccessResourceScope = "own_agents" | "workspace";

export interface WorkspaceAccessToken {
  id: string;
  workspace_id: string;
  name: string;
  capabilities: WorkspaceAccessCapability[];
  resource_scope: WorkspaceAccessResourceScope;
  version: number;
  token_prefix: string;
  expires_at: string | null;
  last_used_at: string | null;
  created_at: string;
  updated_at: string;
  revoked_at: string | null;
}

export interface WorkspaceAccessTokenSecretResponse extends WorkspaceAccessToken {
  token: string;
}

export interface CreateWorkspaceAccessTokenRequest {
  name: string;
  capabilities: WorkspaceAccessCapability[];
  resource_scope: WorkspaceAccessResourceScope;
  expires_at: string | null;
}

export interface UpdateWorkspaceAccessTokenRequest extends CreateWorkspaceAccessTokenRequest {
  version: number;
}

export interface RegenerateWorkspaceAccessTokenRequest {
  expires_at: string | null;
  version: number;
}
