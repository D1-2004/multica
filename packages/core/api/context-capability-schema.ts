import { z } from "zod";
import type {
  AgentContextCapabilities,
  AgentScenesPage,
  AgentSceneSummary,
  AgentTenant,
  AgentTenantPerson,
  AgentTenantsList,
  AgentUnassignedOrg,
  ConnectedApp,
  ConnectedAppDetail,
  ConnectedAppPersonUsage,
  ConnectedAppSceneUsage,
  ConnectedAppSharedAccountSource,
  ConnectedAppsList,
  ConnectedAppTool,
  ContextCapabilityBinding,
  ContextConfigAccess,
  ContextConfigAgentDetail,
  ContextConfigCatalogApp,
  ContextConfigOrgScope,
  ContextConfigScopeContent,
  ContextConfigTenantRef,
  ContextConfigAgentSummary,
  ContextConfigGrant,
  ContextConfigRedeemResult,
  ContextConfigSceneDetail,
  ContextConfigSceneGrant,
  ContextConnectorAuthMode,
  ContextConnectorCredential,
  ContextCredentialKind,
  ContextEffectiveItem,
  ContextEffectiveMcpServer,
  ContextEffectivePrompt,
  ContextLayer,
  ContextNodeConnector,
  ContextNodeDetail,
  ContextNodeScope,
  ContextNodeScopeType,
  ContextPromptComponent,
  ContextResourceType,
  ContextRoutine,
  ContextRoutineRun,
  ContextRoutineTrigger,
  ContextRoutineWriteResult,
  ContextSceneKind,
  ContextSceneScope,
  ContextScopeRights,
  ContextScopeType,
  DingTalkJsapiConfig,
} from "../types/context-capability";
import { safeExternalUrl } from "./internal-connector-schema";

// Context capability endpoints are new and served by Go handlers, where a nil
// slice marshals to `null` and optional strings may be omitted. Every list
// and display string therefore tolerates null/absent values, while ids and
// security-relevant booleans stay strict: a binding is only ever reported as
// enabled when the server sent a literal `true`.

const text = z
  .string()
  .nullish()
  .transform((value) => value ?? "");

const id = z.string().min(1);

function list<T extends z.ZodType>(item: T) {
  return z
    .array(item)
    .nullish()
    .transform((value): z.output<T>[] => value ?? []);
}

const strictTrue = z.unknown().transform((value) => value === true);

/** The OrgId a new tenant must have (the create-tenant rule). */
export const ORG_ID_PATTERN = /^[A-Za-z0-9_-]{1,64}$/;

const ORG_ID_FORBIDDEN = /[\s\p{Cc}]/u;

/**
 * Whether `value` is an OrgId the server reads and writes scopes under
 * (contextcap.ValidOrgID): non-empty, at most 256 UTF-8 bytes, without
 * whitespace or control characters. Looser than ORG_ID_PATTERN, because the
 * agent's own org and the orgs seen in chats are kept as DingTalk sent them.
 */
export function isOrgId(value: unknown): value is string {
  if (typeof value !== "string" || value === "" || ORG_ID_FORBIDDEN.test(value)) return false;
  let bytes = 0;
  for (const char of value) {
    const code = char.codePointAt(0) ?? 0;
    bytes += code < 0x80 ? 1 : code < 0x800 ? 2 : code < 0x10000 ? 3 : 4;
    if (bytes > 256) return false;
  }
  return true;
}

const orgIdString = z.string().refine(isOrgId);

const count = z
  .number()
  .int()
  .nonnegative()
  .nullish()
  .catch(0)
  .transform((value) => value ?? 0);

// Older backends only served group scenes and sent no kind; anything that is
// not literally "dm" is shown as a group chat.
const sceneKind = z
  .unknown()
  .transform((value): ContextSceneKind => (value === "dm" ? "dm" : "group"));

/** Drops malformed items instead of failing the whole list, so one bad row
 * from a newer backend never empties a page. */
function tolerantList<T extends z.ZodType>(item: T) {
  return z
    .array(z.unknown())
    .nullish()
    .catch(null)
    .transform((values): z.output<T>[] =>
      (values ?? []).flatMap((value) => {
        const parsed = item.safeParse(value);
        return parsed.success ? [parsed.data as z.output<T>] : [];
      }),
    );
}

function scopeTypeOf(value: string): ContextScopeType | null {
  return value === "scene" || value === "person" ? value : null;
}

function resourceTypeOf(value: string): ContextResourceType | null {
  return value === "connector" || value === "skill" ? value : null;
}

const SceneScopeWireSchema = z.object({ type: z.string(), key: id, title: text });

/** `scope` of a scene read: where the scene page's configuration lives,
 * the scene itself for a group and a 1:1 chat alike. A missing field (an
 * older backend) means the scene itself; a null or malformed value reads as
 * null. */
