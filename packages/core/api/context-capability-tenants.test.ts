import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient } from "./client";
import { parseWithFallback } from "./schema";
import type { AgentTenantsList, ContextNodeDetail } from "../types/context-capability";
import {
  AgentTenantPersonsSchema,
  AgentTenantResponseSchema,
  AgentTenantsListSchema,
  ContextNodeDetailSchema,
  EMPTY_AGENT_TENANTS,
  ORG_ID_PATTERN,
  isOrgId,
} from "./context-capability-schema";

afterEach(() => vi.unstubAllGlobals());

const base = "https://pre.example.test";
const agentId = "11111111-1111-4111-8111-111111111111";
const connectorId = "22222222-2222-4222-8222-222222222222";
const skillId = "33333333-3333-4333-8333-333333333333";
const opts = { endpoint: "test", includeReceived: false };

function stubFetch(body: unknown, status = 200) {
  const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify(body), { status }));
  vi.stubGlobal("fetch", fetch);
  return fetch;
}

function requestOf(fetch: ReturnType<typeof vi.fn>, index = 0) {
  const [url, init] = fetch.mock.calls[index] as [string, RequestInit & { headers: Record<string, string> }];
  return { url, init };
}

describe("tenants", () => {
  it("maps tenants and unassigned orgs", () => {
    const list = parseWithFallback<AgentTenantsList>(
      {
        tenants: [
          { org_id: "dingA", name: "Acme", source: "identity", group_count: 3, person_count: 2 },
          { org_id: "dingB", name: "Beta", source: "created", group_count: 0, person_count: 1 },
        ],
        unassigned_orgs: [{ org_id: "dingC", group_count: 4, person_count: 0 }],
      },
      AgentTenantsListSchema,
      EMPTY_AGENT_TENANTS,
      opts,
    );
    expect(list).toEqual({
      tenants: [
        { orgId: "dingA", name: "Acme", source: "identity", groupCount: 3, personCount: 2 },
        { orgId: "dingB", name: "Beta", source: "created", groupCount: 0, personCount: 1 },
      ],
      unassignedOrgs: [{ orgId: "dingC", groupCount: 4, personCount: 0 }],
    });
  });

  it("drops malformed rows, dedupes orgs and reads unknown sources as created", () => {
    const list = AgentTenantsListSchema.parse({
      tenants: [
        { org_id: "dingA", name: null, source: "future", group_count: -1, person_count: "2" },
        { org_id: "dingA", name: "dup" },
        { org_id: "bad org!", name: "x" },
        { name: "no org" },
      ],
      unassigned_orgs: [{ org_id: "dingA" }, { org_id: "dingC" }, "dingD"],
    });
    expect(list.tenants).toEqual([{ orgId: "dingA", name: "dup", source: "created", groupCount: 0, personCount: 0 }]);
    // An org that has a tenant is never also unassigned.
    expect(list.unassignedOrgs).toEqual([{ orgId: "dingC", groupCount: 0, personCount: 0 }]);
  });

  it("reads org ids by the server's scope rule, not the create-tenant pattern", () => {
    // The agent's own org and orgs seen in chats are kept as DingTalk sent them.
    const longId = "d".repeat(100);
    const list = AgentTenantsListSchema.parse({
      tenants: [{ org_id: "ding.corp", name: "Own", source: "identity" }],
      unassigned_orgs: [{ org_id: longId }, { org_id: "has space" }, { org_id: "x".repeat(257) }],
    });
    expect(list.tenants.map((tenant) => tenant.orgId)).toEqual(["ding.corp"]);
    expect(list.unassignedOrgs.map((org) => org.orgId)).toEqual([longId]);
    expect(isOrgId("ding.corp")).toBe(true);
    expect(isOrgId("组织")).toBe(true);
    expect(isOrgId("tab\tid")).toBe(false);
    expect(isOrgId("")).toBe(false);
    expect(isOrgId("é".repeat(129))).toBe(false);
    // A new tenant still needs the strict OrgId.
    expect(ORG_ID_PATTERN.test("ding.corp")).toBe(false);
  });

  it("falls back to an empty list for a malformed body", () => {
    expect(parseWithFallback<AgentTenantsList>("nope", AgentTenantsListSchema, EMPTY_AGENT_TENANTS, opts)).toEqual(
      EMPTY_AGENT_TENANTS,
    );
    expect(AgentTenantsListSchema.parse({ tenants: null })).toEqual(EMPTY_AGENT_TENANTS);
  });

  it("reads a tenant echo wrapped or bare, and null when malformed", () => {
    const tenant = { org_id: "dingA", name: "Acme", source: "created" };
    expect(AgentTenantResponseSchema.parse({ tenant })?.orgId).toBe("dingA");
    expect(AgentTenantResponseSchema.parse(tenant)?.name).toBe("Acme");
    expect(AgentTenantResponseSchema.parse({ ok: true })).toBeNull();
  });

  it("maps a tenant's people and tolerates a missing 1:1 chat", () => {
    const persons = AgentTenantPersonsSchema.parse({
      persons: [
        { staff_id: "staff-1", title: "Ada", dm_scene_key: "cidDm==", last_active_at: "t" },
        { staff_id: "staff-2", title: null },
        { staff_id: "staff-2", title: "dup" },
        { title: "no id" },
      ],
    });
    expect(persons).toEqual([
      { staffId: "staff-1", title: "Ada", dmSceneKey: "cidDm==", lastActiveAt: "t" },
      { staffId: "staff-2", title: "dup", dmSceneKey: "", lastActiveAt: "" },
    ]);
    expect(AgentTenantPersonsSchema.parse({ persons: "x" })).toEqual([]);
  });
});

