import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient } from "./client";
import {
  ContextConfigAgentDetailSchema,
  ContextConfigSceneDetailSchema,
  ContextNodeDetailSchema,
  ContextPromptComponentsResponseSchema,
} from "./context-capability-schema";

afterEach(() => vi.unstubAllGlobals());

const base = "https://pre.example.test";
const agentId = "11111111-1111-4111-8111-111111111111";
const scene = { scope_key: "cidGroup", scope_title: "Release crew", source: "agent_link", expires_at: "", kind: "group" };

function stubFetch(body: unknown, status = 200) {
  const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify(body), { status }));
  vi.stubGlobal("fetch", fetch);
  return fetch;
}

function requestOf(fetch: ReturnType<typeof vi.fn>) {
  const [url, init] = fetch.mock.calls[0] as [string, RequestInit & { headers: Record<string, string> }];
  return { url, init };
}

describe("configure-page scope rights, prompts and MCP servers", () => {
  it("maps a scene's rights, prompts in merge order and its own MCP servers", () => {
    const detail = ContextConfigSceneDetailSchema.parse({
      scene,
      rights: { toggle: true, connect: true, edit_prompts: true, edit_mcp: false },
      prompts: [
        { id: "p2", name: "Format", order: 2, text: "Use lists.", enabled: false },
        { id: "p1", name: "Tone", order: 1, text: "Be brief." },
      ],
      mcp_config: { mcpServers: { docs: { url: "https://mcp.example/docs" } } },
      mcp_config_redacted: false,
    });
    expect(detail.rights).toEqual({ toggle: true, connect: true, editPrompts: true, editMcp: false, editRoutines: false });
    expect(detail.prompts).toEqual([
      // A prompt without a switch (an older backend) is on.
      { id: "p1", name: "Tone", order: 1, text: "Be brief.", enabled: true, updatedByName: "", updatedAt: "" },
      { id: "p2", name: "Format", order: 2, text: "Use lists.", enabled: false, updatedByName: "", updatedAt: "" },
    ]);
    expect(detail.mcpConfig).toEqual({ mcpServers: { docs: { url: "https://mcp.example/docs" } } });
    expect(detail.mcpConfigRedacted).toBe(false);
  });

  it("lists the apps a scene signs in to with its own OAuth application, tolerating drift", () => {
    expect(ContextConfigSceneDetailSchema.parse({ scene, scene_oauth_apps: ["slack", "", 3, "asana"] }).sceneOAuthApps).toEqual([
      "slack",
      "asana",
    ]);
    // An older backend sends none; a malformed value reads as none.
    expect(ContextConfigSceneDetailSchema.parse({ scene }).sceneOAuthApps).toEqual([]);
    expect(ContextConfigSceneDetailSchema.parse({ scene, scene_oauth_apps: "slack" }).sceneOAuthApps).toEqual([]);
  });

  it("grants a right only for a literal true and reads missing or malformed rights as none sent", () => {
    const parse = (rights: unknown) => ContextConfigSceneDetailSchema.parse({ scene, rights }).rights;
    expect(parse({ toggle: "true", connect: 1, edit_prompts: null })).toEqual({
      toggle: false,
      connect: false,
      editPrompts: false,
      editMcp: false,
      editRoutines: false,
    });
    expect(parse(undefined)).toBeNull();
    expect(parse(null)).toBeNull();
    expect(parse("all")).toBeNull();
    expect(parse([true])).toBeNull();
  });

  it("tolerates malformed prompts and MCP documents without losing the scope", () => {
    const detail = ContextConfigSceneDetailSchema.parse({
      scene,
      prompts: [{ id: "p1", name: "", text: "no name" }, "bad", { id: "p2", name: "Tone", order: "x", enabled: "no" }],
      mcp_config: ["not", "an", "object"],
      mcp_config_redacted: "yes",
    });
    // Only a literal false switches a prompt off.
    expect(detail.prompts).toEqual([
      { id: "p2", name: "Tone", order: 0, text: "", enabled: true, updatedByName: "", updatedAt: "" },
    ]);
    expect(detail.mcpConfig).toBeNull();
    expect(detail.mcpConfigRedacted).toBe(false);
    expect(ContextConfigSceneDetailSchema.parse({ scene, prompts: "nope" }).prompts).toEqual([]);
  });

  it("never shows a withheld MCP document", () => {
    const detail = ContextConfigSceneDetailSchema.parse({
      scene,
      mcp_config: { mcpServers: { docs: { url: "https://mcp.example/docs" } } },
      mcp_config_redacted: true,
    });
    expect(detail.mcpConfig).toBeNull();
    expect(detail.mcpConfigRedacted).toBe(true);
  });

  it("maps the person and enterprise levels of the agent detail", () => {
    const detail = ContextConfigAgentDetailSchema.parse({
      agent: { id: agentId, name: "Helper" },
      person: {
        scope_key: "staff-1",
        scope_title: "Ada",
        rights: { toggle: true, connect: true, edit_prompts: true, edit_mcp: true },
        prompts: [{ id: "p1", name: "Tone", order: 1, text: "Be brief.", enabled: true }],
        mcp_config: null,
      },
      org: {
        scope_key: "dingA",
        scope_title: "Acme",
        can_edit: true,
        rights: { toggle: true, connect: true, edit_prompts: true, edit_mcp: true },
        mcp_config: { mcpServers: { wiki: { url: "https://wiki.example/mcp", disabled: true } } },
      },
    });
    expect(detail.person?.rights?.editMcp).toBe(true);
    expect(detail.person?.prompts.map((prompt) => prompt.name)).toEqual(["Tone"]);
    expect(detail.person?.mcpConfig).toBeNull();
    expect(detail.org?.rights).toEqual({ toggle: true, connect: true, editPrompts: true, editMcp: true, editRoutines: false });
    expect(detail.org?.mcpConfig).toEqual({ mcpServers: { wiki: { url: "https://wiki.example/mcp", disabled: true } } });
  });

  it("reads the Context Builder node's rights and prompt switches", () => {
    const node = ContextNodeDetailSchema.parse({
      prompts: [{ id: "p1", name: "Tone", order: 1, text: "Be brief.", enabled: false }],
      rights: { toggle: false, connect: false, edit_prompts: false, edit_mcp: false },
    });
    expect(node.prompts[0]?.enabled).toBe(false);
    expect(node.rights).toEqual({ toggle: false, connect: false, editPrompts: false, editMcp: false, editRoutines: false });
    expect(ContextNodeDetailSchema.parse({}).rights).toBeNull();
  });

  it("reads a malformed prompts echo as null", () => {
    expect(ContextPromptComponentsResponseSchema.parse({ prompts: "x" })).toBeNull();
    expect(ContextPromptComponentsResponseSchema.parse({ prompts: [{ name: "Tone", enabled: false }] })?.[0]?.enabled).toBe(
      false,
    );
  });
});

