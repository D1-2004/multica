/**
 * Context capabilities: scene (DingTalk group chat or 1:1 chat) and personal
 * connector/skill layers on top of an agent's global grants. See
 * docs/context-capabilities.md for the product contract. Wire JSON is
 * snake_case; these types are the camelCase shapes the API client returns.
 */

/** Scope a mobile user can configure. `offer` is admin-only and never
 * appears in mobile responses. */
export type ContextScopeType = "scene" | "person";

/** Scope of a configuration-page write: a scene, a person, or (managers
 * only) the enterprise level of the tenant. */
export type ContextWriteScopeType = ContextScopeType | "org";

export type ContextResourceType = "connector" | "skill";

/** Kind of an IM scene: a DingTalk group chat or a 1:1 chat (a 1:1 chat is a
 * scene exactly like a group, with its own configuration). Older backends
 * only knew group scenes, so a missing kind parses as "group". */
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
  /** The scene_id of a scene (a group or 1:1 chat), the person key of a
   * person (a staffId, or "odt:" + an openDingTalkId). */
  scopeKey: string;
  scopeTitle: string;
  source: ContextGrantSource;
  expiresAt: string;
}

/** Scene grant as returned inside agent detail (scope type is implied). */
export interface ContextConfigSceneGrant {
  /** The scene_id (docs/agent-scene.md), sent back as-is on every scene
   * path and write. */
  scopeKey: string;
  scopeTitle: string;
  source: ContextGrantSource;
  expiresAt: string;
  /** Group chat or 1:1 chat. */
  kind: ContextSceneKind;
  /** DingTalk org (tenant) of the scene; "" when the server does not say,
   * which means the agent's own org. Sent back on every write. */
  orgId: string;
}