describe("Context Builder node", () => {
  const body = {
    scope: { type: "scene", org_id: "dingA", key: "cidGroup==", title: "Release crew" },
    scene: {
      scene_key: "cidGroup==",
      kind: "group",
      title: "Release crew",
      org_id: "dingA",
      last_active_at: "t",
      inbound_session_id: "s1",
      inbound_count: 2,
      memory_id: "m1",
    },
    prompts: [
      { id: "p2", name: "Format", order: 2, text: "Use lists.", updated_by_name: "Bob", updated_at: "t2" },
      { id: "p1", name: "Tone", order: 1, text: "Be brief.", updated_by_name: "Ada", updated_at: "t1" },
    ],
    connectors: [
      {
        id: connectorId,
        name: "GitHub",
        catalog_slug: "github",
        auth_mode: "oauth",
        accepts_credential: true,
        accepts_pat: true,
        oauth_available: true,
        install_url: "https://github.com/apps/qwen-tag-pre/installations/new",
        enabled: true,
        credential: { connected: true, account: "@ada" },
        upstream_url: "https://secret.example",
      },
    ],
    skills: [{ id: skillId, name: "Report", description: "Weekly", enabled: false }],
    mcp_config: { mcpServers: { docs: { url: "https://mcp.example/docs" } } },
    mcp_config_redacted: false,
    can_connect: true,
    effective: {
      prompts: [
        { name: "Tone", text: "Be formal.", layer: "org", overridden_by: "scene" },
        { name: "Tone", text: "Be brief.", layer: "scene" },
        { name: "Base", text: "Hello.", layer: "global" },
      ],
      connectors: [{ id: connectorId, name: "GitHub", layer: "scene" }],
      skills: [{ id: skillId, name: "Report", layer: "global" }],
      mcp_servers: [
        { name: "docs", layer: "org", overridden_by: "scene" },
        { name: "docs", layer: "scene", overridden_by: null },
      ],
    },
  };

  it("maps the node, sorts prompts by order and keeps private fields out", () => {
    const node = parseWithFallback<ContextNodeDetail | null>(body, ContextNodeDetailSchema, null, opts);
    expect(node?.scope).toEqual({ type: "scene", orgId: "dingA", key: "cidGroup==", title: "Release crew" });
    // The node's chat: its inbound session and memory.
    expect(node?.scene).toEqual({
      sceneKey: "cidGroup==",
      kind: "group",
      title: "Release crew",
      orgId: "dingA",
      lastActiveAt: "t",
      inboundSessionId: "s1",
      inboundCount: 2,
      memoryId: "m1",
    });
    expect(node?.prompts.map((prompt) => prompt.name)).toEqual(["Tone", "Format"]);
    expect(node?.prompts[0]).toEqual({
      id: "p1",
      name: "Tone",
      order: 1,
      text: "Be brief.",
      updatedByName: "Ada",
      updatedAt: "t1",
    });
    expect(node?.connectors).toEqual([
      {
        id: connectorId,
        name: "GitHub",
        catalogSlug: "github",
        authMode: "oauth",
        acceptsCredential: true,
        acceptsPat: true,
        oauthAvailable: true,
        installUrl: "https://github.com/apps/qwen-tag-pre/installations/new",
        enabled: true,
        credential: { connected: true, account: "@ada" },
      },
    ]);
    expect(node?.skills).toEqual([{ id: skillId, name: "Report", description: "Weekly", enabled: false }]);
    expect(node?.mcpConfig).toEqual({ mcpServers: { docs: { url: "https://mcp.example/docs" } } });
    expect(node?.canConnect).toBe(true);
    expect(JSON.stringify(node)).not.toContain("upstream_url");
  });

  it("maps the effective preview with layers and overrides", () => {
    const node = ContextNodeDetailSchema.parse(body);
    expect(node.effective.prompts).toEqual([
      { name: "Tone", text: "Be formal.", layer: "org", overridden: true, overriddenBy: "scene" },
      { name: "Tone", text: "Be brief.", layer: "scene", overridden: false, overriddenBy: null },
      { name: "Base", text: "Hello.", layer: "global", overridden: false, overriddenBy: null },
    ]);
    expect(node.effective.connectors).toEqual([{ id: connectorId, name: "GitHub", layer: "scene" }]);
    expect(node.effective.skills).toEqual([{ id: skillId, name: "Report", layer: "global" }]);
    expect(node.effective.mcpServers).toEqual([
      { name: "docs", layer: "org", overridden: true, overriddenBy: "scene" },
      { name: "docs", layer: "scene", overridden: false, overriddenBy: null },
    ]);
  });

  it("tolerates malformed fields without losing the node", () => {
    const node = ContextNodeDetailSchema.parse({
      scope: { type: "team", key: "x" },
      scene: { title: "no key" },
      prompts: [{ name: "" }, { name: "Ok", order: "1", text: null }],
      connectors: [{ name: "no id" }, { id: connectorId, enabled: "true", credential: "x", oauth_available: "yes" }],
      skills: null,
      mcp_config: ["not", "an", "object"],
      mcp_config_redacted: "yes",
      can_connect: "true",
      effective: {
        prompts: [
          { name: "x", layer: "department" },
          { name: "y", layer: "org", overridden_by: "future" },
          { name: "z", layer: 3 },
        ],
        connectors: "x",
      },
    });
    // Never guess where to write.
    expect(node.scope).toBeNull();
    expect(node.scene).toBeNull();
    expect(node.prompts).toEqual([{ id: "", name: "Ok", order: 0, text: "", updatedByName: "", updatedAt: "" }]);
    expect(node.connectors).toHaveLength(1);
    expect(node.connectors[0]).toMatchObject({
      enabled: false,
      oauthAvailable: false,
      credential: { connected: false, account: "" },
    });
    expect(node.skills).toEqual([]);
    expect(node.mcpConfig).toBeNull();
    expect(node.mcpConfigRedacted).toBe(false);
    expect(node.canConnect).toBe(false);
    // A layer this build does not know keeps its entry under the raw name
    // (a malformed one is dropped); an unknown overriding layer still marks
    // the entry overridden.
    expect(node.effective.prompts).toEqual([
      { name: "x", text: "", layer: "department", overridden: false, overriddenBy: null },
      { name: "y", text: "", layer: "org", overridden: true, overriddenBy: null },
    ]);
    expect(node.effective.connectors).toEqual([]);
    expect(node.effective.mcpServers).toEqual([]);
  });

  it("reads an empty body as an empty node and a non-object as null", () => {
    expect(ContextNodeDetailSchema.parse({})).toEqual({
      scope: null,
      scene: null,
      prompts: [],
      connectors: [],
      skills: [],
      mcpConfig: null,
      mcpConfigRedacted: false,
      canConnect: false,
      effective: { prompts: [], connectors: [], skills: [], mcpServers: [] },
    });
    expect(parseWithFallback("nope", ContextNodeDetailSchema, null, opts)).toBeNull();
  });
});

