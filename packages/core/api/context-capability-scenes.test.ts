import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient } from "./client";
import { parseWithFallback } from "./schema";
import type { AgentScenesPage } from "../types/context-capability";
import {
  AgentScenesPageSchema,
  ContextCapabilityBindingResponseSchema,
  ContextConfigAgentDetailSchema,
  ContextConfigRedeemSchema,
  ContextConfigSceneDetailSchema,
  EMPTY_AGENT_SCENES_PAGE,
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

function requestOf(fetch: ReturnType<typeof vi.fn>) {
  const [url, init] = fetch.mock.calls[0] as [string, RequestInit & { headers: Record<string, string> }];
  return { url, init };
}

// An Agent work scene: its scene_id is its identity (scene_key repeats it);
// the DingTalk conversation id is display-only.
const groupSceneId = "66666666-6666-4666-8666-666666666666";
const dmSceneId = "77777777-7777-4777-8777-777777777777";

const groupScene = {
  scene_id: groupSceneId,
  scene_key: groupSceneId,
  conversation_id: "cidGroup==",
  kind: "group",
  title: "Release crew",
  org_id: "ding-org",
  last_active_at: "2026-09-30T08:00:00Z",
  inbound_session_id: "44444444-4444-4444-8444-444444444444",
  inbound_count: 3,
  memory_id: groupSceneId,
  has_memory: true,
  has_prompt: true,
};

describe("tenant group list", () => {
  it("maps group and 1:1 scenes to camelCase, keyed by scene_id", () => {
    const page = parseWithFallback<AgentScenesPage>(
      {
        scenes: [
          groupScene,
          {
            scene_id: dmSceneId,
            scene_key: dmSceneId,
            conversation_id: "cidDm==",
            kind: "dm",
            title: null,
            memory_id: "",
            has_memory: false,
            has_prompt: false,
          },
        ],
        has_more: true,
      },
      AgentScenesPageSchema,
      EMPTY_AGENT_SCENES_PAGE,
      opts,
    );
    expect(page.hasMore).toBe(true);
    expect(page.scenes[0]).toEqual({
      sceneId: groupSceneId,
      sceneKey: groupSceneId,
      conversationId: "cidGroup==",
      kind: "group",
      title: "Release crew",
      orgId: "ding-org",
      lastActiveAt: "2026-09-30T08:00:00Z",
      inboundSessionId: "44444444-4444-4444-8444-444444444444",
      inboundCount: 3,
      memoryId: groupSceneId,
      hasMemory: true,
      hasPrompt: true,
    });
    expect(page.scenes[1]).toMatchObject({
      sceneId: dmSceneId,
      conversationId: "cidDm==",
      kind: "dm",
      title: "",
      inboundSessionId: "",
      inboundCount: 0,
      memoryId: "",
      hasMemory: false,
      hasPrompt: false,
    });
  });

  it("falls back safely when scene_id, conversation_id or the memory flags are missing", () => {
    const page = AgentScenesPageSchema.parse({
      scenes: [
        // No scene_id: scene_key carries the same scene_id.
        { scene_key: groupSceneId, kind: "group", memory_id: groupSceneId },
        // No scene_key: scene_id alone; malformed conversation and flags.
        { scene_id: dmSceneId, kind: "dm", conversation_id: 42, has_memory: "true", has_prompt: 1 },
        // has_memory without a memory_id: the memory is opened by scene_id.
        { scene_id: "88888888-8888-4888-8888-888888888888", has_memory: true },
        // Neither id: dropped, never keyed by a guess.
        { conversation_id: "cidOrphan==", kind: "group", title: "Orphan" },
        { scene_id: "", scene_key: null, title: "Empty ids" },
      ],
    });
    expect(page.scenes).toHaveLength(3);
    expect(page.scenes[0]).toMatchObject({
      sceneId: groupSceneId,
      sceneKey: groupSceneId,
      conversationId: "",
      memoryId: groupSceneId,
      hasMemory: true,
      hasPrompt: false,
    });
    expect(page.scenes[1]).toMatchObject({
      sceneId: dmSceneId,
      sceneKey: dmSceneId,
      conversationId: "",
      kind: "dm",
      memoryId: "",
      hasMemory: false,
      hasPrompt: false,
    });
    expect(page.scenes[2]).toMatchObject({
      sceneId: "88888888-8888-4888-8888-888888888888",
      memoryId: "88888888-8888-4888-8888-888888888888",
      hasMemory: true,
    });
  });

  it("drops malformed rows and unknown flags instead of emptying the list", () => {
    const page = AgentScenesPageSchema.parse({
      scenes: [
        { kind: "group" },
        { ...groupScene, kind: "channel", inbound_count: -2 },
      ],
      has_more: "yes",
    });
    expect(page.hasMore).toBe(false);
    expect(page.scenes).toHaveLength(1);
    expect(page.scenes[0]).toMatchObject({ kind: "group", inboundCount: 0 });
  });

  it("falls back to an empty page for a malformed body", () => {
    expect(
      parseWithFallback<AgentScenesPage>("nope", AgentScenesPageSchema, EMPTY_AGENT_SCENES_PAGE, opts),
    ).toEqual(EMPTY_AGENT_SCENES_PAGE);
    expect(AgentScenesPageSchema.parse({ scenes: null })).toEqual(EMPTY_AGENT_SCENES_PAGE);
  });
});

describe("share_in_groups and scene kinds on the mobile API", () => {
  it("reports share_in_groups only for a literal true on a connector binding", () => {
    expect(
      ContextCapabilityBindingResponseSchema.parse({
        binding: { resource_type: "connector", resource_id: connectorId, enabled: true, share_in_groups: true },
      }),
    ).toEqual({ resourceType: "connector", resourceId: connectorId, enabled: true, shareInGroups: true });
    expect(
      ContextCapabilityBindingResponseSchema.parse({
        binding: { resource_type: "connector", resource_id: connectorId, enabled: true, share_in_groups: "true" },
      })?.shareInGroups,
    ).toBe(false);
    expect(
      ContextCapabilityBindingResponseSchema.parse({
        binding: { resource_type: "skill", resource_id: skillId, enabled: true, share_in_groups: true },
      })?.shareInGroups,
    ).toBe(false);
  });

  it("labels agent detail scenes as group or 1:1 chats", () => {
    const detail = ContextConfigAgentDetailSchema.parse({
      agent: { id: agentId, name: "Helper" },
      scenes: [
        { scope_key: "cidGroup", scope_title: "Team", kind: "group" },
        { scope_key: "cidDm", scope_title: "Ada", kind: "dm" },
        { scope_key: "cidOld", scope_title: "Legacy" },
      ],
    });
    expect(detail.scenes.map((scene) => scene.kind)).toEqual(["group", "dm", "group"]);
  });
});

describe("configure-page scene scope", () => {
  const scene = { scope_key: dmSceneId, scope_title: "Chat", source: "manager", expires_at: "", kind: "dm" };

  it("reads a 1:1 chat as its own scene scope, keyed by its scene_id", () => {
    const detail = ContextConfigSceneDetailSchema.parse({
      scene,
      bindings: [],
      credentials: [],
      scope: { type: "scene", key: dmSceneId, title: "Chat" },
      rights: { toggle: true, connect: true, edit_prompts: true, edit_mcp: true },
    });
    expect(detail.scene).toMatchObject({ scopeKey: dmSceneId, kind: "dm" });
    expect(detail.scope).toEqual({ type: "scene", key: dmSceneId, title: "Chat" });
    expect(detail.rights).toEqual({ toggle: true, connect: true, editPrompts: true, editMcp: true, editRoutines: false });
  });

  it("reads a missing scope as the scene and a null or malformed one as unknown", () => {
    expect(ContextConfigSceneDetailSchema.parse({ scene }).scope).toEqual({
      type: "scene",
      key: dmSceneId,
      title: "Chat",
    });
    expect(ContextConfigSceneDetailSchema.parse({ scene, scope: null }).scope).toBeNull();
    expect(ContextConfigSceneDetailSchema.parse({ scene, scope: { type: "person" } }).scope).toBeNull();
  });

  it("reads whether the caller may connect, with only a literal true allowing it", () => {
    expect(ContextConfigSceneDetailSchema.parse({ scene, can_connect: true }).canConnect).toBe(true);
    expect(ContextConfigSceneDetailSchema.parse({ scene, can_connect: false }).canConnect).toBe(false);
    expect(ContextConfigSceneDetailSchema.parse({ scene, can_connect: "true" }).canConnect).toBe(false);
    // An older backend does not say.
    expect(ContextConfigSceneDetailSchema.parse({ scene }).canConnect).toBeNull();
  });
});

describe("configure-page scope writes", () => {
  it("sends share_in_groups only when the caller sets it", async () => {
    const fetch = stubFetch({
      binding: { resource_type: "connector", resource_id: connectorId, enabled: true, share_in_groups: true },
    });
    const binding = await new ApiClient(base).setContextCapabilityBinding(agentId, {
      scopeType: "person",
      scopeKey: "staff-1",
      resourceType: "connector",
      resourceId: connectorId,
      enabled: true,
      shareInGroups: true,
    });
    expect(JSON.parse(requestOf(fetch).init.body as string)).toEqual({
      scope_type: "person",
      scope_key: "staff-1",
      resource_type: "connector",
      resource_id: connectorId,
      enabled: true,
      share_in_groups: true,
    });
    expect(binding.shareInGroups).toBe(true);
  });
});

describe("configure-page tenants", () => {
  it("reads the tenant of each scene and drops malformed org ids", () => {
    const detail = ContextConfigAgentDetailSchema.parse({
      agent: { id: agentId, name: "Helper" },
      scenes: [
        { scope_key: "cidA", scope_title: "Team", kind: "group", org_id: "dingA" },
        { scope_key: "cidB", scope_title: "Other", kind: "group", org_id: "bad org!" },
        { scope_key: "cidC", scope_title: "Old" },
      ],
    });
    expect(detail.scenes.map((scene) => scene.orgId)).toEqual(["dingA", "", ""]);
    // An older backend names no tenant and no enterprise level.
    expect(detail.tenant).toBeNull();
    expect(detail.tenants).toEqual([]);
    expect(detail.org).toBeNull();
  });

  it("maps the page's tenant, the tenants the caller may open and the enterprise level", () => {
    const detail = ContextConfigAgentDetailSchema.parse({
      agent: { id: agentId, name: "Helper" },
      tenant: { org_id: "dingA", name: "Acme", source: "identity" },
      tenants: [
        { org_id: "dingA", name: "Acme", source: "identity" },
        { org_id: "dingB", name: "Beta", source: "created" },
        { org_id: "dingB", name: "dup", source: "created" },
        { org_id: "bad org!", name: "x", source: "created" },
      ],
      org: {
        scope_key: "dingA",
        scope_title: "Acme",
        bindings: [
          { resource_type: "skill", resource_id: skillId, enabled: true },
          { resource_type: "widget", resource_id: connectorId, enabled: true },
        ],
        credentials: [{ connector_id: connectorId, hint: "", updated_at: "2026-09-30T08:00:00Z", kind: "oauth" }],
        can_edit: false,
      },
    });
    expect(detail.tenant).toEqual({ orgId: "dingA", name: "Acme", source: "identity" });
    expect(detail.tenants.map((tenant) => [tenant.orgId, tenant.name])).toEqual([
      ["dingA", "Acme"],
      ["dingB", "dup"],
    ]);
    expect(detail.org).toEqual({
      scopeKey: "dingA",
      scopeTitle: "Acme",
      bindings: [{ resourceType: "skill", resourceId: skillId, enabled: true, shareInGroups: false }],
      credentials: [{ connectorId, hint: "", updatedAt: "2026-09-30T08:00:00Z", kind: "oauth" }],
      canEdit: false,
      // An older backend sends no rights, prompts or MCP servers.
      rights: null,
      prompts: [],
      mcpConfig: null,
      mcpConfigRedacted: false,
    });
  });

  it("allows editing the enterprise level only for a literal true, and reads a malformed one as none", () => {
    const org = { scope_key: "dingA", scope_title: "Acme", bindings: [], credentials: [] };
    const parse = (value: unknown) =>
      ContextConfigAgentDetailSchema.parse({ agent: { id: agentId }, org: value }).org;
    expect(parse({ ...org, can_edit: true })?.canEdit).toBe(true);
    expect(parse({ ...org, can_edit: "true" })?.canEdit).toBe(false);
    expect(parse({ ...org, scope_key: "bad org!" })).toBeNull();
    expect(parse({ scope_title: "no key" })).toBeNull();
    expect(parse("dingA")).toBeNull();
    expect(
      ContextConfigAgentDetailSchema.parse({ agent: { id: agentId }, tenant: { org_id: "" } }).tenant,
    ).toBeNull();
  });

  it("lists the catalog apps, dropping malformed and duplicate rows", () => {
    const detail = ContextConfigAgentDetailSchema.parse({
      agent: { id: agentId, name: "Helper" },
      apps: [
        { slug: "github", name: "GitHub" },
        { slug: "notion" },
        { slug: "Bad Slug!", name: "x" },
        { name: "no slug" },
        "slack",
        { slug: "github", name: "GitHub again" },
      ],
    });
    expect(detail.apps).toEqual([
      { slug: "github", name: "GitHub again" },
      { slug: "notion", name: "notion" },
    ]);
    // An older backend sends no catalog; a malformed one reads as empty.
    expect(ContextConfigAgentDetailSchema.parse({ agent: { id: agentId } }).apps).toEqual([]);
    expect(ContextConfigAgentDetailSchema.parse({ agent: { id: agentId }, apps: "github" }).apps).toEqual([]);
  });

  it("reads the tenant of a redeemed link", () => {
    const base = { agent_id: agentId, workspace_id: "ws", scope_type: "person", scope_key: "staff-1" };
    expect(ContextConfigRedeemSchema.parse({ ...base, org_id: "dingB" }).orgId).toBe("dingB");
    expect(ContextConfigRedeemSchema.parse({ ...base, org_id: "bad org!" }).orgId).toBe("");
    expect(ContextConfigRedeemSchema.parse(base).orgId).toBe("");
  });

  it("sends org_id on scope requests only for another tenant", async () => {
    const fetch = stubFetch({ binding: { resource_type: "skill", resource_id: skillId, enabled: true } });
    const client = new ApiClient(base);
    await client.setContextCapabilityBinding(agentId, {
      scopeType: "scene",
      scopeKey: "cidB",
      orgId: "dingB",
      resourceType: "skill",
      resourceId: skillId,
      enabled: true,
    });
    expect(JSON.parse(requestOf(fetch).init.body as string)).toMatchObject({ org_id: "dingB" });

    const own = stubFetch({ binding: { resource_type: "skill", resource_id: skillId, enabled: true } });
    await client.setContextCapabilityBinding(agentId, {
      scopeType: "scene",
      scopeKey: "cidA",
      orgId: "",
      resourceType: "skill",
      resourceId: skillId,
      enabled: true,
    });
    expect(JSON.parse(requestOf(own).init.body as string)).not.toHaveProperty("org_id");

    const scene = stubFetch({ scene: { scope_key: "cidB" } });
    await client.getContextConfigScene(agentId, "cid+B", "dingB");
    expect(requestOf(scene).url).toBe(
      `${base}/api/context-capabilities/agents/${agentId}/scenes/cid%2BB?org_id=dingB`,
    );

    const remove = stubFetch({});
    await client.deleteContextConnectorCredential(agentId, {
      scopeType: "person",
      scopeKey: "staff-1",
      orgId: "dingB",
      connectorId,
    });
    expect(requestOf(remove).url).toContain("org_id=dingB");
  });
});
