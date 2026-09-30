import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient } from "./client";
import { parseWithFallback } from "./schema";
import type { AgentSceneDetail, AgentScenesPage } from "../types/context-capability";
import {
  AgentSceneDetailSchema,
  AgentScenesPageSchema,
  ContextCapabilityBindingResponseSchema,
  ContextConfigAgentDetailSchema,
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

const groupScene = {
  scene_key: "cidGroup==",
  kind: "group",
  title: "Release crew",
  org_id: "ding-org",
  last_active_at: "2026-09-30T08:00:00Z",
  inbound_session_id: "44444444-4444-4444-8444-444444444444",
  inbound_count: 3,
  memory_id: "55555555-5555-4555-8555-555555555555",
  has_prompt: true,
};

describe("admin scene list", () => {
  it("maps group and 1:1 scenes to camelCase", () => {
    const page = parseWithFallback<AgentScenesPage>(
      {
        scenes: [
          groupScene,
          { scene_key: "cidDm==", kind: "dm", title: null, has_prompt: false },
        ],
        has_more: true,
      },
      AgentScenesPageSchema,
      EMPTY_AGENT_SCENES_PAGE,
      opts,
    );
    expect(page.hasMore).toBe(true);
    expect(page.scenes[0]).toEqual({
      sceneKey: "cidGroup==",
      kind: "group",
      title: "Release crew",
      orgId: "ding-org",
      lastActiveAt: "2026-09-30T08:00:00Z",
      inboundSessionId: "44444444-4444-4444-8444-444444444444",
      inboundCount: 3,
      memoryId: "55555555-5555-4555-8555-555555555555",
      hasPrompt: true,
    });
    expect(page.scenes[1]).toMatchObject({
      sceneKey: "cidDm==",
      kind: "dm",
      title: "",
      inboundSessionId: "",
      inboundCount: 0,
      memoryId: "",
      hasPrompt: false,
    });
  });

  it("drops malformed rows and unknown flags instead of emptying the list", () => {
    const page = AgentScenesPageSchema.parse({
      scenes: [
        { kind: "group" },
        { ...groupScene, kind: "channel", has_prompt: "true", inbound_count: -2 },
      ],
      has_more: "yes",
    });
    expect(page.hasMore).toBe(false);
    expect(page.scenes).toHaveLength(1);
    expect(page.scenes[0]).toMatchObject({ kind: "group", hasPrompt: false, inboundCount: 0 });
  });

  it("falls back to an empty page for a malformed body", () => {
    expect(
      parseWithFallback<AgentScenesPage>("nope", AgentScenesPageSchema, EMPTY_AGENT_SCENES_PAGE, opts),
    ).toEqual(EMPTY_AGENT_SCENES_PAGE);
    expect(AgentScenesPageSchema.parse({ scenes: null })).toEqual(EMPTY_AGENT_SCENES_PAGE);
  });
});

describe("admin scene detail", () => {
  const body = {
    scene: groupScene,
    prompt: { text: "Answer in Chinese.", updated_at: "2026-09-30T09:00:00Z", updated_by_name: "Ada" },
    bindings: [
      {
        resource_type: "connector",
        resource_id: connectorId,
        enabled: true,
        updated_by_name: "Bob",
        updated_at: "2026-09-30T10:00:00Z",
      },
      { resource_type: "future", resource_id: skillId, enabled: true },
      { resource_type: "skill", resource_id: skillId, enabled: 1 },
    ],
    offers: {
      connectors: [{ id: connectorId, name: "GitHub", catalog_slug: "github", auth_mode: "oauth", upstream_url: "https://x" }],
      skills: [{ id: skillId, name: "Report", description: null }],
    },
  };

  it("maps prompt, bindings and offers and drops unknown resource types", () => {
    const detail = parseWithFallback<AgentSceneDetail | null>(body, AgentSceneDetailSchema, null, opts);
    expect(detail?.prompt).toEqual({
      text: "Answer in Chinese.",
      updatedAt: "2026-09-30T09:00:00Z",
      updatedByName: "Ada",
    });
    expect(detail?.bindings).toEqual([
      {
        resourceType: "connector",
        resourceId: connectorId,
        enabled: true,
        updatedByName: "Bob",
        updatedAt: "2026-09-30T10:00:00Z",
      },
      { resourceType: "skill", resourceId: skillId, enabled: false, updatedByName: "", updatedAt: "" },
    ]);
    expect(detail?.offers).toEqual({
      connectors: [{ id: connectorId, name: "GitHub", catalogSlug: "github", authMode: "oauth" }],
      skills: [{ id: skillId, name: "Report", description: "" }],
    });
    // Private connector fields never reach the view model.
    expect(JSON.stringify(detail)).not.toContain("upstream_url");
  });

  it("tolerates a missing prompt and offers", () => {
    const detail = AgentSceneDetailSchema.parse({ scene: { scene_key: "cidDm==", kind: "dm" }, prompt: null, bindings: null });
    expect(detail.prompt).toEqual({ text: "", updatedAt: "", updatedByName: "" });
    expect(detail.bindings).toEqual([]);
    expect(detail.offers).toEqual({ connectors: [], skills: [] });
    expect(detail.scene.kind).toBe("dm");
  });

  it("returns null without a scene identity", () => {
    expect(parseWithFallback({ scene: { title: "x" } }, AgentSceneDetailSchema, null, opts)).toBeNull();
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

describe("admin scene client", () => {
  it("lists scenes with paging parameters and a pinned workspace", async () => {
    const fetch = stubFetch({ scenes: [groupScene], has_more: false });
    const page = await new ApiClient(base).listAgentScenes("ws-1", agentId, { limit: 50, offset: 100 });
    const { url, init } = requestOf(fetch);
    expect(url).toBe(`${base}/api/agents/${agentId}/scenes?limit=50&offset=100`);
    expect(init.headers["X-Workspace-ID"]).toBe("ws-1");
    expect(init.headers["X-Workspace-Slug"]).toBe("");
    expect(page.scenes[0]?.sceneKey).toBe("cidGroup==");
  });

  it("encodes scene keys in every scene path", async () => {
    const fetch = stubFetch({ scene: groupScene });
    await new ApiClient(base).getAgentScene("ws-1", agentId, "cid+/=");
    expect(requestOf(fetch).url).toBe(`${base}/api/agents/${agentId}/scenes/cid%2B%2F%3D`);
  });

  it("returns null for a malformed scene detail", async () => {
    stubFetch({ scene: null });
    expect(await new ApiClient(base).getAgentScene("ws-1", agentId, "cid1")).toBeNull();
  });

  it("saves the prompt and falls back to the trimmed text on a malformed echo", async () => {
    const fetch = stubFetch({ ok: true });
    const prompt = await new ApiClient(base).setAgentScenePrompt("ws-1", agentId, "cid1", "  Be brief.  ");
    const { url, init } = requestOf(fetch);
    expect(url).toBe(`${base}/api/agents/${agentId}/scenes/cid1/prompt`);
    expect(init.method).toBe("PUT");
    expect(JSON.parse(init.body as string)).toEqual({ prompt: "  Be brief.  " });
    expect(prompt).toEqual({ text: "Be brief.", updatedAt: "", updatedByName: "" });
  });

  it("maps the saved prompt echo", async () => {
    stubFetch({ prompt: { text: "Be brief.", updated_at: "t", updated_by_name: "Ada" } });
    expect(await new ApiClient(base).setAgentScenePrompt("ws-1", agentId, "cid1", "Be brief.")).toEqual({
      text: "Be brief.",
      updatedAt: "t",
      updatedByName: "Ada",
    });
  });

  it("sends admin binding toggles in snake_case", async () => {
    const fetch = stubFetch({
      binding: { resource_type: "skill", resource_id: skillId, enabled: true, updated_by_name: "Ada", updated_at: "t" },
    });
    const binding = await new ApiClient(base).setAgentSceneBinding("ws-1", agentId, "cid1", {
      resourceType: "skill",
      resourceId: skillId,
      enabled: true,
    });
    const { url, init } = requestOf(fetch);
    expect(url).toBe(`${base}/api/agents/${agentId}/scenes/cid1/bindings`);
    expect(JSON.parse(init.body as string)).toEqual({ resource_type: "skill", resource_id: skillId, enabled: true });
    expect(binding).toEqual({ resourceType: "skill", resourceId: skillId, enabled: true, updatedByName: "Ada", updatedAt: "t" });
  });

  it("falls back to the request for a malformed binding echo", async () => {
    stubFetch({ ok: true });
    const binding = await new ApiClient(base).setAgentSceneBinding("ws-1", agentId, "cid1", {
      resourceType: "connector",
      resourceId: connectorId,
      enabled: false,
    });
    expect(binding).toEqual({
      resourceType: "connector",
      resourceId: connectorId,
      enabled: false,
      updatedByName: "",
      updatedAt: "",
    });
  });

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