describe("configure-page prompt and MCP writes", () => {
  it("replaces a scope's prompts without a workspace header", async () => {
    const fetch = stubFetch({
      prompts: [{ id: "p1", name: "Tone", order: 1, text: "Be brief.", enabled: false, updated_by_name: "Ada" }],
    });
    const saved = await new ApiClient(base).setContextConfigPrompts(
      agentId,
      { scopeType: "scene", scopeKey: "cidB", orgId: "dingB" },
      [
        { name: "Tone", order: 1, text: "Be brief.", enabled: false },
        { name: "Format", order: 2, text: "Use lists." },
      ],
    );
    const { url, init } = requestOf(fetch);
    expect(url).toBe(`${base}/api/context-capabilities/agents/${agentId}/prompts`);
    expect(init.method).toBe("PUT");
    expect(init.headers["X-Workspace-Slug"]).toBe("");
    expect(JSON.parse(init.body as string)).toEqual({
      scope_type: "scene",
      scope_key: "cidB",
      org_id: "dingB",
      prompts: [
        { name: "Tone", order: 1, text: "Be brief.", enabled: false },
        // A missing switch is sent as on.
        { name: "Format", order: 2, text: "Use lists.", enabled: true },
      ],
    });
    expect(saved).toEqual([
      { id: "p1", name: "Tone", order: 1, text: "Be brief.", enabled: false, updatedByName: "Ada", updatedAt: "" },
    ]);
  });

  it("returns null for a malformed prompts echo", async () => {
    stubFetch({ ok: true });
    expect(
      await new ApiClient(base).setContextConfigPrompts(agentId, { scopeType: "person", scopeKey: "staff-1" }, []),
    ).toBeNull();
  });

  it("replaces a scope's MCP servers and keeps what was sent when the echo is malformed", async () => {
    const config = { mcpServers: { docs: { url: "https://mcp.example/docs", disabled: true } } };
    const fetch = stubFetch({ mcp_config: config });
    const client = new ApiClient(base);
    expect(await client.setContextConfigMcpConfig(agentId, { scopeType: "person", scopeKey: "staff-1" }, config)).toEqual(
      config,
    );
    const { url, init } = requestOf(fetch);
    expect(url).toBe(`${base}/api/context-capabilities/agents/${agentId}/mcp-config`);
    expect(init.method).toBe("PUT");
    // No org_id for the agent's own org.
    expect(JSON.parse(init.body as string)).toEqual({ scope_type: "person", scope_key: "staff-1", mcp_config: config });

    stubFetch({ mcp_config: "bad" });
    expect(await client.setContextConfigMcpConfig(agentId, { scopeType: "org", scopeKey: "dingA", orgId: "dingA" }, config)).toEqual(
      config,
    );
    stubFetch({ mcp_config: null });
    expect(await client.setContextConfigMcpConfig(agentId, { scopeType: "org", scopeKey: "dingA" }, config)).toBeNull();
  });
});