function sceneScopeOf(value: unknown, fallback: ContextSceneScope): ContextSceneScope | null {
  if (value === undefined) return fallback;
  const parsed = SceneScopeWireSchema.safeParse(value);
  if (!parsed.success) return null;
  const type = scopeTypeOf(parsed.data.type);
  return type ? { type, key: parsed.data.key, title: parsed.data.title } : null;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

const BindingWireSchema = z.object({
  resource_type: z.string(),
  resource_id: id,
  enabled: strictTrue,
  // Person connector bindings only; older backends omit it.
  share_in_groups: strictTrue,
});

type BindingWire = z.output<typeof BindingWireSchema>;

function toBinding(wire: BindingWire): ContextCapabilityBinding | null {
  const resourceType = resourceTypeOf(wire.resource_type);
  if (!resourceType) return null;
  return {
    resourceType,
    resourceId: wire.resource_id,
    enabled: wire.enabled,
    // Meaningful only for connectors; a skill binding never shares.
    shareInGroups: resourceType === "connector" && wire.share_in_groups,
  };
}

function toBindings(wires: BindingWire[]): ContextCapabilityBinding[] {
  return wires
    .map(toBinding)
    .filter((binding): binding is ContextCapabilityBinding => binding !== null);
}

const BindingListSchema = list(BindingWireSchema).transform(toBindings);

export const ContextCapabilityBindingResponseSchema = z
  .object({ binding: BindingWireSchema })
  .transform((value) => toBinding(value.binding));

// Older backends omit `kind`: every credential they store is a Bearer.
const credentialKind = z
  .string()
  .nullish()
  .transform((value): ContextCredentialKind => {
    if (value == null || value === "" || value === "bearer") return "bearer";
    return value === "oauth" ? "oauth" : "unknown";
  });

const CredentialWireSchema = z
  .object({
    connector_id: id,
    hint: text,
    updated_at: text,
    kind: credentialKind,
  })
  .transform(
    (value): ContextConnectorCredential => ({
      connectorId: value.connector_id,
      hint: value.hint,
      updatedAt: value.updated_at,
      kind: value.kind,
    }),
  );

// Older backends omit `auth_mode`; their only credential-bearing mode was
// Bearer, reported through `accepts_credential`.
function connectorAuthModeOf(
  value: string | null | undefined,
  acceptsCredential: boolean,
): ContextConnectorAuthMode {
  if (value == null || value === "") return acceptsCredential ? "bearer" : "none";
  if (value === "none" || value === "bearer" || value === "oauth") return value;
  return "unknown";
}

const catalogSlug = z
  .string()
  .nullish()
  .catch("")
  .transform((value) => (value && /^[a-z0-9][a-z0-9_-]{0,63}$/.test(value) ? value : ""));

export const ContextConnectorCredentialResponseSchema = z
  .object({ credential: CredentialWireSchema })
  .transform((value) => value.credential);

const PromptComponentWireSchema = z
  .object({
    id: text,
    name: z.string().min(1),
    order: z.number().int().nullish().catch(0).transform((value) => value ?? 0),
    text: text,
    // Older backends have no switch: every component is on. Only a literal
    // false turns one off.
    enabled: z.unknown().transform((value) => value !== false),
    updated_by_name: text,
    updated_at: text,
  })
  .transform(
    (value): ContextPromptComponent => ({
      id: value.id,
      name: value.name,
      order: value.order,
      text: value.text,
      enabled: value.enabled,
      updatedByName: value.updated_by_name,
      updatedAt: value.updated_at,
    }),
  );

/** Prompt components in the order the runtime composes them: by `order`,
 * then by name. */
function sortPrompts(prompts: ContextPromptComponent[]): ContextPromptComponent[] {
  return [...prompts].sort((a, b) => a.order - b.order || a.name.localeCompare(b.name));
}

/** `rights` of a scope: what the caller may change there. Each right is on
 * only for a literal true; a missing or malformed value reads as null (an
 * older backend), never as a guessed grant. */
const scopeRights = z
  .object({
    toggle: strictTrue,
    connect: strictTrue,
    edit_prompts: strictTrue,
    edit_mcp: strictTrue,
    edit_routines: strictTrue,
  })
  .transform(
    (value): ContextScopeRights => ({
      toggle: value.toggle,
      connect: value.connect,
      editPrompts: value.edit_prompts,
      editMcp: value.edit_mcp,
      editRoutines: value.edit_routines,
    }),
  )
  .nullish()
  .catch(null)
  .transform((value) => value ?? null);

/** Wire fields of a configure-page scope's own prompts and MCP servers. */
const scopeContentWire = {
  rights: scopeRights,
  prompts: tolerantList(PromptComponentWireSchema),
  mcp_config: z.unknown().optional(),
  mcp_config_redacted: strictTrue,
};

function toScopeContent(value: {
  rights: ContextScopeRights | null;
  prompts: ContextPromptComponent[];
  mcp_config?: unknown;
  mcp_config_redacted: boolean;
}): ContextConfigScopeContent {
  const redacted = value.mcp_config_redacted;
  return {
    rights: value.rights,
    prompts: sortPrompts(value.prompts),
    // A withheld document is never shown or saved over.
    mcpConfig: !redacted && isRecord(value.mcp_config) ? value.mcp_config : null,
    mcpConfigRedacted: redacted,
  };
}

// A DingTalk OrgId (corp id). Anything else reads as "" (the agent's own
// org), so a malformed value is never sent back as a scope.
const orgIdOf = z.unknown().transform((value) => (isOrgId(value) ? value : ""));

const SceneGrantSchema = z
  .object({
    scope_key: id,
    scope_title: text,
    source: text,
    expires_at: text,
    kind: sceneKind,
    org_id: orgIdOf,
  })
  .transform(
    (value): ContextConfigSceneGrant => ({
      scopeKey: value.scope_key,
      scopeTitle: value.scope_title,
      source: value.source,
      expiresAt: value.expires_at,
      kind: value.kind,
      orgId: value.org_id,
    }),
  );

const GrantWireSchema = z.object({
  scope_type: z.string(),
  scope_key: text,
  scope_title: text,
  source: text,
  expires_at: text,
});

type GrantWire = z.output<typeof GrantWireSchema>;

function toGrant(wire: GrantWire): ContextConfigGrant | null {
  const scopeType = scopeTypeOf(wire.scope_type);
  if (!scopeType || !wire.scope_key) return null;
  return {
    scopeType,
    scopeKey: wire.scope_key,
    scopeTitle: wire.scope_title,
    source: wire.source,
    expiresAt: wire.expires_at,
  };
}

export const ContextConfigRedeemSchema = z
  .object({
    agent_id: id,
    workspace_id: text,
    scope_type: z.string(),
    scope_key: text,
    scope_title: text,
    org_id: orgIdOf,
  })
  .transform(
    (value): ContextConfigRedeemResult => ({
      agentId: value.agent_id,
      workspaceId: value.workspace_id,
      scopeType: scopeTypeOf(value.scope_type),
      scopeKey: value.scope_key,
      scopeTitle: value.scope_title,
      orgId: value.org_id,
    }),
  );

export const EMPTY_CONTEXT_CONFIG_REDEEM: ContextConfigRedeemResult = {
  agentId: "",
  workspaceId: "",
  scopeType: null,
  scopeKey: "",
  scopeTitle: "",
  orgId: "",
};

const AgentSummaryWireSchema = z.object({
  id,
  name: text,
  avatar_url: z.string().nullish().transform((value) => value || null),
  workspace_id: text,
});

// Older backends listed granted agents only and sent no access; anything
// that is not literally "manager" is a grant-based entry.
const configAccess = z
  .unknown()
  .transform((value): ContextConfigAccess => (value === "manager" ? "manager" : "grant"));

export const ContextConfigAgentListSchema = z
  .object({
    agents: list(
      AgentSummaryWireSchema.extend({ scopes: list(GrantWireSchema), access: configAccess }),
    ),
  })
  .transform((value): ContextConfigAgentSummary[] => {
    // One entry per agent: an agent the caller both manages and holds grants
    // for keeps manager access and every grant.
    const byId = new Map<string, ContextConfigAgentSummary>();
    for (const agent of value.agents) {
      const scopes = agent.scopes
        .map(toGrant)
        .filter((grant): grant is ContextConfigGrant => grant !== null);
      const existing = byId.get(agent.id);
      if (existing) {
        existing.scopes.push(...scopes);
        if (agent.access === "manager") existing.access = "manager";
        continue;
      }
      byId.set(agent.id, {
        id: agent.id,
        name: agent.name,
        avatarUrl: agent.avatar_url,
        workspaceId: agent.workspace_id,
        access: agent.access,
        scopes,
      });
    }
    return [...byId.values()];
  });

const SkillItemSchema = z
  .object({ id, name: text, description: text })
  .transform((value) => ({
    id: value.id,
    name: value.name,
    description: value.description,
  }));

const GlobalConnectorSchema = z
  .object({ id, name: text, catalog_slug: catalogSlug })
  .transform((connector) => ({
    id: connector.id,
    name: connector.name,
    catalogSlug: connector.catalog_slug,
  }));

const TenantRefSchema = z
  .object({ org_id: orgIdString, name: text, source: text })
  .transform(
    (value): ContextConfigTenantRef => ({ orgId: value.org_id, name: value.name, source: value.source }),
  );

/** The enterprise level of the page's tenant. A malformed value reads as
 * none, so nothing is written to a guessed scope; only a literal true
 * allows editing it. */
const ConfigOrgScopeSchema = z
  .object({
    scope_key: orgIdString,
    scope_title: text,
    bindings: BindingListSchema,
    credentials: list(CredentialWireSchema),
    can_edit: strictTrue,
    ...scopeContentWire,
  })
  .transform(
    (value): ContextConfigOrgScope => ({
      scopeKey: value.scope_key,
      scopeTitle: value.scope_title,
      bindings: value.bindings,
      credentials: value.credentials,
      canEdit: value.can_edit,
      ...toScopeContent(value),
    }),
  );

const CatalogAppSchema = z
  .object({ slug: catalogSlug, name: text })
  .refine((app) => app.slug !== "")
  .transform((app): ContextConfigCatalogApp => ({ slug: app.slug, name: app.name || app.slug }));

export const ContextConfigAgentDetailSchema = z
  .object({
    agent: AgentSummaryWireSchema,
    global: z
      .object({
        connectors: list(GlobalConnectorSchema),
        skills: list(SkillItemSchema),
      })
      .nullish(),
    offers: z
      .object({
        connectors: list(
          z.object({
            id,
            name: text,
            tools: list(z.string()),
            accepts_credential: strictTrue,
            credential_required: strictTrue,
            catalog_slug: catalogSlug,
            auth_mode: z.string().nullish().catch(null),
            accepts_pat: strictTrue,
            oauth_available: strictTrue,
            install_url: z.unknown().optional(),
          }),
        ),
        skills: list(SkillItemSchema),
      })
      .nullish(),
    person: z
      .object({
        scope_key: id,
        scope_title: text,
        source: text,
        expires_at: text,
        bindings: BindingListSchema,
        credentials: list(CredentialWireSchema),
        ...scopeContentWire,
      })
      .nullish(),
    scenes: list(SceneGrantSchema),
    tenant: TenantRefSchema.nullish().catch(null),
    tenants: tolerantList(TenantRefSchema),
    org: ConfigOrgScopeSchema.nullish().catch(null),
    jsapi_available: strictTrue,
    access: configAccess,
    apps: tolerantList(CatalogAppSchema),
  })
  .transform(
    (value): ContextConfigAgentDetail => ({
      agent: {
        id: value.agent.id,
        name: value.agent.name,
        avatarUrl: value.agent.avatar_url,
        workspaceId: value.agent.workspace_id,
      },
      global: {
        connectors: value.global?.connectors ?? [],
        skills: value.global?.skills ?? [],
      },
      offers: {
        connectors: (value.offers?.connectors ?? []).map((connector) => {
          const authMode = connectorAuthModeOf(connector.auth_mode, connector.accepts_credential);
          // An OAuth connector holds scoped credentials through the provider
          // sign-in even when an older field says it takes no Bearer.
          const holdsCredential = connector.accepts_credential || authMode === "oauth";
          return {
            id: connector.id,
            name: connector.name,
            tools: connector.tools,
            acceptsCredential: connector.accepts_credential,
            // A connector that cannot hold a scoped credential can never be
            // "waiting for" one, whatever the server says.
            credentialRequired: holdsCredential && connector.credential_required,
            catalogSlug: connector.catalog_slug,
            authMode,
            // A Personal Access Token is only an alternative to OAuth.
            acceptsPat: authMode === "oauth" && connector.accepts_pat,
            // Only a literal true shows the connect action, so the page never
            // offers a sign-in the start endpoint would reject.
            oauthAvailable: authMode === "oauth" && connector.oauth_available,
            installUrl: safeExternalUrl(connector.install_url),
          };
        }),
        skills: value.offers?.skills ?? [],
      },
      person: value.person
        ? {
            scopeKey: value.person.scope_key,
            scopeTitle: value.person.scope_title,
            source: value.person.source,
            expiresAt: value.person.expires_at,
            bindings: value.person.bindings,
            credentials: value.person.credentials,
            ...toScopeContent(value.person),
          }
        : null,
      scenes: value.scenes,
      tenant: value.tenant ?? null,
      tenants: [...new Map(value.tenants.map((tenant) => [tenant.orgId, tenant])).values()],
      org: value.org ?? null,
      jsapiAvailable: value.jsapi_available,
      access: value.access,
      apps: [...new Map(value.apps.map((app) => [app.slug, app])).values()],
    }),
  );

export const ContextConfigSceneDetailSchema = z
  .object({
    scene: SceneGrantSchema,
    bindings: BindingListSchema,
    credentials: list(CredentialWireSchema),
    scope: z.unknown().optional(),
    can_connect: z.unknown().optional(),
    ...scopeContentWire,
  })
  .transform(
    (value): ContextConfigSceneDetail => ({
      scene: value.scene,
      bindings: value.bindings,
      credentials: value.credentials,
      ...toScopeContent(value),
      scope: sceneScopeOf(value.scope, {
        type: "scene",
        key: value.scene.scopeKey,
        title: value.scene.scopeTitle,
      }),
      // Only a literal true allows connecting; an older backend sends none.
      canConnect: value.can_connect === undefined ? null : value.can_connect === true,
    }),
  );

export const ContextConfigSceneResolveSchema = z
  .object({ scene: SceneGrantSchema })
  .transform((value) => value.scene);

const stringOrNumber = z
  .union([z.string(), z.number()])
  .transform((value) => String(value))
  .pipe(z.string().min(1));

export const DingTalkJsapiConfigSchema = z
  .object({
    corp_id: z.string().min(1),
    agent_id: stringOrNumber,
    time_stamp: stringOrNumber,
    nonce_str: z.string().min(1),
    signature: z.string().min(1),
  })
  .transform(
    (value): DingTalkJsapiConfig => ({
      corpId: value.corp_id,
      agentId: value.agent_id,
      timeStamp: value.time_stamp,
      nonceStr: value.nonce_str,
      signature: value.signature,
    }),
  );

const ScopeSummarySchema = z
  .object({
    scope_key: id,
    scope_title: text,
    bindings: BindingListSchema,
    credential_count: z
      .number()
      .int()
      .nonnegative()
      .nullish()
      .transform((value) => value ?? 0),
  })
  .transform((value) => ({
    scopeKey: value.scope_key,
    scopeTitle: value.scope_title,
    bindings: value.bindings,
    credentialCount: value.credential_count,
  }));

export const AgentContextCapabilitiesSchema = z
  .object({
    enabled: strictTrue,
    library: z
      .object({
        connectors: list(
          z.object({
            id,
            name: text,
            enabled: strictTrue,
            auth_mode: text,
            catalog_slug: catalogSlug,
          }),
        ),
        skills: list(SkillItemSchema),
      })
      .nullish(),
    offers: z
      .object({
        connector_ids: list(id),
        skill_ids: list(id),
      })
      .nullish(),
    // Enterprise levels with configuration; older backends omit it.
    orgs: list(ScopeSummarySchema),
    scenes: list(ScopeSummarySchema),
    persons: list(ScopeSummarySchema),
    configure_url: text,
  })
  .transform(
    (value): AgentContextCapabilities => ({
      enabled: value.enabled,
      library: {
        connectors: (value.library?.connectors ?? []).map((connector) => ({
          id: connector.id,
          name: connector.name,
          enabled: connector.enabled,
          authMode: connector.auth_mode,
          catalogSlug: connector.catalog_slug,
        })),
        skills: value.library?.skills ?? [],
      },
      offers: {
        connectorIds: value.offers?.connector_ids ?? [],
        skillIds: value.offers?.skill_ids ?? [],
      },
      orgs: value.orgs,
      scenes: value.scenes,
      persons: value.persons,
      configureUrl: value.configure_url,
    }),
  );

// ---------------------------------------------------------------------------
// Admin 场域: tenants, their groups and people, and Context Builder nodes
// (/api/agents/{id}/tenants...)
// ---------------------------------------------------------------------------

/** A scene's identity: scene_id, else scene_key (which carries the same
 * scene_id). A row naming neither is dropped. */
const sceneIdentity = {
  scene_id: text.catch(""),
  scene_key: text.catch(""),
};

function sceneIdOf(value: { scene_id: string; scene_key: string }): string {
  return value.scene_id || value.scene_key;
}

const hasSceneId = (value: { scene_id: string; scene_key: string }) => sceneIdOf(value) !== "";

/** One Agent work scene (docs/agent-scene.md). The conversation id is
 * display-only; memory is known from has_memory or a memory_id, and opened
 * by the scene_id. */
const AgentSceneSummaryWireSchema = z
  .object({
    ...sceneIdentity,
    conversation_id: text.catch(""),
    kind: sceneKind,
    title: text,
    org_id: text,
    last_active_at: text,
    inbound_session_id: text,
    inbound_count: count,
    memory_id: text.catch(""),
    has_memory: strictTrue,
    has_prompt: strictTrue,
  })
  .refine(hasSceneId)
  .transform((value): AgentSceneSummary => {
    const sceneId = sceneIdOf(value);
    const hasMemory = value.has_memory || value.memory_id !== "";
    return {
      sceneId,
      sceneKey: sceneId,
      conversationId: value.conversation_id,
      kind: value.kind,
      title: value.title,
      orgId: value.org_id,
      lastActiveAt: value.last_active_at,
      inboundSessionId: value.inbound_session_id,
      inboundCount: value.inbound_count,
      memoryId: value.memory_id || (hasMemory ? sceneId : ""),
      hasMemory,
      hasPrompt: value.has_prompt,
    };
  });

export const EMPTY_AGENT_SCENES_PAGE: AgentScenesPage = { scenes: [], hasMore: false };

export const AgentScenesPageSchema = z
  .object({
    scenes: tolerantList(AgentSceneSummaryWireSchema),
    has_more: strictTrue,
  })
  .transform((value): AgentScenesPage => ({
    scenes: value.scenes,
    hasMore: value.has_more,
  }));

// Read side: the agent's own org is a tenant as DingTalk recorded it.
const tenantOrgId = orgIdString;

const AgentTenantWireSchema = z
  .object({
    org_id: tenantOrgId,
    name: text,
    // Only the agent's own identity org is protected from deletion; any
    // other or unknown source is a created tenant.
    source: z.unknown().transform((value) => (value === "identity" ? "identity" : "created")),
    group_count: count,
    person_count: count,
  })
  .transform(
    (value): AgentTenant => ({
      orgId: value.org_id,
      name: value.name,
      source: value.source,
      groupCount: value.group_count,
      personCount: value.person_count,
    }),
  );

const AgentUnassignedOrgWireSchema = z
  .object({ org_id: tenantOrgId, group_count: count, person_count: count })
  .transform(
    (value): AgentUnassignedOrg => ({
      orgId: value.org_id,
      groupCount: value.group_count,
      personCount: value.person_count,
    }),
  );

export const EMPTY_AGENT_TENANTS: AgentTenantsList = { tenants: [], unassignedOrgs: [] };

export const AgentTenantsListSchema = z
  .object({
    tenants: tolerantList(AgentTenantWireSchema),
    unassigned_orgs: tolerantList(AgentUnassignedOrgWireSchema),
  })
  .transform((value): AgentTenantsList => {
    // One entry per org; an org with a tenant is never also unassigned.
    const tenants = [...new Map(value.tenants.map((tenant) => [tenant.orgId, tenant])).values()];
    const tenantOrgs = new Set(tenants.map((tenant) => tenant.orgId));
    const unassignedOrgs = [
      ...new Map(
        value.unassigned_orgs
          .filter((org) => !tenantOrgs.has(org.orgId))
          .map((org) => [org.orgId, org]),
      ).values(),
    ];
    return { tenants, unassignedOrgs };
  });

/** Echo of a tenant create or rename: `{tenant: T}` or the bare tenant.
 * null when neither parses; the caller refetches the list. */
export const AgentTenantResponseSchema = z
  .union([z.object({ tenant: AgentTenantWireSchema }).transform((value) => value.tenant), AgentTenantWireSchema])
  .nullable()
  .catch(null);

const AgentTenantPersonWireSchema = z
  .object({
    staff_id: id,
    title: text,
    dm_scene_key: text,
    last_active_at: text,
  })
  .transform(
    (value): AgentTenantPerson => ({
      staffId: value.staff_id,
      title: value.title,
      dmSceneKey: value.dm_scene_key,
      lastActiveAt: value.last_active_at,
    }),
  );

export const AgentTenantPersonsSchema = z
  .object({ persons: tolerantList(AgentTenantPersonWireSchema) })
  .transform((value): AgentTenantPerson[] => [
    ...new Map(value.persons.map((person) => [person.staffId, person])).values(),
  ]);

function nodeScopeTypeOf(value: unknown): ContextNodeScopeType | null {
  return value === "org" || value === "scene" || value === "person" ? value : null;
}

function layerOf(value: unknown): ContextLayer | null {
  return value === "global" || value === "org" || value === "scene" || value === "person" ? value : null;
}

/** `scope` of a node read. A malformed or missing value reads as null, so
 * nothing is ever written to a guessed scope. */
function nodeScopeOf(value: unknown): ContextNodeScope | null {
  const parsed = z
    .object({ type: z.unknown(), org_id: text, key: id, title: text })
    .safeParse(value);
  if (!parsed.success) return null;
  const type = nodeScopeTypeOf(parsed.data.type);
  return type ? { type, orgId: parsed.data.org_id, key: parsed.data.key, title: parsed.data.title } : null;
}

const NodeConnectorWireSchema = z
  .object({
    id,
    name: text,
    catalog_slug: catalogSlug,
    auth_mode: z.string().nullish().catch(null),
    accepts_credential: strictTrue,
    accepts_pat: strictTrue,
    oauth_available: strictTrue,
    install_url: z.unknown().optional(),
    global: strictTrue,
    enabled: strictTrue,
    credential: z
      .object({ connected: strictTrue, account: text.catch("") })
      .nullish()
      .catch(null),
  })
  .transform((connector): ContextNodeConnector => {
    const authMode = connectorAuthModeOf(connector.auth_mode, connector.accepts_credential);
    const connected = connector.credential?.connected === true;
    return {
      id: connector.id,
      name: connector.name,
      catalogSlug: connector.catalog_slug,
      authMode,
      acceptsCredential: connector.accepts_credential,
      // A Personal Access Token is only an alternative to OAuth.
      acceptsPat: authMode === "oauth" && connector.accepts_pat,
      // Only a literal true shows 连接, so the page never offers a sign-in
      // the start endpoint would reject.
      oauthAvailable: authMode === "oauth" && connector.oauth_available,
      installUrl: safeExternalUrl(connector.install_url),
      global: connector.global,
      enabled: connector.enabled,
      credential: { connected, account: connected ? (connector.credential?.account ?? "") : "" },
    };
  });

const NodeSkillWireSchema = z
  .object({ id, name: text, description: text, enabled: strictTrue })
  .transform((value) => ({
    id: value.id,
    name: value.name,
    description: value.description,
    enabled: value.enabled,
  }));

// A layer this build does not know (a newer backend's level) is kept by
// name rather than dropping the entry from the preview.
const layer = z.string().min(1);

// `overridden_by` names the nearer layer whose component replaces this one.
// Any non-empty value marks the entry overridden, even a layer this build
// does not know.
const overriddenBy = z.unknown().transform((value) => ({
  overridden: typeof value === "string" ? value !== "" : value != null && value !== false,
  overriddenBy: layerOf(value),
}));

const EffectivePromptWireSchema = z
  .object({ name: z.string().min(1), text: text, layer, overridden_by: overriddenBy })
  .transform(
    (value): ContextEffectivePrompt => ({
      name: value.name,
      text: value.text,
      layer: value.layer,
      overridden: value.overridden_by.overridden,
      overriddenBy: value.overridden_by.overriddenBy,
    }),
  );

const EffectiveItemWireSchema = z
  .object({ id, name: text, layer })
  .transform((value): ContextEffectiveItem => ({ id: value.id, name: value.name, layer: value.layer }));

const EffectiveMcpServerWireSchema = z
  .object({ name: z.string().min(1), layer, overridden_by: overriddenBy })
  .transform(
    (value): ContextEffectiveMcpServer => ({
      name: value.name,
      layer: value.layer,
      overridden: value.overridden_by.overridden,
      overriddenBy: value.overridden_by.overriddenBy,
    }),
  );

export const ContextNodeDetailSchema = z
  .object({
    scope: z.unknown().optional(),
    // The node's chat (S). A malformed value reads as none.
    scene: AgentSceneSummaryWireSchema.nullish().catch(null),
    prompts: tolerantList(PromptComponentWireSchema),
    connectors: tolerantList(NodeConnectorWireSchema),
    skills: tolerantList(NodeSkillWireSchema),
    mcp_config: z.unknown().optional(),
    mcp_config_redacted: strictTrue,
    can_connect: strictTrue,
    rights: scopeRights,
    effective: z
      .object({
        prompts: tolerantList(EffectivePromptWireSchema),
        connectors: tolerantList(EffectiveItemWireSchema),
        skills: tolerantList(EffectiveItemWireSchema),
        mcp_servers: tolerantList(EffectiveMcpServerWireSchema),
      })
      .nullish()
      .catch(null),
  })
  .transform(
    (value): ContextNodeDetail => ({
      scope: nodeScopeOf(value.scope),
      scene: value.scene ?? null,
      prompts: sortPrompts(value.prompts),
      connectors: value.connectors,
      skills: value.skills,
      mcpConfig: isRecord(value.mcp_config) ? value.mcp_config : null,
      mcpConfigRedacted: value.mcp_config_redacted,
      canConnect: value.can_connect,
      rights: value.rights,
      effective: {
        prompts: value.effective?.prompts ?? [],
        connectors: value.effective?.connectors ?? [],
        skills: value.effective?.skills ?? [],
        mcpServers: value.effective?.mcp_servers ?? [],
      },
    }),
  );

/** Echo of the prompts PUT. null when malformed; the caller keeps what it
 * sent and refetches the node. */
export const ContextPromptComponentsResponseSchema = z
  .object({ prompts: z.array(PromptComponentWireSchema) })
  .transform((value) => sortPrompts(value.prompts))
  .nullable()
  .catch(null);

/** Echo of the mcp-config PUT. The field is required (an object, or null
 * once cleared), so a body without it fails and the caller keeps what it
 * sent. */
export const ContextNodeMcpConfigResponseSchema = z
  .object({ mcp_config: z.union([z.record(z.string(), z.unknown()), z.null()]) })
  .transform((value): Record<string, unknown> | null => value.mcp_config);

/** Echo of the grants DELETE: how many configure-page grants it removed.
 * null when malformed (the revoke still happened; the caller refetches). */
export const ContextNodeGrantsRevokedSchema = z
  .object({ revoked: z.number().int().nonnegative() })
  .transform((value): number => value.revoked)
  .nullable()
  .catch(null);

// ---------------------------------------------------------------------------
// Admin connected apps (GET /api/agents/{id}/connected-apps[/{slug}])
// ---------------------------------------------------------------------------

const appSlug = z.string().regex(/^[a-z0-9][a-z0-9_-]{0,63}$/);

// A malformed or missing connector id means "not in the workspace yet": the
// page then offers 添加 instead of controls that need a connector.
const connectorIdOrNull = z.string().min(1).nullish().catch(null).transform((value) => value ?? null);

// Unknown or missing sources read as "" (not reported): the page then keeps
// its disconnect action, which is what older backends offered.
const sharedAccountSource = z
  .unknown()
  .transform((value): ConnectedAppSharedAccountSource =>
    value === "workspace" || value === "environment" ? value : "",
  );

const ConnectedAppWireSchema = z.object({
  slug: appSlug,
  name: text,
  oauth_available: strictTrue,
  allows_pat: strictTrue,
  install_url: z.unknown().optional(),
  connector_id: connectorIdOrNull,
  added: strictTrue,
  enabled_in_workspace: strictTrue,
  global_enabled: strictTrue,
  offered: strictTrue,
  write_enabled: strictTrue,
  tools: z
    .object({ discovered: count, allowed: count })
    .nullish()
    .catch(null),
  shared_account: z
    .object({ connected: strictTrue, account: text.catch(""), source: sharedAccountSource })
    .nullish()
    .catch(null),
  usage: z
    .object({
      scenes_enabled: count,
      scenes_connected: count,
      persons_enabled: count,
      persons_connected: count,
    })
    .nullish()
    .catch(null),
});

function toConnectedApp(wire: z.output<typeof ConnectedAppWireSchema>): ConnectedApp {
  // A shared account can only be connected on a connector that exists.
  const hasConnector = wire.connector_id !== null;
  return {
    slug: wire.slug,
    name: wire.name || wire.slug,
    oauthAvailable: wire.oauth_available,
    allowsPat: wire.allows_pat,
    installUrl: safeExternalUrl(wire.install_url),
    connectorId: wire.connector_id,
    added: wire.added,
    enabledInWorkspace: hasConnector && wire.enabled_in_workspace,
    globalEnabled: hasConnector && wire.global_enabled,
    offered: hasConnector && wire.offered,
    writeEnabled: wire.write_enabled,
    tools: {
      discovered: wire.tools?.discovered ?? 0,
      allowed: wire.tools?.allowed ?? 0,
    },
    sharedAccount: {
      connected: hasConnector && wire.shared_account?.connected === true,
      account: wire.shared_account?.account ?? "",
      source:
        hasConnector && wire.shared_account?.connected === true ? (wire.shared_account?.source ?? "") : "",
    },
    usage: {
      scenesEnabled: wire.usage?.scenes_enabled ?? 0,
      scenesConnected: wire.usage?.scenes_connected ?? 0,
      personsEnabled: wire.usage?.persons_enabled ?? 0,
      personsConnected: wire.usage?.persons_connected ?? 0,
    },
  };
}

export const ConnectedAppsListSchema = z
  .object({
    apps: tolerantList(ConnectedAppWireSchema),
    can_admin: strictTrue,
  })
  .transform(
    (value): ConnectedAppsList => ({
      apps: value.apps.map(toConnectedApp),
      canAdmin: value.can_admin,
    }),
  );

const ConnectedAppSceneWireSchema = z
  .object({
    ...sceneIdentity,
    title: text,
    kind: sceneKind,
    enabled: strictTrue,
    connected: strictTrue,
    account: text,
  })
  .refine(hasSceneId)
  .transform(
    (value): ConnectedAppSceneUsage => ({
      sceneId: sceneIdOf(value),
      sceneKey: sceneIdOf(value),
      title: value.title,
      kind: value.kind,
      enabled: value.enabled,
      connected: value.connected,
      account: value.account,
    }),
  );

const ConnectedAppPersonWireSchema = z
  .object({
    scope_key: id,
    title: text,
    enabled: strictTrue,
    connected: strictTrue,
    account: text,
    share_in_groups: strictTrue,
  })
  .transform(
    (value): ConnectedAppPersonUsage => ({
      scopeKey: value.scope_key,
      title: value.title,
      enabled: value.enabled,
      connected: value.connected,
      account: value.account,
      shareInGroups: value.share_in_groups,
    }),
  );

const ConnectedAppToolWireSchema = z
  .object({ name: z.string().min(1), read_only: strictTrue, allowed: strictTrue })
  .transform(
    (value): ConnectedAppTool => ({
      name: value.name,
      readOnly: value.read_only,
      allowed: value.allowed,
    }),
  );

export const ConnectedAppDetailSchema = ConnectedAppWireSchema.extend({
  scenes: tolerantList(ConnectedAppSceneWireSchema),
  persons: tolerantList(ConnectedAppPersonWireSchema),
  tool_list: tolerantList(ConnectedAppToolWireSchema),
  can_admin: strictTrue,
}).transform(
  (value): ConnectedAppDetail => ({
    ...toConnectedApp(value),
    scenes: value.scenes,
    persons: value.persons,
    toolList: value.tool_list,
    canAdmin: value.can_admin,
  }),
);

// ---------------------------------------------------------------------------
// Scene routines (GET/POST/PATCH … /routines on the configure page and the
// admin Context Builder)

const nullableText = z
  .string()
  .nullish()
  .transform((value) => value ?? null);

const RoutineTriggerSchema = z
  .object({
    id: text,
    kind: text,
    cron: text,
    timezone: text,
    next_run_at: nullableText,
    next_runs: list(z.string()),
    webhook_url_masked: text,
    webhook_url: text,
  })
  .transform(
    (value): ContextRoutineTrigger => ({
      id: value.id,
      kind: value.kind,
      cron: value.cron,
      timezone: value.timezone,
      nextRunAt: value.next_run_at,
      nextRuns: value.next_runs,
      webhookUrlMasked: value.webhook_url_masked,
      webhookUrl: value.webhook_url,
    }),
  );

const RoutineRunSchema = z
  .object({
    id,
    status: text,
    source: text,
    failure_reason: text,
    created_at: text,
    completed_at: nullableText,
  })
  .transform(
    (value): ContextRoutineRun => ({
      id: value.id,
      status: value.status,
      source: value.source,
      failureReason: value.failure_reason,
      createdAt: value.created_at,
      completedAt: value.completed_at,
    }),
  );

export const ContextRoutineSchema = z
  .object({
    id,
    scene_id: text,
    scene_kind: text,
    autopilot_id: text,
    title: text,
    instructions: text,
    enabled: strictTrue,
    pause_reason: text,
    trigger: RoutineTriggerSchema,
    last_run: RoutineRunSchema.nullish().catch(null),
    created_by_type: text,
    created_at: text,
    updated_at: text,
  })
  .transform(
    (value): ContextRoutine => ({
      id: value.id,
      sceneId: value.scene_id,
      sceneKind: value.scene_kind,
      autopilotId: value.autopilot_id,
      title: value.title,
      instructions: value.instructions,
      enabled: value.enabled,
      pauseReason: value.pause_reason,
      trigger: value.trigger,
      lastRun: value.last_run ?? null,
      createdByType: value.created_by_type,
      createdAt: value.created_at,
      updatedAt: value.updated_at,
    }),
  );

/** A scene's routines; a malformed entry is dropped, not the whole list. */
export const ContextRoutinesListSchema = z
  .object({ routines: tolerantList(ContextRoutineSchema) })
  .transform((value): ContextRoutine[] => value.routines);

/** Create and edit responses: the stored routine and whether a create
 * updated an existing one. null when malformed (the caller refetches). */
export const ContextRoutineWriteSchema = z
  .object({ routine: ContextRoutineSchema, updated: strictTrue })
  .transform((value): ContextRoutineWriteResult => ({ routine: value.routine, updated: value.updated }))
  .nullable()
  .catch(null);

/** Rotate response: the routine with its new full webhook URL. */
export const ContextRoutineEnvelopeSchema = z
  .object({ routine: ContextRoutineSchema })
  .transform((value): ContextRoutine => value.routine)
  .nullable()
  .catch(null);

/** Run-now response: the run it started (null when the run was not created
 * or the body is malformed). */
export const ContextRoutineRunEnvelopeSchema = z
  .object({ run: RoutineRunSchema.nullish() })
  .transform((value): ContextRoutineRun | null => value.run ?? null)
  .nullable()
  .catch(null);
