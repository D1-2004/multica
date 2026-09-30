import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient } from "./client";

afterEach(() => vi.unstubAllGlobals());

const base = "https://pre.example.test";
const agentId = "11111111-1111-4111-8111-111111111111";
const connectorId = "22222222-2222-4222-8222-222222222222";

function stubFetch(body: unknown, status = 200) {
  const fetch = vi.fn().mockResolvedValue(
    status === 204 ? new Response(null, { status }) : new Response(JSON.stringify(body), { status }),
  );
  vi.stubGlobal("fetch", fetch);
  return fetch;
}

function requestOf(fetch: ReturnType<typeof vi.fn>) {
  const [url, init] = fetch.mock.calls[0] as [string, RequestInit & { headers: Record<string, string> }];
  return { url, init };
}

describe("context capability mobile client", () => {
  it("redeems a link without a workspace header", async () => {
    const fetch = stubFetch({
      agent_id: agentId,
      workspace_id: "ws-1",
      scope_type: "scene",
      scope_key: "cid1",
      scope_title: "Team",
    });
    const result = await new ApiClient(base).redeemContextConfigLink("tok");
    const { url, init } = requestOf(fetch);
    expect(url).toBe(`${base}/api/context-capabilities/links/redeem`);
    expect(init.method).toBe("POST");
    expect(init.body).toBe(JSON.stringify({ token: "tok" }));
    expect(init.headers["X-Workspace-Slug"]).toBe("");
    expect(result).toEqual({ agentId, workspaceId: "ws-1", scopeType: "scene", scopeKey: "cid1", scopeTitle: "Team" });
  });

  it("returns null for a malformed agent detail instead of an empty agent", async () => {
    stubFetch({ agent: { name: "missing id" } });
    expect(await new ApiClient(base).getContextConfigAgent(agentId)).toBeNull();
  });

  it("encodes scene keys in the scene path", async () => {
    const fetch = stubFetch({
      scene: { scope_key: "cid+/=", scope_title: "Team", source: "agent_link", expires_at: "" },
      bindings: [],
      credentials: [],
    });
    await new ApiClient(base).getContextConfigScene(agentId, "cid+/=");
    expect(requestOf(fetch).url).toBe(
      `${base}/api/context-capabilities/agents/${agentId}/scenes/cid%2B%2F%3D`,
    );
  });

  it("sends snake_case binding writes and falls back to the request on a malformed echo", async () => {
    const fetch = stubFetch({ ok: true });
    const result = await new ApiClient(base).setContextCapabilityBinding(agentId, {
      scopeType: "person",
      scopeKey: "staff-1",
      resourceType: "connector",
      resourceId: connectorId,
      enabled: true,
    });
    const { url, init } = requestOf(fetch);
    expect(url).toBe(`${base}/api/context-capabilities/agents/${agentId}/bindings`);
    expect(init.method).toBe("PUT");
    expect(JSON.parse(init.body as string)).toEqual({
      scope_type: "person",
      scope_key: "staff-1",
      resource_type: "connector",
      resource_id: connectorId,
      enabled: true,
    });
    expect(result).toEqual({ resourceType: "connector", resourceId: connectorId, enabled: true });
  });

  it("never returns the bearer from a credential write", async () => {
    stubFetch({ credential: { connector_id: connectorId, hint: "••••cret", updated_at: "t", bearer: "super-secret" } });
    const result = await new ApiClient(base).setContextConnectorCredential(agentId, {
      scopeType: "scene",
      scopeKey: "cid1",
      connectorId,
      bearer: "super-secret",
    });
    expect(result).toEqual({ connectorId, hint: "••••cret", updatedAt: "t", kind: "bearer" });
  });

  it("starts an OAuth connection without a workspace header and returns the authorization URL", async () => {
    const fetch = stubFetch({ authorize_url: "https://github.com/login/oauth/authorize?state=mcpc.x" });
    const url = await new ApiClient(base).startContextConnectorConnection(agentId, {
      scopeType: "person",
      scopeKey: "staff-1",
      connectorId,
    });
    const request = requestOf(fetch);
    expect(request.url).toBe(`${base}/api/context-capabilities/agents/${agentId}/connections/start`);
    expect(request.init.method).toBe("POST");
    expect(request.init.headers["X-Workspace-Slug"]).toBe("");
    expect(JSON.parse(request.init.body as string)).toEqual({
      scope_type: "person",
      scope_key: "staff-1",
      connector_id: connectorId,
    });
    expect(url).toBe("https://github.com/login/oauth/authorize?state=mcpc.x");
  });

  it("forwards return_to only when given", async () => {
    const fetch = stubFetch({ authorize_url: "https://mcp.notion.com/authorize" });
    await new ApiClient(base).startContextConnectorConnection(agentId, {
      scopeType: "scene",
      scopeKey: "cid1",
      connectorId,
      returnTo: "/dingtalk/configure?agent=a",
    });
    expect(JSON.parse(requestOf(fetch).init.body as string)).toMatchObject({
      return_to: "/dingtalk/configure?agent=a",
    });
  });

  it("never navigates to a malformed or non-https authorization URL", async () => {
    stubFetch({ authorize_url: "javascript:alert(document.cookie)" });
    expect(
      await new ApiClient(base).startContextConnectorConnection(agentId, {
        scopeType: "person",
        scopeKey: "staff-1",
        connectorId,
      }),
    ).toBe("");
    stubFetch({ redirect: "https://github.com" });
    expect(
      await new ApiClient(base).startContextConnectorConnection(agentId, {
        scopeType: "person",
        scopeKey: "staff-1",
        connectorId,
      }),
    ).toBe("");
  });

  it("deletes credentials with the scope in the query string", async () => {
    const fetch = stubFetch(null, 204);
    await new ApiClient(base).deleteContextConnectorCredential(agentId, {
      scopeType: "scene",
      scopeKey: "cid1",
      connectorId,
    });
    const { url, init } = requestOf(fetch);
    expect(init.method).toBe("DELETE");
    const parsed = new URL(url);
    expect(parsed.pathname).toBe(`/api/context-capabilities/agents/${agentId}/credentials`);
    expect(Object.fromEntries(parsed.searchParams)).toEqual({
      scope_type: "scene",
      scope_key: "cid1",
      connector_id: connectorId,
    });
  });

  it("sends only the ids the JSAPI picker returned", async () => {
    const fetch = stubFetch({ scene: { scope_key: "cid1", scope_title: "Team", source: "jsapi", expires_at: "" } });
    const scene = await new ApiClient(base).resolveContextConfigScene(agentId, { chatId: "chat-1" });
    expect(JSON.parse(requestOf(fetch).init.body as string)).toEqual({ chat_id: "chat-1" });
    expect(scene).toEqual({ scopeKey: "cid1", scopeTitle: "Team", source: "jsapi", expiresAt: "" });
  });

  it("asks for a JSAPI signature of the exact page URL", async () => {
    const fetch = stubFetch({ corp_id: "ding1", agent_id: "9", time_stamp: "1", nonce_str: "n", signature: "s" });
    const config = await new ApiClient(base).getDingTalkJsapiConfig("https://app.example/dingtalk/configure?agent=a");
    expect(new URL(requestOf(fetch).url).searchParams.get("url")).toBe(
      "https://app.example/dingtalk/configure?agent=a",
    );
    expect(config?.corpId).toBe("ding1");
  });
});

describe("context capability admin client", () => {
  it("pins the workspace and replaces offers with snake_case ids", async () => {
    const fetch = stubFetch({ enabled: true, offers: { connector_ids: [connectorId], skill_ids: [] } });
    const result = await new ApiClient(base).setAgentContextCapabilityOffers("ws-1", agentId, {
      connectorIds: [connectorId],
      skillIds: [],
    });
    const { url, init } = requestOf(fetch);
    expect(url).toBe(`${base}/api/agents/${agentId}/context-capabilities/offers`);
    expect(init.method).toBe("PUT");
    expect(init.headers["X-Workspace-ID"]).toBe("ws-1");
    expect(JSON.parse(init.body as string)).toEqual({ connector_ids: [connectorId], skill_ids: [] });
    expect(result?.offers.connectorIds).toEqual([connectorId]);
  });

  it("returns null for a malformed tab body", async () => {
    stubFetch({ enabled: true, library: { connectors: [{ id: 5 }] } });
    expect(await new ApiClient(base).getAgentContextCapabilities("ws-1", agentId)).toBeNull();
  });
});
