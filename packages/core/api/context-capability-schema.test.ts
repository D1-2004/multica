import { describe, expect, it } from "vitest";
import { parseWithFallback } from "./schema";
import type { ContextConfigAgentDetail } from "../types/context-capability";
import {
  AgentContextCapabilitiesSchema,
  ContextCapabilityBindingResponseSchema,
  ContextConfigAgentDetailSchema,
  ContextConfigAgentListSchema,
  ContextConfigRedeemSchema,
  ContextConfigSceneDetailSchema,
  DingTalkJsapiConfigSchema,
  EMPTY_CONTEXT_CONFIG_REDEEM,
} from "./context-capability-schema";

const agentId = "11111111-1111-4111-8111-111111111111";
const connectorId = "22222222-2222-4222-8222-222222222222";
const skillId = "33333333-3333-4333-8333-333333333333";
const opts = { endpoint: "test", includeReceived: false };

const detail = {
  agent: { id: agentId, name: "Helper", avatar_url: "", workspace_id: "ws-1" },
  global: {
    connectors: [{ id: connectorId, name: "Wiki" }],
    skills: [{ id: skillId, name: "Report", description: null }],
  },
  offers: {
    connectors: [
      {
        id: connectorId,
        name: "Wiki",
        tools: ["search"],
        accepts_credential: true,
        credential_required: true,
        upstream_url: "https://private.example/mcp",
        credential_ref: "SECRET_REF",
      },
    ],
    skills: [{ id: skillId, name: "Report", description: "Weekly" }],
  },
  person: {
    scope_key: "staff-1",
    scope_title: "Alice",
    source: "agent_link",
    expires_at: "2027-09-29T00:00:00Z",
    bindings: [
      { resource_type: "connector", resource_id: connectorId, enabled: true },
      { resource_type: "future_kind", resource_id: skillId, enabled: true },
    ],
    credentials: [
      { connector_id: connectorId, hint: "••••abcd", updated_at: "2026-09-29T00:00:00Z", bearer: "raw-secret" },
    ],
  },
  scenes: null,
  jsapi_available: true,
};

