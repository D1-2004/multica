export type AgentA2AClientStatus = "active" | "disabled" | "revoked";
export type AgentA2ACredentialStatus = "active" | "revoked";
/** Independently assignable permissions for the inbound A2A endpoint. */
export type AgentA2AScope = "send" | "read" | "list" | "cancel";

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
  /** Canonical header-authenticated MCP endpoint; absent on older servers. */
  mcpUrl?: string;
  /** Workspace MCP endpoint disclosed while the replacement flag is enabled. */
  workspaceMcpUrl?: string;
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
  rateLimitPerMinute?: number | null;
  maxConcurrentTasks?: number | null;
}

export interface UpdateAgentA2AClientRequest {
  name?: string;
  status?: AgentA2AClientStatus;
  scopes?: AgentA2AScope[];
  rateLimitPerMinute?: number | null;
  maxConcurrentTasks?: number | null;
}

export interface CreateAgentA2ACredentialRequest {
  expiresAt?: string | null;
}

/**
 * The Agent's DingTalk identity. It is the same record as the Integrations
 * DingTalk identity; `a2aEnabled` says whether A2A turns may run as it.
 */
export interface AgentA2AOperatorIdentity {
  uid: string;
  orgId: string;
  displayName: string;
  organizationName: string;
  deapAgentUuid: string | null;
  a2aEnabled: boolean;
  boundAt: string;
}

/**
 * Pre-release: one production registry. `current` means the registry holds
 * this Agent's present identity; a registration that is not current no longer
 * receives traffic.
 */
export interface AgentA2AProdForwardRegistration {
  registry: string;
  registeredAt: string | null;
  current: boolean;
  error: string;
}

/** Pre-release: whether this Agent accepts production forwards for its identity. */
export interface AgentA2AProdForward {
  accept: boolean;
  /** Why the Agent cannot register yet while `accept` is on; empty when it can. */
  blockedReason: string;
  registrations: AgentA2AProdForwardRegistration[];
}

/** Production: the pre-release Agent currently registered for this Agent's identity. */
export interface AgentA2AForwardTarget {
  rpcUrl: string;
  agentName: string;
  registeredAt: string;
}

/**
 * Deployment operator settings for one Agent. Non-operators always receive
 * `operator: false` and no settings.
 */
export interface AgentA2AOperatorConfig {
  operator: boolean;
  dwsIdentity: AgentA2AOperatorIdentity | null;
  prodForward: AgentA2AProdForward | null;
  forwardTarget: AgentA2AForwardTarget | null;
}

export interface UpdateAgentA2AOperatorIdentityRequest {
  uid: string;
  orgId: string;
  displayName?: string;
  organizationName?: string;
  deapAgentUuid?: string;
}

export interface UpdateAgentA2AProdForwardRequest {
  accept: boolean;
}
