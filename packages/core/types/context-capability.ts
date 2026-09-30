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

/** Why the caller may configure an agent on the configuration page: live
 * scene/person grants, or agent management (workspace owner/admin or the
 * agent owner), which covers every scene of the agent but not the personal
 * scope of another person. Older backends only listed granted agents, so a
 * missing value parses as "grant". */
export type ContextConfigAccess = "grant" | "manager";

export interface ContextConfigAgentSummary {
  id: string;
  name: string;
  avatarUrl: string | null;
  workspaceId: string;
  access: ContextConfigAccess;
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
  /** "manager" when the caller manages the agent (scenes then lists every
   * scene of the agent); "grant" otherwise and from older backends. */
  access: ContextConfigAccess;
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

// ---------------------------------------------------------------------------
// Admin connected apps (agent detail → 配置 → 连接器 → 连接应用)
// ---------------------------------------------------------------------------

export interface ConnectedAppTools {
  /** Tools the upstream server listed at the last discovery. */
  discovered: number;
  /** Tools currently exposed to agents (read-only unless writes are on). */
  allowed: number;
}

/** Where a connected shared account comes from: the stored workspace
 * credential (the page can disconnect it), the operator's deployment
 * environment (it cannot), or "" when not connected or not reported. */
export type ConnectedAppSharedAccountSource = "workspace" | "environment" | "";

/** The workspace shared account (所有人共用). `connected` is true only when
 * the workspace credential is usable. */
export interface ConnectedAppSharedAccount {
  connected: boolean;
  /** Display hint ("@octocat", "OAuth", "••••abcd"), "" when none, for an
   * environment credential and for callers who are not workspace admins. */
  account: string;
  source: ConnectedAppSharedAccountSource;
}

/** Server-computed use of an app by this agent's scenes and people.
 * `*Enabled` counts enabled bindings (已开启); `*Connected` counts stored
 * credentials at that scope (已连接). The two are independent. */
export interface ConnectedAppUsage {
  scenesEnabled: number;
  scenesConnected: number;
  personsEnabled: number;
  personsConnected: number;
}

/** One official app as the agent's connector tab sees it. */
export interface ConnectedApp {
  slug: string;
  name: string;
  /** The server can run the app's OAuth sign-in, so the start endpoint
   * accepts it. false unless the server sent a literal true. */
  oauthAvailable: boolean;
  /** A Personal Access Token can be saved for the app on this deployment
   * (GitHub, with credential storage configured). */
  allowsPat: boolean;
  /** Provider installation page (GitHub App), "" when none or unsafe. */
  installUrl: string;
  /** The workspace catalog connector, null while none exists. */
  connectorId: string | null;
  added: boolean;
  /** Workspace kill switch of the connector (`internal_connector.enabled`). */
  enabledInWorkspace: boolean;
  /** Granted to this agent: used for every user in every scene
   * (对所有用户启用). */
  globalEnabled: boolean;
  /** In this agent's offer catalog: groups and people may connect their own
   * accounts (允许群聊、个人连接自己的账号). */
  offered: boolean;
  writeEnabled: boolean;
  tools: ConnectedAppTools;
  sharedAccount: ConnectedAppSharedAccount;
  usage: ConnectedAppUsage;
}

export interface ConnectedAppsList {
  apps: ConnectedApp[];
  /** The caller is a workspace owner/admin: may add apps, grant them,
   * change their offers and manage the shared account. */
  canAdmin: boolean;
}

/** One scene's use of an app. */
export interface ConnectedAppSceneUsage {
  sceneKey: string;
  title: string;
  kind: ContextSceneKind;
  /** An enabled scene binding (已开启). */
  enabled: boolean;
  /** A stored scene credential (已连接). */
  connected: boolean;
  /** Credential hint ("@octocat", "OAuth", "••••abcd"), "" when none. */
  account: string;
}

/** One person's use of an app. */
export interface ConnectedAppPersonUsage {
  scopeKey: string;
  title: string;
  enabled: boolean;
  connected: boolean;
  account: string;
  /** 「在群聊中由我触发时也可用」. */
  shareInGroups: boolean;
}

export interface ConnectedAppTool {
  name: string;
  readOnly: boolean;
  /** Exposed to the agent right now. */
  allowed: boolean;
}

export interface ConnectedAppDetail extends ConnectedApp {
  scenes: ConnectedAppSceneUsage[];
  persons: ConnectedAppPersonUsage[];
  toolList: ConnectedAppTool[];
  /** Same as ConnectedAppsList.canAdmin, so the app page does not depend on
   * the list request. */
  canAdmin: boolean;
}
