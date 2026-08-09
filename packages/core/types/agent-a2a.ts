export type AgentA2AClientStatus = "active" | "disabled" | "revoked";
export type AgentA2ACredentialStatus = "active" | "revoked";
/** Methods implemented by the first inbound A2A slice. */
export type AgentA2AScope = "send" | "read";

export interface AgentA2ASecurityRequirement {
  schemes: Record<string, string[]>;
  [key: string]: unknown;
}

/** Public skill metadata embedded in the standard A2A Agent Card. */
export interface AgentA2ACardSkill {
  id: string;
  name: string;
  description: string;
  tags: string[];
  examples?: string[];
  inputModes?: string[];
  outputModes?: string[];
  securityRequirements?: AgentA2ASecurityRequirement[];
  [key: string]: unknown;
}

export interface AgentA2ASupportedInterface {
  url: string;
  protocolBinding: string;
  protocolVersion: string;
  tenant?: string;
  [key: string]: unknown;
}

export interface AgentA2ACapabilities {
  streaming?: boolean;
  pushNotifications?: boolean;
  extendedAgentCard?: boolean;
  extensions?: unknown[];
  [key: string]: unknown;
}

/**
 * Standard A2A Agent Card JSON. Unknown standard extensions are preserved so
 * exporting a Card never strips fields introduced by a newer server.
 */
export interface AgentA2AAgentCard {
  name: string;
  description: string;
  supportedInterfaces: AgentA2ASupportedInterface[];
  version: string;
  capabilities: AgentA2ACapabilities;
  securitySchemes?: Record<string, unknown>;
  securityRequirements?: AgentA2ASecurityRequirement[];
  defaultInputModes: string[];
  defaultOutputModes: string[];
  skills: AgentA2ACardSkill[];
  [key: string]: unknown;
}

export interface AgentA2AEndpoint {
  enabled: boolean;
  publicAgentId: string;
  cardName: string;
  cardDescription: string;
  cardVersion: string;
  cardSkills: AgentA2ACardSkill[];
  cardUrl: string;
  rpcUrl: string;
  protocolVersion: string;
  id?: string;
  agentId?: string;
  delegatedByUserId?: string;
  createdAt?: string;
  updatedAt?: string;
}

export interface AgentA2ACredential {
  id: string;
  keyId: string;
  tokenPrefix: string;
  status: AgentA2ACredentialStatus;
  expiresAt: string | null;
  lastUsedAt: string | null;
  revokedAt: string | null;
  createdAt: string;
  updatedAt: string;
}

export interface AgentA2AClient {
  id: string;
  name: string;
  status: AgentA2AClientStatus;
  scopes: AgentA2AScope[];
  rateLimitPerMinute: number | null;
  maxConcurrentTasks: number | null;
  credentials: AgentA2ACredential[];
  createdAt: string;
  updatedAt: string;
  revokedAt: string | null;
}

export interface AgentA2AConfig {
  endpoint: AgentA2AEndpoint | null;
  agentCard: AgentA2AAgentCard | null;
  clients: AgentA2AClient[];
}

export interface AgentA2ACredentialSecretResponse {
  credential: AgentA2ACredential;
  token: string;
}

export interface UpdateAgentA2AConfigRequest {
  enabled: boolean;
  cardName: string;
  cardDescription: string;
  cardVersion: string;
  cardSkills: AgentA2ACardSkill[];
}

export interface CreateAgentA2AClientRequest {
  name: string;
  scopes?: AgentA2AScope[];
}

export interface UpdateAgentA2AClientRequest {
  name?: string;
  status?: AgentA2AClientStatus;
  scopes?: AgentA2AScope[];
}

export interface CreateAgentA2ACredentialRequest {
  expiresAt?: string | null;
}
