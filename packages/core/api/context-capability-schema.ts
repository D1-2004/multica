import { z } from "zod";
import type {
  AgentContextCapabilities,
  AgentSceneBinding,
  AgentSceneDetail,
  AgentScenePrompt,
  AgentScenesPage,
  AgentSceneSummary,
  ContextCapabilityBinding,
  ContextConfigAgentDetail,
  ContextConfigAgentSummary,
  ContextConfigGrant,
  ContextConfigRedeemResult,
  ContextConfigSceneDetail,
  ContextConfigSceneGrant,
  ContextConnectorAuthMode,
  ContextConnectorCredential,
  ContextCredentialKind,
  ContextResourceType,
  ContextSceneKind,
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

const SceneGrantSchema = z
  .object({
    scope_key: id,
    scope_title: text,
    source: text,
    expires_at: text,
    kind: sceneKind,
  })
  .transform(
    (value): ContextConfigSceneGrant => ({
      scopeKey: value.scope_key,
      scopeTitle: value.scope_title,
      source: value.source,
      expiresAt: value.expires_at,
      kind: value.kind,
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
  })
  .transform(
    (value): ContextConfigRedeemResult => ({
      agentId: value.agent_id,
      workspaceId: value.workspace_id,
      scopeType: scopeTypeOf(value.scope_type),
      scopeKey: value.scope_key,
      scopeTitle: value.scope_title,
    }),
  );

export const EMPTY_CONTEXT_CONFIG_REDEEM: ContextConfigRedeemResult = {
  agentId: "",
  workspaceId: "",
  scopeType: null,
  scopeKey: "",
  scopeTitle: "",
};

const AgentSummaryWireSchema = z.object({
  id,
  name: text,
  avatar_url: z.string().nullish().transform((value) => value || null),
  workspace_id: text,
});

export const ContextConfigAgentListSchema = z
  .object({
    agents: list(
      AgentSummaryWireSchema.extend({ scopes: list(GrantWireSchema) }),
    ),
  })
  .transform((value): ContextConfigAgentSummary[] =>
    value.agents.map((agent) => ({
      id: agent.id,
      name: agent.name,
      avatarUrl: agent.avatar_url,
      workspaceId: agent.workspace_id,
      scopes: agent.scopes
        .map(toGrant)
        .filter((grant): grant is ContextConfigGrant => grant !== null),
    })),
  );

const SkillItemSchema = z
  .object({ id, name: text, description: text })
  .transform((value) => ({
    id: value.id,
    name: value.name,
    description: value.description,
  }));

export const ContextConfigAgentDetailSchema = z
  .object({
    agent: AgentSummaryWireSchema,
    global: z
      .object({
        connectors: list(z.object({ id, name: text, catalog_slug: catalogSlug })),
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
            oauth_available: z.boolean().nullish().catch(null),
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
      })
      .nullish(),
    scenes: list(SceneGrantSchema),
    jsapi_available: strictTrue,
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
        connectors: (value.global?.connectors ?? []).map((connector) => ({
          id: connector.id,
          name: connector.name,
          catalogSlug: connector.catalog_slug,
        })),
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
            // Only an explicit false hides the connect action: a backend
            // that predates the field still offers it (and answers 503
            // oauth_unavailable, which the page reports).
            oauthAvailable: authMode === "oauth" && connector.oauth_available !== false,
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
          }
        : null,
      scenes: value.scenes,
      jsapiAvailable: value.jsapi_available,
    }),
  );

export const ContextConfigSceneDetailSchema = z
  .object({
    scene: SceneGrantSchema,
    bindings: BindingListSchema,
    credentials: list(CredentialWireSchema),
  })
  .transform(
    (value): ContextConfigSceneDetail => ({
      scene: value.scene,
      bindings: value.bindings,
      credentials: value.credentials,
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
      scenes: value.scenes,
      persons: value.persons,
      configureUrl: value.configure_url,
    }),
  );

// ---------------------------------------------------------------------------
// Admin scenes (GET/PUT /api/agents/{id}/scenes...)
// ---------------------------------------------------------------------------

const AgentSceneSummaryWireSchema = z
  .object({
    scene_key: id,
    kind: sceneKind,
    title: text,
    org_id: text,
    last_active_at: text,
    inbound_session_id: text,
    inbound_count: count,
    memory_id: text,
    has_prompt: strictTrue,
    connector_count: count,
    skill_count: count,
  })
  .transform(
    (value): AgentSceneSummary => ({
      sceneKey: value.scene_key,
      kind: value.kind,
      title: value.title,
      orgId: value.org_id,
      lastActiveAt: value.last_active_at,
      inboundSessionId: value.inbound_session_id,
      inboundCount: value.inbound_count,
      memoryId: value.memory_id,
      hasPrompt: value.has_prompt,
      connectorCount: value.connector_count,
      skillCount: value.skill_count,
    }),
  );

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

const AgentScenePromptWireSchema = z
  .object({
    text: text,
    updated_at: text,
    updated_by_name: text,
  })
  .transform(
    (value): AgentScenePrompt => ({
      text: value.text,
      updatedAt: value.updated_at,
      updatedByName: value.updated_by_name,
    }),
  );

export const EMPTY_AGENT_SCENE_PROMPT: AgentScenePrompt = {
  text: "",
  updatedAt: "",
  updatedByName: "",
};

const AgentSceneBindingWireSchema = z.object({
  resource_type: z.string(),
  resource_id: id,
  enabled: strictTrue,
  updated_by_name: text,
  updated_at: text,
});

function toAgentSceneBinding(
  wire: z.output<typeof AgentSceneBindingWireSchema>,
): AgentSceneBinding | null {
  const resourceType = resourceTypeOf(wire.resource_type);
  if (!resourceType) return null;
  return {
    resourceType,
    resourceId: wire.resource_id,
    enabled: wire.enabled,
    updatedByName: wire.updated_by_name,
    updatedAt: wire.updated_at,
  };
}

export const AgentSceneDetailSchema = z
  .object({
    scene: AgentSceneSummaryWireSchema,
    prompt: AgentScenePromptWireSchema.nullish().catch(null),
    bindings: tolerantList(AgentSceneBindingWireSchema),
    offers: z
      .object({
        connectors: tolerantList(
          z.object({
            id,
            name: text,
            catalog_slug: catalogSlug,
            auth_mode: z.string().nullish().catch(null),
          }),
        ),
        skills: tolerantList(SkillItemSchema),
      })
      .nullish()
      .catch(null),
  })
  .transform(
    (value): AgentSceneDetail => ({
      scene: value.scene,
      prompt: value.prompt ?? EMPTY_AGENT_SCENE_PROMPT,
      bindings: value.bindings
        .map(toAgentSceneBinding)
        .filter((binding): binding is AgentSceneBinding => binding !== null),
      offers: {
        connectors: (value.offers?.connectors ?? []).map((connector) => ({
          id: connector.id,
          name: connector.name,
          catalogSlug: connector.catalog_slug,
          authMode: connectorAuthModeOf(connector.auth_mode, false),
        })),
        skills: value.offers?.skills ?? [],
      },
    }),
  );

export const AgentScenePromptResponseSchema = z
  .object({ prompt: AgentScenePromptWireSchema })
  .transform((value) => value.prompt);

export const AgentSceneBindingResponseSchema = z
  .object({ binding: AgentSceneBindingWireSchema })
  .transform((value) => toAgentSceneBinding(value.binding));