describe("context capability mobile responses", () => {
  it("maps agent detail to camelCase and drops private connector fields", () => {
    const result = parseWithFallback(raw(detail), ContextConfigAgentDetailSchema, null, opts);
    expect(result).toMatchObject({
      agent: { id: agentId, name: "Helper", avatarUrl: null, workspaceId: "ws-1" },
      global: { skills: [{ id: skillId, name: "Report", description: "" }] },
      offers: {
        connectors: [
          { id: connectorId, tools: ["search"], acceptsCredential: true, credentialRequired: true },
        ],
      },
      scenes: [],
      jsapiAvailable: true,
    });
    const serialized = JSON.stringify(result);
    expect(serialized).not.toContain("private.example");
    expect(serialized).not.toContain("SECRET_REF");
    expect(serialized).not.toContain("raw-secret");
  });

  it("drops bindings of unknown resource types instead of failing the page", () => {
    const result = parseWithFallback<ContextConfigAgentDetail | null>(
      raw(detail),
      ContextConfigAgentDetailSchema,
      null,
      opts,
    );
    expect(result?.person?.bindings).toEqual([
      { resourceType: "connector", resourceId: connectorId, enabled: true, shareInGroups: false },
    ]);
  });

  it("never reports a binding as enabled unless the server sent literal true", () => {
    const result = ContextConfigSceneDetailSchema.parse({
      scene: { scope_key: "cid123", scope_title: "Team", source: "jsapi", expires_at: "" },
      bindings: [{ resource_type: "skill", resource_id: skillId, enabled: "true" }],
      credentials: null,
    });
    expect(result.bindings).toEqual([
      { resourceType: "skill", resourceId: skillId, enabled: false, shareInGroups: false },
    ]);
    expect(result.credentials).toEqual([]);
  });

  it("does not claim a credential is required for a connector that cannot hold one", () => {
    const body = raw(detail);
    body.offers.connectors[0]!.accepts_credential = false;
    const result = ContextConfigAgentDetailSchema.parse(body);
    expect(result.offers.connectors[0]).toMatchObject({
      acceptsCredential: false,
      credentialRequired: false,
    });
  });

  it("falls back to null when the agent identity is missing", () => {
    const body: Record<string, unknown> = raw(detail);
    delete body.agent;
    expect(parseWithFallback(body, ContextConfigAgentDetailSchema, null, opts)).toBeNull();
  });

  it("filters unknown scope types from the agent list", () => {
    const result = ContextConfigAgentListSchema.parse({
      agents: [
        {
          id: agentId,
          name: "Helper",
          avatar_url: null,
          workspace_id: "ws-1",
          scopes: [
            { scope_type: "scene", scope_key: "cid1", scope_title: "Team", source: "agent_link", expires_at: "" },
            { scope_type: "offer", scope_key: "", scope_title: "", source: "", expires_at: "" },
          ],
        },
      ],
    });
    expect(result[0]?.scopes).toEqual([
      { scopeType: "scene", scopeKey: "cid1", scopeTitle: "Team", source: "agent_link", expiresAt: "" },
    ]);
  });

  it("returns an empty list when the agent list is malformed", () => {
    expect(parseWithFallback({ agents: "nope" }, ContextConfigAgentListSchema, [], opts)).toEqual([]);
    expect(ContextConfigAgentListSchema.parse({ agents: null })).toEqual([]);
  });

  it("keeps redeem results usable when the scope type is unknown", () => {
    expect(
      ContextConfigRedeemSchema.parse({
        agent_id: agentId,
        scope_type: "mystery",
        scope_key: "cid1",
        scope_title: "Team",
      }),
    ).toEqual({ agentId, workspaceId: "", scopeType: null, scopeKey: "cid1", scopeTitle: "Team" });
    expect(
      parseWithFallback({ scope_type: "scene" }, ContextConfigRedeemSchema, EMPTY_CONTEXT_CONFIG_REDEEM, opts),
    ).toEqual(EMPTY_CONTEXT_CONFIG_REDEEM);
  });

  it("returns null for a binding echo of an unknown resource type", () => {
    expect(
      ContextCapabilityBindingResponseSchema.parse({
        binding: { resource_type: "other", resource_id: skillId, enabled: true },
      }),
    ).toBeNull();
  });

  it("accepts numeric DingTalk agent ids and timestamps but rejects partial signatures", () => {
    expect(
      DingTalkJsapiConfigSchema.parse({
        corp_id: "ding123",
        agent_id: 4567,
        time_stamp: 1700000000,
        nonce_str: "nonce",
        signature: "sig",
      }),
    ).toEqual({ corpId: "ding123", agentId: "4567", timeStamp: "1700000000", nonceStr: "nonce", signature: "sig" });
    expect(
      parseWithFallback({ corp_id: "ding123", agent_id: "1", nonce_str: "n" }, DingTalkJsapiConfigSchema, null, opts),
    ).toBeNull();
  });
});

