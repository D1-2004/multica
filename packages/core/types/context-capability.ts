/**
 * Context capabilities: scene (DingTalk group chat or 1:1 chat) and personal
 * connector/skill layers on top of an agent's global grants. See
 * docs/context-capabilities.md for the product contract. Wire JSON is
 * snake_case; these types are the camelCase shapes the API client returns.
 */

/** Scope a mobile user can configure. `offer` is admin-only and never
 * appears in mobile responses. */
export type ContextScopeType = "scene" | "person";

export type ContextResourceType = "connector" | "skill";

/** Kind of an IM scene: a DingTalk group chat or a 1:1 chat (a 1:1 chat is a
 * scene exactly like a group). Older backends only knew group scenes, so a
 * missing kind parses as "group". */
export type ContextSceneKind = "group" | "dm";

/** How the caller obtained the right to configure a scope. Kept as a plain
 * string so a new server-side source does not break parsing. */
export type ContextGrantSource = "agent_link" | "jsapi" | (string & {});

/** One binding of a library resource inside a scene or personal scope. */
export interface ContextCapabilityBinding {
  resourceType: ContextResourceType;
  resourceId: string;
  enabled: boolean;
  /** Person-scope connector bindings only: the person also allows this
   * connector in group chats when they trigger the run ("在群聊中由我触发时也
   * 可用"). false unless the server sent a literal true. */
  shareInGroups: boolean;
}

/** How a stored scene or personal credential was obtained. Unknown kinds
 * from a newer backend parse as "unknown". */
export type ContextCredentialKind = "bearer" | "oauth" | "unknown";

/** Write-only credential metadata. The secret itself is never returned. */
export interface ContextConnectorCredential {
  connectorId: string;
  /** Masked hint such as "••••abcd", or "@octocat" / "OAuth" for an OAuth
   * connection. Never token material. */
  hint: string;
  updatedAt: string;
  /** Older backends omit the kind; their credentials are always Bearer. */
  kind: ContextCredentialKind;
}

/** Authentication mode of an offered connector. */
export type ContextConnectorAuthMode = "none" | "bearer" | "oauth" | "unknown";

/** A live grant that lets the caller configure one scope of an agent. */
export interface ContextConfigGrant {
  scopeType: ContextScopeType;
  scopeKey: string;
  scopeTitle: string;
  source: ContextGrantSource;
  expiresAt: string;
}

/** Scene grant as returned inside agent detail (scope type is implied). */
export interface ContextConfigSceneGrant {
  scopeKey: string;
  scopeTitle: string;
  source: ContextGrantSource;
  expiresAt: string;
  /** Group chat or 1:1 chat. */
  kind: ContextSceneKind;
}

export interface ContextConfigRedeemResult {
  agentId: string;
  workspaceId: string;
  scopeType: ContextScopeType | null;
  scopeKey: string;
  scopeTitle: string;
}

export interface ContextConfigAgentSummary {
  id: string;
  name: string;
  avatarUrl: string | null;
  workspaceId: string;
  scopes: ContextConfigGrant[];
}

export interface ContextConfigAgentIdentity {
  id: string;
  name: string;
  avatarUrl: string | null;
  workspaceId: string;
}

export interface ContextGlobalConnector {
  id: string;
  name: string;
  /** Official app catalog slug ("github", ...), "" for custom connectors. */
  catalogSlug: string;
}

export interface ContextSkillItem {
  id: string;
  name: string;
  description: string;
}

export interface ContextOfferedConnector {
  id: string;
  name: string;
  tools: string[];
  /** The connector authenticates with a bearer token, so a scene or
   * personal credential can be stored for it. */
  acceptsCredential: boolean;
  /** No workspace credential is ready: the connector only works in this
   * scope once a scene or personal credential is set. */
  credentialRequired: boolean;
  /** Official app catalog slug ("github", ...), "" for custom connectors. */
  catalogSlug: string;
  /** "oauth" connectors are connected through the provider's sign-in. */
  authMode: ContextConnectorAuthMode;
  /** An OAuth connector that also accepts a Personal Access Token (GitHub). */
  acceptsPat: boolean;
  /** The server can run this connector's OAuth sign-in (for GitHub it needs
   * the GitHub App client credentials). false only when the server says so;
   * an older backend that sends nothing keeps the connect action. */
  oauthAvailable: boolean;
  /** Optional provider page where users grant the app access to their
   * resources (GitHub App installation). "" when the server sends none. */
  installUrl: string;
}

export interface ContextPersonScope {
  scopeKey: string;
  scopeTitle: string;
  source: ContextGrantSource;
  expiresAt: string;
  bindings: ContextCapabilityBinding[];
  credentials: ContextConnectorCredential[];
}

export interface ContextConfigAgentDetail {
  agent: ContextConfigAgentIdentity;
  global: {
    connectors: ContextGlobalConnector[];
    skills: ContextSkillItem[];
  };
  offers: {
    connectors: ContextOfferedConnector[];
    skills: ContextSkillItem[];
  };
  person: ContextPersonScope | null;
  scenes: ContextConfigSceneGrant[];
  jsapiAvailable: boolean;
}

