import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient } from "./client";
import { parseWithFallback } from "./schema";
import type { ConnectedAppDetail, ConnectedAppsList } from "../types/context-capability";
import { ConnectedAppDetailSchema, ConnectedAppsListSchema } from "./context-capability-schema";

afterEach(() => vi.unstubAllGlobals());

const base = "https://pre.example.test";
const agentId = "11111111-1111-4111-8111-111111111111";
const connectorId = "22222222-2222-4222-8222-222222222222";
const opts = { endpoint: "test", includeReceived: false };

function stubFetch(body: unknown, status = 200) {
  const fetch = vi.fn().mockResolvedValue(new Response(body === undefined ? null : JSON.stringify(body), { status }));
  vi.stubGlobal("fetch", fetch);
  return fetch;
}

function requestOf(fetch: ReturnType<typeof vi.fn>) {
  const [url, init] = fetch.mock.calls[0] as [string, RequestInit & { headers: Record<string, string> }];
  return { url, init };
}

const githubWire = {
  slug: "github",
  name: "GitHub",
  description: "Repositories",
  auth_kind: "oauth_github_app",
  oauth_available: false,
  allows_pat: true,
  install_url: "https://github.com/apps/multica/installations/new",
  connector_id: connectorId,
  added: true,
  enabled_in_workspace: true,
  global_enabled: true,
  offered: true,
  write_enabled: false,
  tools: { discovered: 48, allowed: 20, read_only: 20 },
  shared_account: { connected: true, account: "@octocat", source: "workspace" },
  usage: { scenes_enabled: 2, scenes_connected: 1, persons_enabled: 3, persons_connected: 0 },
};

const notionWire = {
  slug: "notion",
  name: "Notion",
  auth_kind: "oauth_dcr",
  oauth_available: true,
  allows_pat: false,
  install_url: "",
  connector_id: null,
  added: false,
  enabled_in_workspace: false,
  global_enabled: false,
  offered: false,
  write_enabled: false,
  tools: { discovered: 0, allowed: 0, read_only: 0 },
  shared_account: { connected: false, account: "" },
  usage: { scenes_enabled: 0, scenes_connected: 0, persons_enabled: 0, persons_connected: 0 },
};

function parseList(body: unknown) {
  return parseWithFallback<ConnectedAppsList | null>(body, ConnectedAppsListSchema, null, opts);
}

describe("connected apps list", () => {
  it("maps every app with its server status to camelCase", () => {
    const list = parseList({ apps: [githubWire, notionWire], can_admin: true });
    expect(list?.canAdmin).toBe(true);
    expect(list?.apps[0]).toEqual({
      slug: "github",
      name: "GitHub",
      oauthAvailable: false,
      allowsPat: true,
      installUrl: "https://github.com/apps/multica/installations/new",
      connectorId,
      added: true,
      enabledInWorkspace: true,
      globalEnabled: true,
      offered: true,
      writeEnabled: false,
      tools: { discovered: 48, allowed: 20 },
      sharedAccount: { connected: true, account: "@octocat", source: "workspace" },
      usage: { scenesEnabled: 2, scenesConnected: 1, personsEnabled: 3, personsConnected: 0 },
    });
    expect(list?.apps[1]).toMatchObject({ slug: "notion", connectorId: null, added: false });
  });

  it("only trusts literal true flags and never reports state on a missing connector", () => {
    const list = parseList({
      apps: [
        {
          ...githubWire,
          oauth_available: "true",
          allows_pat: 1,
          global_enabled: "yes",
          shared_account: { connected: "true", account: null },
        },
        // Flags on an app without a workspace connector cannot be real.
        { ...notionWire, global_enabled: true, offered: true, shared_account: { connected: true, account: "@x" } },
      ],
      can_admin: "true",
    });
    expect(list?.canAdmin).toBe(false);
    expect(list?.apps[0]).toMatchObject({
      oauthAvailable: false,
      allowsPat: false,
      globalEnabled: false,
      sharedAccount: { connected: false, account: "" },
    });
    expect(list?.apps[1]).toMatchObject({ globalEnabled: false, offered: false, sharedAccount: { connected: false } });
  });

  it("defaults missing counts, drops malformed apps and unsafe install links", () => {
    const list = parseList({
      apps: [
        { ...githubWire, tools: null, usage: { scenes_enabled: -1, persons_enabled: "2" }, install_url: "javascript:alert(1)" },
        { ...notionWire, slug: "Not A Slug" },
        "garbage",
        { ...notionWire, connector_id: 42 },
      ],
      can_admin: false,
    });
    expect(list?.apps.map((app) => app.slug)).toEqual(["github", "notion"]);
    expect(list?.apps[0]).toMatchObject({
      installUrl: "",
      tools: { discovered: 0, allowed: 0 },
      usage: { scenesEnabled: 0, scenesConnected: 0, personsEnabled: 0, personsConnected: 0 },
    });
    // A malformed connector id reads as "not in the workspace yet".
    expect(list?.apps[1]).toMatchObject({ connectorId: null });
  });

  it("reports where a connected shared account comes from, never for a missing account", () => {
    const list = parseList({
      apps: [
        { ...githubWire, shared_account: { connected: true, account: "", source: "environment" } },
        { ...githubWire, slug: "linear", shared_account: { connected: true, account: "@x", source: "vault" } },
        { ...githubWire, slug: "sentry", shared_account: { connected: false, account: "", source: "workspace" } },
        // An older backend sends no source.
        { ...githubWire, slug: "asana", shared_account: { connected: true, account: "@y" } },
      ],
      can_admin: true,
    });
    expect(list?.apps.map((app) => app.sharedAccount.source)).toEqual(["environment", "", "", ""]);
    expect(list?.apps[0]?.sharedAccount).toEqual({ connected: true, account: "", source: "environment" });
  });

  it("returns null for a malformed body instead of an empty gallery", () => {
    expect(parseList({ apps: [githubWire] })).toMatchObject({ canAdmin: false });
    expect(parseList(null)).toBeNull();
    expect(parseList("nope")).toBeNull();
    // A non-array apps field degrades to an empty list, not a crash.
    expect(parseList({ apps: "nope", can_admin: true })).toEqual({ apps: [], canAdmin: true });
  });
});