describe("tenant and Context Builder client", () => {
  const node = { orgId: "dingA", scopeType: "scene" as const, scopeKey: "cid+/=" };
  const nodePath = `${base}/api/agents/${agentId}/tenants/dingA/context/scene/cid%2B%2F%3D`;

  it("lists tenants with a pinned workspace", async () => {
    const fetch = stubFetch({ tenants: [{ org_id: "dingA", name: "Acme" }] });
    const list = await new ApiClient(base).listAgentTenants("ws-1", agentId);
    const { url, init } = requestOf(fetch);
    expect(url).toBe(`${base}/api/agents/${agentId}/tenants`);
    expect(init.headers["X-Workspace-ID"]).toBe("ws-1");
    expect(init.headers["X-Workspace-Slug"]).toBe("");
    expect(list.tenants[0]?.orgId).toBe("dingA");
  });

  it("creates, renames and deletes a tenant", async () => {
    const created = stubFetch({ tenant: { org_id: "dingB", name: "Beta", source: "created" } });
    const client = new ApiClient(base);
    expect(await client.createAgentTenant("ws-1", agentId, { orgId: "dingB", name: "Beta" })).toMatchObject({
      orgId: "dingB",
      name: "Beta",
    });
    expect(requestOf(created).init.method).toBe("POST");
    expect(JSON.parse(requestOf(created).init.body as string)).toEqual({ org_id: "dingB", name: "Beta" });

    // A malformed echo still created the tenant: fall back to what was sent.
    stubFetch({ ok: true });
    expect(await client.createAgentTenant("ws-1", agentId, { orgId: "dingC", name: "Gamma" })).toEqual({
      orgId: "dingC",
      name: "Gamma",
      source: "created",
      groupCount: 0,
      personCount: 0,
    });

    const renamed = stubFetch({ org_id: "dingB", name: "Beta 2" });
    expect((await client.renameAgentTenant("ws-1", agentId, "dingB", "Beta 2"))?.name).toBe("Beta 2");
    expect(requestOf(renamed).url).toBe(`${base}/api/agents/${agentId}/tenants/dingB`);
    expect(requestOf(renamed).init.method).toBe("PATCH");
    expect(JSON.parse(requestOf(renamed).init.body as string)).toEqual({ name: "Beta 2" });

    const removed = stubFetch({});
    await client.deleteAgentTenant("ws-1", agentId, "dingB");
    expect(requestOf(removed).init.method).toBe("DELETE");
    expect(requestOf(removed).url).toBe(`${base}/api/agents/${agentId}/tenants/dingB`);
  });

  it("pages a tenant's groups and lists its people", async () => {
    const groups = stubFetch({ scenes: [{ scene_key: "cid1" }], has_more: true });
    const client = new ApiClient(base);
    const page = await client.listAgentTenantGroups("ws-1", agentId, "dingA", { limit: 50, offset: 100 });
    expect(requestOf(groups).url).toBe(`${base}/api/agents/${agentId}/tenants/dingA/groups?limit=50&offset=100`);
    expect(page).toMatchObject({ hasMore: true, scenes: [{ sceneKey: "cid1" }] });

    const persons = stubFetch({ persons: [{ staff_id: "staff-1", title: "Ada" }] });
    expect(await client.listAgentTenantPersons("ws-1", agentId, "dingA")).toHaveLength(1);
    expect(requestOf(persons).url).toBe(`${base}/api/agents/${agentId}/tenants/dingA/persons`);
  });

  it("encodes the node address and returns null for a malformed node", async () => {
    const fetch = stubFetch("nope");
    expect(await new ApiClient(base).getContextNode("ws-1", agentId, node)).toBeNull();
    expect(requestOf(fetch).url).toBe(nodePath);
  });

  it("writes switches, prompts, MCP servers, tokens and connects to the node's routes", async () => {
    const client = new ApiClient(base);

    const binding = stubFetch({});
    await client.setContextNodeBinding("ws-1", agentId, node, {
      resourceType: "connector",
      resourceId: connectorId,
      enabled: true,
    });
    expect(requestOf(binding).url).toBe(`${nodePath}/bindings`);
    expect(requestOf(binding).init.method).toBe("PUT");
    expect(JSON.parse(requestOf(binding).init.body as string)).toEqual({
      resource_type: "connector",
      resource_id: connectorId,
      enabled: true,
    });

    const prompts = stubFetch({ prompts: [{ id: "p1", name: "Tone", order: 1, text: "Be brief." }] });
    const saved = await client.setContextNodePrompts("ws-1", agentId, node, [{ name: "Tone", order: 1, text: "Be brief." }]);
    expect(requestOf(prompts).url).toBe(`${nodePath}/prompts`);
    expect(JSON.parse(requestOf(prompts).init.body as string)).toEqual({
      prompts: [{ name: "Tone", order: 1, text: "Be brief." }],
    });
    expect(saved?.[0]).toMatchObject({ id: "p1", name: "Tone" });
    stubFetch({ ok: true });
    expect(await client.setContextNodePrompts("ws-1", agentId, node, [])).toBeNull();

    const config = { mcpServers: { docs: { url: "https://mcp.example/docs" } } };
    const mcp = stubFetch({ mcp_config: config });
    expect(await client.setContextNodeMcpConfig("ws-1", agentId, node, config)).toEqual(config);
    expect(requestOf(mcp).url).toBe(`${nodePath}/mcp-config`);
    expect(JSON.parse(requestOf(mcp).init.body as string)).toEqual({ mcp_config: config });
    stubFetch({ ok: true });
    // A malformed echo keeps what was sent.
    expect(await client.setContextNodeMcpConfig("ws-1", agentId, node, config)).toEqual(config);

    const token = stubFetch({});
    await client.setContextNodeCredential("ws-1", agentId, node, { connectorId, bearer: "secret" });
    expect(requestOf(token).url).toBe(`${nodePath}/credentials`);
    expect(JSON.parse(requestOf(token).init.body as string)).toEqual({ connector_id: connectorId, bearer: "secret" });

    const remove = stubFetch({});
    await client.deleteContextNodeCredential("ws-1", agentId, node, connectorId);
    expect(requestOf(remove).url).toBe(`${nodePath}/credentials?connector_id=${connectorId}`);
    expect(requestOf(remove).init.method).toBe("DELETE");

    const start = stubFetch({ authorize_url: "https://github.com/login/oauth/authorize?state=x" });
    expect(
      await client.startContextNodeConnection("ws-1", agentId, node, { connectorId, returnTo: "/acme/agents/a" }),
    ).toBe("https://github.com/login/oauth/authorize?state=x");
    expect(requestOf(start).url).toBe(`${nodePath}/connections/start`);
    expect(JSON.parse(requestOf(start).init.body as string)).toEqual({
      connector_id: connectorId,
      return_to: "/acme/agents/a",
    });
    stubFetch({ authorize_url: "javascript:alert(1)" });
    expect(await client.startContextNodeConnection("ws-1", agentId, node, { connectorId })).toBe("");
  });
});