export interface ContextConfigSceneDetail {
  scene: ContextConfigSceneGrant;
  bindings: ContextCapabilityBinding[];
  credentials: ContextConnectorCredential[];
}

export interface SetContextCapabilityBindingInput {
  scopeType: ContextScopeType;
  scopeKey: string;
  resourceType: ContextResourceType;
  resourceId: string;
  enabled: boolean;
  /** Person scope + connector only; omitted leaves the stored value. */
  shareInGroups?: boolean;
}

export interface SetContextConnectorCredentialInput {
  scopeType: ContextScopeType;
  scopeKey: string;
  connectorId: string;
  bearer: string;
}

export interface DeleteContextConnectorCredentialInput {
  scopeType: ContextScopeType;
  scopeKey: string;
  connectorId: string;
}

/** Starts connecting an OAuth connector for one scope. `returnTo` must be on
 * the app origin; the server defaults to the configuration page. */
export interface StartContextConnectorConnectionInput {
  scopeType: ContextScopeType;
  scopeKey: string;
  connectorId: string;
  returnTo?: string;
}

/** JSAPI group picker result forwarded to the server. At least one id is
 * required; the server converts `chatId` itself. */
export interface ResolveContextConfigSceneInput {
  chatId?: string;
  openConversationId?: string;
}

/** `dd.config` signature parameters for the DingTalk H5 JSAPI. */
export interface DingTalkJsapiConfig {
  corpId: string;
  agentId: string;
  timeStamp: string;
  nonceStr: string;
  signature: string;
}

// ---------------------------------------------------------------------------
// Admin (agent detail → Skills / Connectors offer sections, Scenes section)
// ---------------------------------------------------------------------------

export interface ContextLibraryConnector {
  id: string;
  name: string;
  enabled: boolean;
  authMode: string;
  /** Official app slug ("github", ...) or "" for an Aone FaaS connector. */
  catalogSlug: string;
}

export interface ContextScopeSummary {
  scopeKey: string;
  scopeTitle: string;
  bindings: ContextCapabilityBinding[];
  credentialCount: number;
}

export interface AgentContextCapabilities {
  /** Server feature flag `context_capabilities`. */
  enabled: boolean;
  library: {
    connectors: ContextLibraryConnector[];
    skills: ContextSkillItem[];
  };
  offers: {
    connectorIds: string[];
    skillIds: string[];
  };
  scenes: ContextScopeSummary[];
  persons: ContextScopeSummary[];
  configureUrl: string;
}

export interface SetAgentContextCapabilityOffersInput {
  connectorIds: string[];
  skillIds: string[];
}

// ---------------------------------------------------------------------------
// Admin scenes (agent detail → 场域 section)
// ---------------------------------------------------------------------------

/** One IM scene of an agent (a DingTalk group chat or 1:1 chat) as listed
 * by `GET /api/agents/{id}/scenes`. */
export interface AgentSceneSummary {
  /** openConversationId of the conversation. */
  sceneKey: string;
  kind: ContextSceneKind;
  title: string;
  orgId: string;
  /** "" when the server reported no activity time. */
  lastActiveAt: string;
  /** Latest inbound Coordinator chat session of the conversation, "" when
   * there is none. */
  inboundSessionId: string;
  inboundCount: number;
  /** scene_memory row id, "" when the scene has no memory. */
  memoryId: string;
  hasPrompt: boolean;
  connectorCount: number;
  skillCount: number;
}

export interface AgentScenesPage {
  scenes: AgentSceneSummary[];
  hasMore: boolean;
}

/** Scene prompt (场域提示词). Stored per scene; not yet applied at runtime. */
export interface AgentScenePrompt {
  text: string;
  /** "" when the scene has no stored prompt. */
  updatedAt: string;
  updatedByName: string;
}

/** A scene binding as the admin sees it, with who changed it last. */
export interface AgentSceneBinding {
  resourceType: ContextResourceType;
  resourceId: string;
  enabled: boolean;
  updatedByName: string;
  updatedAt: string;
}

export interface AgentSceneOfferedConnector {
  id: string;
  name: string;
  /** Official app catalog slug ("github", ...), "" for custom connectors. */
  catalogSlug: string;
  authMode: ContextConnectorAuthMode;
}

export interface AgentSceneDetail {
  scene: AgentSceneSummary;
  prompt: AgentScenePrompt;
  bindings: AgentSceneBinding[];
  offers: {
    connectors: AgentSceneOfferedConnector[];
    skills: ContextSkillItem[];
  };
}

export interface ListAgentScenesParams {
  limit?: number;
  offset?: number;
}

export interface SetAgentSceneBindingInput {
  resourceType: ContextResourceType;
  resourceId: string;
  enabled: boolean;
}
