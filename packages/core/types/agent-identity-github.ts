export interface AgentIdentityGitHubConnection {
  connectionId: string;
  accountLogin: string;
  accountId: string;
  status: string;
  grantedScopes: string;
  accessExpiresAt?: number | null;
  refreshExpiresAt?: number | null;
  lastRefreshAt?: number | null;
  lastTestAt?: number | null;
}

export interface AgentIdentityGitHubStatusResponse {
  configured: boolean;
  connection: AgentIdentityGitHubConnection | null;
}

export interface BeginAgentIdentityGitHubOAuthResponse {
  state: string;
  authorizationUrl: string;
}

export interface TestAgentIdentityGitHubConnectionResponse {
  ok: boolean;
  refreshed: boolean;
  connectionId?: string;
  accountLogin?: string;
  accountId?: string;
  grantedScopes?: string;
}
