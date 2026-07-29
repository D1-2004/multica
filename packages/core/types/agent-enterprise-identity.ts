export interface AgentEnterpriseIdentityConnection {
  employeeId: string;
  displayName: string;
  status: "active" | "needs_reauth" | "revoked" | string;
  aipId: string;
  agentSpiffeId: string;
  bucStatus: "active" | "needs_reauth" | "revoked" | string;
  agentIdentityStatus: "active" | "needs_reauth" | "revoked" | string;
  refreshExpiresAt?: number;
}

export interface AgentEnterpriseIdentityStatusResponse {
  configured: boolean;
  canManage: boolean;
  identity: AgentEnterpriseIdentityConnection | null;
}

export interface BeginAgentEnterpriseIdentityBindingResponse {
  authorizationUrl: string;
  expiresAt: number;
}

export interface TestAgentEnterpriseIdentityResponse {
  ok: boolean;
}
