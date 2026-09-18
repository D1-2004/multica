export type WorkspaceAccessPermission = "all" | "dsh_config";

export interface WorkspaceAccessToken {
  permission: WorkspaceAccessPermission;
  id: string;
  workspace_id: string;
  name: string;
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
  permission?: WorkspaceAccessPermission;
  name: string;
  expires_at: string | null;
}

export interface UpdateWorkspaceAccessTokenRequest extends CreateWorkspaceAccessTokenRequest {
  version: number;
}

export interface RegenerateWorkspaceAccessTokenRequest {
  expires_at: string | null;
  version: number;
}