export interface ContextConfigRedeemResult {
  agentId: string;
  workspaceId: string;
  scopeType: ContextScopeType | null;
  scopeKey: string;
  scopeTitle: string;
  /** Tenant (DingTalk org) of the granted scope; "" when the server does not
   * say. The page opens that tenant. */
  orgId: string;
  /** The 1:1 chat (scene_id) a person link was minted in, also granted; ""
   * for any other link or an older backend. */
  extraSceneId: string;
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

/** What the caller may change in one configure-page scope, decided by the
 * server's single rights function. Each is true only when the server sent a
 * literal true. */
export interface ContextScopeRights {
  /** Switch offered connectors and skills on or off. */
  toggle: boolean;
  /** Store, remove or connect accounts and tokens. */
  connect: boolean;
  /** Add, edit, delete and switch the scope's prompt components. */
  editPrompts: boolean;
  /** Add, edit, delete and switch the scope's remote MCP servers. */
  editMcp: boolean;
  /** Create, edit, run and delete the scene's routines (例行任务). Only group
   * and 1:1 chat scenes have routines. */
  editRoutines: boolean;
}

/** A configure-page scope's own prompt components and MCP servers, and what
 * the caller may change there. */
export interface ContextConfigScopeContent {
  /** null from an older backend: the page then falls back to `canEdit` /
   * `canConnect` and offers no prompt or MCP editing. */
  rights: ContextScopeRights | null;
  /** This scope's own prompt components in merge order. */
  prompts: ContextPromptComponent[];
  /** This scope's own MCP servers (`{"mcpServers": {...}}`), null when none
   * or withheld. */
  mcpConfig: Record<string, unknown> | null;
  /** The workspace redacts secrets, so an existing `mcpConfig` is withheld
   * (null). The page must not save over it. */
  mcpConfigRedacted: boolean;
}

/** Scope of a configure-page write. */
export interface ContextConfigScopeInput {
  scopeType: ContextWriteScopeType;
  scopeKey: string;
  /** Tenant of the scope; omitted or "" means the agent's own org. */
  orgId?: string;
}

export interface ContextPersonScope extends ContextConfigScopeContent {
  scopeKey: string;
  scopeTitle: string;
  source: ContextGrantSource;
  expiresAt: string;
  bindings: ContextCapabilityBinding[];
  credentials: ContextConnectorCredential[];
}

/** A tenant (企业) of the agent on the configuration page. */
export interface ContextConfigTenantRef {
  orgId: string;
  name: string;
  /** "identity" for the agent's own DingTalk org. */
  source: string;
}

/** The enterprise level of the page's tenant: its switches and accounts.
 * Read-only unless `canEdit` (the caller manages the agent). */
export interface ContextConfigOrgScope extends ContextConfigScopeContent {
  /** The OrgId. */
  scopeKey: string;
  scopeTitle: string;
  bindings: ContextCapabilityBinding[];
  credentials: ContextConnectorCredential[];
  canEdit: boolean;
}

/** An official app of the connector catalog (GitHub, Notion, ...). */
/** How an official app's sign-in gets ready: by itself ("automatic":
 * dynamic registration or a deployment client), after the workspace saves
 * an OAuth application ("oauth_app"), or not at all ("unsupported"). Unknown
 * values from a newer backend read as "automatic". */
export type ContextConfigAppSetup = "automatic" | "oauth_app" | "unsupported";

export interface ContextConfigCatalogApp {
  slug: string;
  name: string;
  setup: ContextConfigAppSetup;
  /** A sign-in can start on this deployment now. */
  ready: boolean;
}

/** One value of a scene's OAuth application: "client_id" or "client_secret". */
export interface ContextConfigOAuthAppField {
  key: string;
  optional: boolean;
  /** Pasted as a file's content. */
  file: boolean;
}

/** A scene's own OAuth application of an app without dynamic registration
 * (Slack, Asana, GitHub), as the configure page shows it. Connections at
 * the scene sign in with it; without one the scene uses the workspace's
 * (saved in the admin console). Secrets are never returned. */
export interface ContextConfigOAuthApp {
  slug: string;
  name: string;
  fields: ContextConfigOAuthAppField[];
  /** Where the OAuth application is created in the provider's console. */
  docsUrl: string;
  /** The callback URL to register there. */
  callbackUrl: string;
  /** The scene has its own. */
  saved: boolean;
  clientId: string;
  clientSecretSet: boolean;
  /** Without its own, the scene signs in with the workspace's application. */
  workspaceReady: boolean;
  /** A sign-in can start at the scene now. */
  ready: boolean;
  /** The caller may save it now: the first one with the scene's connect
   * right, a saved one only as the agent's manager. */
  canEdit: boolean;
}

/** Saves a scene's OAuth application; an omitted secret keeps the stored one. */
export interface ContextConfigOAuthAppInput {
  clientId: string;
  clientSecret?: string;
}

/** Adds an official app at a level from the configure page. */
export interface AddContextConfigAppInput {
  slug: string;
  scopeType: ContextWriteScopeType;
  scopeKey: string;
  /** Tenant of the scope; omitted or "" means the agent's own org. */
  orgId?: string;
}

export interface AddContextConfigAppResult {
  connectorId: string;
  /** The agent's own connector: on at every level already. */
  defaultOn: boolean;
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
  /** The tenant this detail describes (the requested one, else the agent's
   * own org); null when the server names none. Person and enterprise
   * writes carry its OrgId. */
  tenant: ContextConfigTenantRef | null;
  /** The tenants the caller may open. */
  tenants: ContextConfigTenantRef[];
  /** The tenant's enterprise level, read-only unless the caller manages the
   * agent; null when there is none (and from older backends, for a caller
   * who does not manage the agent). */
  org: ContextConfigOrgScope | null;
  jsapiAvailable: boolean;
  /** "manager" when the caller manages the agent (scenes then lists every
   * scene of the agent); "grant" otherwise and from older backends. */
  access: ContextConfigAccess;
  /** Every official app the deployment supports, in catalog order. An app
   * is usable here only once `global` or `offers` lists its connector;
   * older backends send none. */
  apps: ContextConfigCatalogApp[];
}

/** Where the configuration of one scene page lives: the scene itself, a
 * group chat or a 1:1 chat alike (key = its scene_id). */
export interface ContextSceneScope {
  type: ContextScopeType;
  key: string;
  /** The chat's title. "" when unknown. */
  title: string;
}

export interface ContextConfigSceneDetail extends ContextConfigScopeContent {
  scene: ContextConfigSceneGrant;
  bindings: ContextCapabilityBinding[];
  credentials: ContextConnectorCredential[];
  /** Where these bindings and credentials live: the scene itself. Older
   * backends send no scope (the scene itself too); null when the value is
   * malformed. */
  scope: ContextSceneScope | null;
  /** The caller may store, remove or connect credentials in `scope`. null
   * when an older backend does not say. */
  canConnect: boolean | null;
  /** Apps (catalog slugs) the scene signs in to with its own OAuth
   * application; [] from an older backend. */
  sceneOAuthApps: string[];
}

export interface SetContextCapabilityBindingInput {
  scopeType: ContextWriteScopeType;
  scopeKey: string;
  /** Tenant of the scope; omitted or "" means the agent's own org. */
  orgId?: string;
  resourceType: ContextResourceType;
  resourceId: string;
  enabled: boolean;
  /** Person scope + connector only; omitted leaves the stored value. */
  shareInGroups?: boolean;
}

export interface SetContextConnectorCredentialInput {
  scopeType: ContextWriteScopeType;
  scopeKey: string;
  /** Tenant of the scope; omitted or "" means the agent's own org. */
  orgId?: string;
  connectorId: string;
  bearer: string;
}

export interface DeleteContextConnectorCredentialInput {
  scopeType: ContextWriteScopeType;
  scopeKey: string;
  /** Tenant of the scope; omitted or "" means the agent's own org. */
  orgId?: string;
  connectorId: string;
}

/** Starts connecting an OAuth connector for one scope. `returnTo` must be on
 * the app origin; the server defaults to the configuration page. */
export interface StartContextConnectorConnectionInput {
  scopeType: ContextWriteScopeType;
  scopeKey: string;
  /** Tenant of the scope; omitted or "" means the agent's own org. */
  orgId?: string;
  connectorId: string;
  returnTo?: string;
}

/** JSAPI group picker result forwarded to the server. At least one id is
 * required; the server converts `chatId` itself. */
export interface ResolveContextConfigSceneInput {
  chatId?: string;
  openConversationId?: string;
  /** The tenant the page works in ("" or omitted: the agent's own org).
   * The person grant must be there and the group is granted there. */
  orgId?: string;
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
  /** Enterprise (tenant) levels with configuration; `scopeKey` is the
   * OrgId. Empty from older backends. */
  orgs: ContextScopeSummary[];
  scenes: ContextScopeSummary[];
  persons: ContextScopeSummary[];
  configureUrl: string;
}

export interface SetAgentContextCapabilityOffersInput {
  connectorIds: string[];
  skillIds: string[];
}

// ---------------------------------------------------------------------------
// Admin 场域 (agent detail → 场域 section): tenants, their group chats and
// people, and the Context Builder of each level
// ---------------------------------------------------------------------------

/** One Agent work scene (a DingTalk group chat or 1:1 chat, see
 * docs/agent-scene.md) as listed by
 * `GET /api/agents/{id}/tenants/{orgId}/groups`. */
export interface AgentSceneSummary {
  /** The scene's identity (a server-generated UUID): the key of its
   * Context Builder node, its Scene Memory and its relations. */
  sceneId: string;
  /** Same as sceneId (the server sends both). */
  sceneKey: string;
  /** DingTalk openConversationId of the chat, for display only; "" when
   * the server does not say. */
  conversationId: string;
  kind: ContextSceneKind;
  title: string;
  orgId: string;
  /** "" when the server reported no activity time. */
  lastActiveAt: string;
  /** Latest inbound Coordinator chat session of the conversation, "" when
   * there is none. */
  inboundSessionId: string;
  inboundCount: number;
  /** The scene_id when the scene has Scene Memory, "" otherwise. */
  memoryId: string;
  /** The scene has Scene Memory (opened by sceneId). */
  hasMemory: boolean;
  /** The scene has prompt components of its own. */
  hasPrompt: boolean;
}

export interface AgentScenesPage {
  scenes: AgentSceneSummary[];
  hasMore: boolean;
}

export interface ListAgentScenesParams {
  limit?: number;
  offset?: number;
}

/** Where a tenant comes from: created by a manager with its OrgId, or the
 * agent's own DingTalk identity org (which cannot be deleted). */
export type AgentTenantSource = "created" | "identity";

/** One enterprise (tenant) the agent serves, keyed by its DingTalk OrgId. */
export interface AgentTenant {
  orgId: string;
  name: string;
  source: AgentTenantSource;
  groupCount: number;
  personCount: number;
}

/** An org seen in scene or person data that has no tenant yet. Its
 * configuration does not apply until a tenant is created for it. */
export interface AgentUnassignedOrg {
  orgId: string;
  groupCount: number;
  personCount: number;
}

export interface AgentTenantsList {
  tenants: AgentTenant[];
  unassignedOrgs: AgentUnassignedOrg[];
}

export interface CreateAgentTenantInput {
  orgId: string;
  name: string;
}

/** A person known under a tenant: their personal level. Their 1:1 chat is
 * a scene of its own. */
export interface AgentTenantPerson {
  staffId: string;
  title: string;
  /** scene_id of the person's 1:1 chat scene, "" when unknown. */
  dmSceneKey: string;
  lastActiveAt: string;
}

/** Level of a Context Builder node. */
export type ContextNodeScopeType = "org" | "scene" | "person";

/** Layer an effective component comes from, nearest last. */
export type ContextLayer = "global" | "org" | "scene" | "person";

/** The layer of an effective component as the server names it: a known
 * ContextLayer, or the raw name of a level this build does not know yet
 * (levels may grow, e.g. departments), so the preview never drops it. */
export type ContextEffectiveLayer = ContextLayer | (string & {});

/** Address of one Context Builder node: the tenant itself (`org`, key =
 * OrgId), one of its scenes (`scene`, key = scene_id; a group chat or a 1:1
 * chat, each its own scope) or one of its people (`person`, key =
 * staffId). */
export interface ContextNodeRef {
  orgId: string;
  scopeType: ContextNodeScopeType;
  scopeKey: string;
}

export interface ContextNodeScope {
  type: ContextNodeScopeType;
  orgId: string;
  key: string;
  title: string;
}

/** One prompt component of a level. A lower level's component replaces an
 * upper one with the same name. */
export interface ContextPromptComponent {
  id: string;
  name: string;
  order: number;
  text: string;
  /** A disabled component takes no part in the merge (it does not replace
   * an upper one either). Older backends omit it: enabled. */
  enabled: boolean;
  updatedByName: string;
  updatedAt: string;
}

/** A prompt component as written: the list PUT replaces the whole list. */
export interface ContextPromptComponentInput {
  name: string;
  order: number;
  text: string;
  /** Omitted means enabled. */
  enabled?: boolean;
}

/** Credential of an offered connector in the node's scope. */
export interface ContextNodeConnectorCredential {
  connected: boolean;
  /** Hint ("@octocat", "OAuth", "••••abcd"); "" when not connected or not
   * shown to this caller. */
  account: string;
}

/** An offered or granted connector with this node's switch and credential. */
export interface ContextNodeConnector {
  id: string;
  name: string;
  /** Official app catalog slug ("github", ...), "" for custom connectors. */
  catalogSlug: string;
  authMode: ContextConnectorAuthMode;
  /** Takes a pasted token: a Bearer connector, or an official app that
   * allows a Personal Access Token. */
  acceptsCredential: boolean;
  /** An OAuth app that also accepts a Personal Access Token (GitHub). */
  acceptsPat: boolean;
  /** The server can run this app's OAuth sign-in. */
  oauthAvailable: boolean;
  /** Provider installation page (GitHub App), "" when none. */
  installUrl: string;
  /** Granted to the agent (通用能力): on at every level, which may only give
   * it its own account, never switch it. */
  global: boolean;
  /** Switched on at this level. */
  enabled: boolean;
  credential: ContextNodeConnectorCredential;
}

/** An offered skill with this node's switch. */
export interface ContextNodeSkill {
  id: string;
  name: string;
  description: string;
  enabled: boolean;
}

export interface ContextEffectivePrompt {
  name: string;
  text: string;
  layer: ContextEffectiveLayer;
  /** A nearer layer's component with the same name replaces this one. */
  overridden: boolean;
  /** That nearer layer, null when the server did not say which. */
  overriddenBy: ContextLayer | null;
}

export interface ContextEffectiveItem {
  id: string;
  name: string;
  layer: ContextEffectiveLayer;
}

export interface ContextEffectiveMcpServer {
  name: string;
  layer: ContextEffectiveLayer;
  overridden: boolean;
  overriddenBy: ContextLayer | null;
}

/** What a run at this node gets, as the runtime builds it: global, then
 * enterprise, then group or person. */
export interface ContextNodeEffective {
  prompts: ContextEffectivePrompt[];
  connectors: ContextEffectiveItem[];
  skills: ContextEffectiveItem[];
  mcpServers: ContextEffectiveMcpServer[];
}

export interface ContextNodeDetail {
  /** Where this node's configuration lives: the node's own scope. null
   * when the value is malformed. */
  scope: ContextNodeScope | null;
  /** The node's chat: the scene of a scene node, the 1:1 chat scene of a
   * person (its inbound session and memory). null for a tenant, a person
   * without a 1:1 chat, and from a server that does not say. */
  scene: AgentSceneSummary | null;
  prompts: ContextPromptComponent[];
  connectors: ContextNodeConnector[];
  skills: ContextNodeSkill[];
  /** Custom MCP servers of the node (the agent `mcp_config` document
   * shape), null when none. */
  mcpConfig: Record<string, unknown> | null;
  /** The workspace always redacts secrets, so an existing `mcpConfig` is
   * withheld (null). The page must not save over it. */
  mcpConfigRedacted: boolean;
  /** The caller may store, remove or connect credentials here (false for a
   * manager on someone's personal level). */
  canConnect: boolean;
  /** What the caller may change at this level; null when the server does
   * not say (the panel then reads a person level as its person's only). */
  rights: ContextScopeRights | null;
  effective: ContextNodeEffective;
}

export interface SetContextNodeBindingInput {
  resourceType: ContextResourceType;
  resourceId: string;
  enabled: boolean;
}

export interface SetContextNodeCredentialInput {
  connectorId: string;
  bearer: string;
}

export interface StartContextNodeConnectionInput {
  connectorId: string;
  /** Must be on the app origin; the server has a default. */
  returnTo?: string;
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
  /** The scene's scene_id. */
  sceneId: string;
  /** Same as sceneId (the server sends both). */
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

// ---------------------------------------------------------------------------
// Scene routines (例行任务): cron or webhook runs bound to one group or 1:1
// chat scene. Each run uses the scene's configuration; the server posts a
// start and an end notice into the scene.

/** "schedule", "once" or "webhook"; other values come from a newer backend and are
 * shown generically. */
export type ContextRoutineTriggerKind = "schedule" | "once" | "webhook" | (string & {});

export interface ContextRoutineTrigger {
  id: string;
  kind: ContextRoutineTriggerKind;
  /** One-shot execution instant (RFC3339), null for other triggers. */
  runAt: string | null;
  /** True once a one-shot trigger has admitted its execution. */
  consumed: boolean;
  /** Five-field cron expression; "" for other triggers. */
  cron: string;
  /** IANA timezone of the schedule; "" for a webhook. */
  timezone: string;
  /** Next timed run (ISO), null when paused or consumed. */
  nextRunAt: string | null;
  /** The next few scheduled runs (ISO), empty when paused or a webhook. */
  nextRuns: string[];
  /** The webhook URL with the token masked; "" for a schedule. */
  webhookUrlMasked: string;
  /** The full webhook URL, only right after it was minted (create, rotate). */
  webhookUrl: string;
}

export interface ContextRoutineRun {
  id: string;
  /** Autopilot run status ("running", "completed", "failed", "skipped", …). */
  status: string;
  /** "schedule", "webhook" or "manual". */
  source: string;
  failureReason: string;
  createdAt: string;
  completedAt: string | null;
}

export interface ContextRoutine {
  id: string;
  sceneId: string;
  /** "group" or "dm". */
  sceneKind: string;
  autopilotId: string;
  title: string;
  instructions: string;
  /** Runs on its trigger only when true. */
  enabled: boolean;
  /** Why the system paused it ("" when none). */
  pauseReason: string;
  trigger: ContextRoutineTrigger;
  lastRun: ContextRoutineRun | null;
  /** "member" (configure page) or "agent" (created from a conversation). */
  createdByType: string;
  createdAt: string;
  updatedAt: string;
}

/** Outcome of a create or edit. `updated` is true when a create matched a
 * routine of the scene with the same purpose and schedule and updated it. */
export interface ContextRoutineWriteResult {
  routine: ContextRoutine;
  updated: boolean;
}

export interface ContextRoutineInput {
  title: string;
  instructions: string;
  trigger: {
    kind: "schedule" | "once" | "webhook";
    run_at?: string;
    cron?: string;
    timezone?: string;
  };
}

/** Fields of an edit; omitted ones stay. A schedule may change its cron and
 * timezone; a one-shot may change its run_at before admission; the trigger kind never changes. */
export interface ContextRoutinePatch {
  title?: string;
  instructions?: string;
  enabled?: boolean;
  cron?: string;
  timezone?: string;
  run_at?: string;
}

/** Where a scene's routines are read and written: the configure page
 * (authorized by the caller's grant) or the admin Context Builder (a
 * workspace-scoped scene node). */
export type SceneRoutinesTarget =
  | { kind: "config"; agentId: string; sceneId: string; orgId: string }
  | { kind: "node"; wsId: string; agentId: string; node: ContextNodeRef };