describe("connected app detail", () => {
  const groupSceneId = "66666666-6666-4666-8666-666666666666";
  const dmSceneId = "77777777-7777-4777-8777-777777777777";
  const detailWire = {
    ...githubWire,
    scenes: [
      {
        scene_id: groupSceneId,
        scene_key: groupSceneId,
        title: "Release crew",
        kind: "group",
        enabled: true,
        connected: true,
        account: "@team",
      },
      // An older shape without scene_id: scene_key carries the scene_id.
      { scene_key: dmSceneId, title: null, kind: "dm", enabled: false, connected: "true", account: null },
      { scene_key: "", title: "no key" },
      { scene_id: null, title: "no id" },
    ],
    persons: [
      { scope_key: "staff-1", title: "Ada", enabled: true, connected: false, account: "", share_in_groups: true },
      { scope_key: null, title: "broken" },
    ],
    tool_list: [
      { name: "search_code", read_only: true, allowed: true },
      { name: "create_issue", read_only: false, allowed: false },
      { name: "", read_only: true },
    ],
  };

  it("maps usage lists and tools, dropping malformed rows", () => {
    const detail = parseWithFallback<ConnectedAppDetail | null>(
      { ...detailWire, can_admin: true },
      ConnectedAppDetailSchema,
      null,
      opts,
    );
    expect(detail).toMatchObject({ slug: "github", globalEnabled: true, sharedAccount: { connected: true }, canAdmin: true });
    expect(detail?.scenes).toEqual([
      {
        sceneId: groupSceneId,
        sceneKey: groupSceneId,
        title: "Release crew",
        kind: "group",
        enabled: true,
        connected: true,
        account: "@team",
      },
      { sceneId: dmSceneId, sceneKey: dmSceneId, title: "", kind: "dm", enabled: false, connected: false, account: "" },
    ]);
    expect(detail?.persons).toEqual([
      { scopeKey: "staff-1", title: "Ada", enabled: true, connected: false, account: "", shareInGroups: true },
    ]);
    expect(detail?.toolList).toEqual([
      { name: "search_code", readOnly: true, allowed: true },
      { name: "create_issue", readOnly: false, allowed: false },
    ]);
  });

  it("tolerates missing lists and rejects a detail without an app identity", () => {
    const detail = parseWithFallback<ConnectedAppDetail | null>(
      { ...notionWire, scenes: null },
      ConnectedAppDetailSchema,
      null,
      opts,
    );
    // can_admin is missing: the page stays read-only.
    expect(detail).toMatchObject({ slug: "notion", scenes: [], persons: [], toolList: [], canAdmin: false });
    expect(
      parseWithFallback<ConnectedAppDetail | null>({ ...detailWire, can_admin: "true" }, ConnectedAppDetailSchema, null, opts)
        ?.canAdmin,
    ).toBe(false);
    expect(parseWithFallback({ scenes: [] }, ConnectedAppDetailSchema, null, opts)).toBeNull();
  });
});

describe("connected apps client", () => {
  it("lists apps with the workspace pinned", async () => {
    const fetch = stubFetch({ apps: [githubWire], can_admin: true });
    const list = await new ApiClient(base).listAgentConnectedApps("ws-1", agentId);
    const { url, init } = requestOf(fetch);
    expect(url).toBe(`${base}/api/agents/${agentId}/connected-apps`);
    expect(init.headers["X-Workspace-ID"]).toBe("ws-1");
    expect(list?.apps[0]?.slug).toBe("github");
  });

  it("returns null for a malformed list and detail", async () => {
    stubFetch({ nope: true, can_admin: 1, apps: 5 });
    expect(await new ApiClient(base).listAgentConnectedApps("ws-1", agentId)).toEqual({ apps: [], canAdmin: false });
    stubFetch([1, 2]);
    expect(await new ApiClient(base).listAgentConnectedApps("ws-1", agentId)).toBeNull();
    stubFetch({ slug: 7 });
    expect(await new ApiClient(base).getAgentConnectedApp("ws-1", agentId, "github")).toBeNull();
  });

  it("encodes the app slug in the detail path", async () => {
    const fetch = stubFetch({ ...githubWire, scenes: [], persons: [], tool_list: [] });
    await new ApiClient(base).getAgentConnectedApp("ws-1", agentId, "git hub");
    expect(requestOf(fetch).url).toBe(`${base}/api/agents/${agentId}/connected-apps/git%20hub`);
  });

  it("disconnects the workspace shared credential", async () => {
    const fetch = stubFetch(undefined, 204);
    await new ApiClient(base).deleteInternalConnectorCredential("ws-1", connectorId);
    const { url, init } = requestOf(fetch);
    expect(url).toBe(`${base}/api/workspaces/ws-1/internal-connectors/${connectorId}/credential`);
    expect(init.method).toBe("DELETE");
  });
});