describe("context capability OAuth connector fields", () => {
  it("defaults the fields of an older backend to a custom Bearer connector", () => {
    const result = ContextConfigAgentDetailSchema.parse(raw(detail));
    expect(result.offers.connectors[0]).toMatchObject({
      catalogSlug: "",
      authMode: "bearer",
      acceptsPat: false,
      oauthAvailable: false,
      installUrl: "",
    });
    expect(result.global.connectors[0]).toEqual({ id: connectorId, name: "Wiki", catalogSlug: "" });
    expect(result.person?.credentials[0]).toMatchObject({ kind: "bearer" });
  });

  it("maps an OAuth catalog connector with a Personal Access Token alternative", () => {
    const body = raw(detail);
    Object.assign(body.offers.connectors[0]!, {
      catalog_slug: "github",
      auth_mode: "oauth",
      accepts_credential: false,
      credential_required: true,
      accepts_pat: true,
      install_url: "https://github.com/apps/multica/installations/new",
    });
    Object.assign(body.person.credentials[0]!, { kind: "oauth", hint: "@octocat" });
    const result = ContextConfigAgentDetailSchema.parse(body);
    expect(result.offers.connectors[0]).toMatchObject({
      catalogSlug: "github",
      authMode: "oauth",
      acceptsPat: true,
      // A backend that predates oauth_available keeps the connect action.
      oauthAvailable: true,
      // OAuth connectors hold scoped credentials through the provider sign-in.
      credentialRequired: true,
      installUrl: "https://github.com/apps/multica/installations/new",
    });
    expect(result.person?.credentials[0]).toMatchObject({ kind: "oauth", hint: "@octocat" });
  });

  it("keeps connectors of unknown auth modes and credential kinds listed without OAuth affordances", () => {
    const body = raw(detail);
    Object.assign(body.offers.connectors[0]!, {
      auth_mode: "mtls",
      accepts_pat: true,
      catalog_slug: "Not A Slug",
      install_url: "javascript:alert(1)",
    });
    Object.assign(body.person.credentials[0]!, { kind: "saml" });
    const result = ContextConfigAgentDetailSchema.parse(body);
    expect(result.offers.connectors[0]).toMatchObject({
      authMode: "unknown",
      acceptsPat: false,
      catalogSlug: "",
      installUrl: "",
    });
    expect(result.person?.credentials[0]?.kind).toBe("unknown");
  });

  it("hides the OAuth connect action only when the server says it is unavailable", () => {
    const connectorWith = (fields: Record<string, unknown>) => {
      const body = raw(detail);
      Object.assign(body.offers.connectors[0]!, { catalog_slug: "github", auth_mode: "oauth", accepts_pat: true, ...fields });
      return ContextConfigAgentDetailSchema.parse(body).offers.connectors[0];
    };
    expect(connectorWith({ oauth_available: false })).toMatchObject({ oauthAvailable: false, acceptsPat: true });
    expect(connectorWith({ oauth_available: true })?.oauthAvailable).toBe(true);
    // A malformed value is treated like a missing one, never as a reason to
    // drop the connector.
    expect(connectorWith({ oauth_available: "no" })?.oauthAvailable).toBe(true);
    expect(connectorWith({ auth_mode: "bearer", oauth_available: true })?.oauthAvailable).toBe(false);
  });

  it("never reports Personal Access Token support from a non-literal true", () => {
    const body = raw(detail);
    Object.assign(body.offers.connectors[0]!, { auth_mode: "oauth", accepts_pat: "true" });
    expect(ContextConfigAgentDetailSchema.parse(body).offers.connectors[0]?.acceptsPat).toBe(false);
  });

  it("maps scene credential kinds", () => {
    const result = ContextConfigSceneDetailSchema.parse({
      scene: { scope_key: "cid1", scope_title: "Team", source: "agent_link", expires_at: "" },
      bindings: [],
      credentials: [{ connector_id: connectorId, hint: "OAuth", updated_at: "", kind: "oauth" }],
    });
    expect(result.credentials).toEqual([{ connectorId, hint: "OAuth", updatedAt: "", kind: "oauth" }]);
  });
});

describe("context capability admin response", () => {
  it("maps the tab body and tolerates null lists", () => {
    const result = AgentContextCapabilitiesSchema.parse({
      enabled: true,
      library: {
        connectors: [{ id: connectorId, name: "Wiki", enabled: true, auth_mode: "oauth", catalog_slug: "github", upstream_url: "https://x" }],
        skills: null,
      },
      offers: { connector_ids: [connectorId], skill_ids: null },
      scenes: [
        {
          scope_key: "cid1",
          scope_title: "Team",
          bindings: [{ resource_type: "connector", resource_id: connectorId, enabled: true }],
          credential_count: 1,
        },
      ],
      persons: null,
      configure_url: "https://app.example/dingtalk/configure?agent=a",
    });
    expect(result).toEqual({
      enabled: true,
      library: {
        connectors: [{ id: connectorId, name: "Wiki", enabled: true, authMode: "oauth", catalogSlug: "github" }],
        skills: [],
      },
      offers: { connectorIds: [connectorId], skillIds: [] },
      scenes: [
        {
          scopeKey: "cid1",
          scopeTitle: "Team",
          bindings: [{ resourceType: "connector", resourceId: connectorId, enabled: true, shareInGroups: false }],
          credentialCount: 1,
        },
      ],
      persons: [],
      configureUrl: "https://app.example/dingtalk/configure?agent=a",
    });
  });

  it("treats a disabled flag response as disabled and malformed input as unavailable", () => {
    expect(AgentContextCapabilitiesSchema.parse({ enabled: false }).enabled).toBe(false);
    expect(AgentContextCapabilitiesSchema.parse({ enabled: "true" }).enabled).toBe(false);
    expect(
      parseWithFallback({ enabled: true, scenes: [{ scope_key: 3 }] }, AgentContextCapabilitiesSchema, null, opts),
    ).toBeNull();
  });
});

function raw<T>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T;
}
