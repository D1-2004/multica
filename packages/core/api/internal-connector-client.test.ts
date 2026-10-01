import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient } from "./client";

afterEach(() => vi.unstubAllGlobals());

const base = "https://pre.example.test";
const workspaceId = "22222222-2222-4222-8222-222222222222";
const connectorId = "11111111-1111-4111-8111-111111111111";

function stubFetch(body: unknown, status = 200) {
  const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify(body), { status }));
  vi.stubGlobal("fetch", fetch);
  return fetch;
}

function requestOf(fetch: ReturnType<typeof vi.fn>) {
  const [url, init] = fetch.mock.calls[0] as [string, RequestInit & { headers: Record<string, string> }];
  return { url, init };
}

const connectorJson = {
  id: connectorId,
  workspace_id: workspaceId,
  name: "GitHub",
  upstream_url: "https://api.githubcopilot.com/mcp/",
  credential_ref: "MULTICA_INTERNAL_MCP_BEARER_X",
  credential_ready: false,
  auth_mode: "oauth",
  allowed_tools: [],
  agent_ids: [],
  enabled: true,
  catalog_slug: "github",
  write_enabled: false,
  discovered_tool_count: 0,
  credential_account: "",
};

describe("official app catalog client", () => {
  it("adds a catalog app with an encoded slug and returns the connector", async () => {
    const fetch = stubFetch({ connector: connectorJson });
    const connector = await new ApiClient(base).addCatalogConnector(workspaceId, "git hub");
    const { url, init } = requestOf(fetch);
    expect(url).toBe(`${base}/api/workspaces/${workspaceId}/connector-catalog/git%20hub`);
    expect(init.method).toBe("POST");
    expect(connector).toMatchObject({ id: connectorId, catalogSlug: "github", authMode: "oauth" });
  });

  it("returns null when the added connector echo is malformed", async () => {
    stubFetch({ connector: { id: connectorId } });
    expect(await new ApiClient(base).addCatalogConnector(workspaceId, "github")).toBeNull();
  });

  it("starts the shared-account OAuth flow with an optional return_to", async () => {
    const fetch = stubFetch({ authorize_url: "https://github.com/login/oauth/authorize?state=mcpc.s" });
    const client = new ApiClient(base);
    expect(await client.startInternalConnectorOAuth(workspaceId, connectorId)).toBe(
      "https://github.com/login/oauth/authorize?state=mcpc.s",
    );
    const { url, init } = requestOf(fetch);
    expect(url).toBe(`${base}/api/workspaces/${workspaceId}/internal-connectors/${connectorId}/oauth/start`);
    expect(init.method).toBe("POST");
    expect(JSON.parse(init.body as string)).toEqual({});

    const second = stubFetch({ authorize_url: "https://mcp.linear.app/authorize" });
    await client.startInternalConnectorOAuth(workspaceId, connectorId, "/acme/internal-connectors");
    expect(JSON.parse(requestOf(second).init.body as string)).toEqual({ return_to: "/acme/internal-connectors" });
  });

  it("refuses to hand a non-https authorization URL to navigation", async () => {
    stubFetch({ authorize_url: "javascript:alert(1)" });
    expect(await new ApiClient(base).startInternalConnectorOAuth(workspaceId, connectorId)).toBe("");
  });

  it("refreshes tools and maps the pinned list", async () => {
    const fetch = stubFetch({ discovered: 48, allowed_tools: ["get_me"] });
    const result = await new ApiClient(base).refreshInternalConnectorTools(workspaceId, connectorId);
    const { url, init } = requestOf(fetch);
    expect(url).toBe(`${base}/api/workspaces/${workspaceId}/internal-connectors/${connectorId}/tools/refresh`);
    expect(init.method).toBe("POST");
    expect(result).toEqual({ discovered: 48, allowedTools: ["get_me"] });
  });

  it("returns null for a malformed tools refresh", async () => {
    stubFetch({ discovered: -1 });
    expect(await new ApiClient(base).refreshInternalConnectorTools(workspaceId, connectorId)).toBeNull();
  });

  it("sends write_enabled in the connector update body", async () => {
    const fetch = stubFetch({});
    await new ApiClient(base).updateInternalConnector(workspaceId, connectorId, {
      name: "GitHub",
      upstream_url: "https://api.githubcopilot.com/mcp/",
      allowed_tools: [],
      agent_ids: [],
      enabled: true,
      auth_mode: "oauth",
      write_enabled: true,
    });
    const { init } = requestOf(fetch);
    expect(init.method).toBe("PATCH");
    expect(JSON.parse(init.body as string)).toMatchObject({ auth_mode: "oauth", write_enabled: true });
  });
});
