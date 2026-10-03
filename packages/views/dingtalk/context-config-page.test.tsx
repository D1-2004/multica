// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type {
  ContextConfigAgentDetail,
  ContextConfigAgentSummary,
  ContextConfigSceneDetail,
  ContextOfferedConnector,
} from "@multica/core/context-capabilities";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../locales/en/common.json";
import enAgents from "../locales/en/agents.json";

const TEST_RESOURCES = { en: { common: enCommon, agents: enAgents } };

const api = vi.hoisted(() => ({
  redeemContextConfigLink: vi.fn(),
  listContextConfigAgents: vi.fn(),
  getContextConfigAgent: vi.fn(),
  getContextConfigScene: vi.fn(),
  setContextCapabilityBinding: vi.fn(),
  setContextConnectorCredential: vi.fn(),
  deleteContextConnectorCredential: vi.fn(),
  resolveContextConfigScene: vi.fn(),
  startContextConnectorConnection: vi.fn(),
  setContextConfigPrompts: vi.fn(),
  setContextConfigMcpConfig: vi.fn(),
  listSceneRoutines: vi.fn(),
  listSceneRoutineRuns: vi.fn(),
  addContextConfigApp: vi.fn(),
  getContextConfigOAuthApp: vi.fn(),
  setContextConfigOAuthApp: vi.fn(),
  deleteContextConfigOAuthApp: vi.fn(),
  createSceneRoutine: vi.fn(),
  updateSceneRoutine: vi.fn(),
  deleteSceneRoutine: vi.fn(),
  runSceneRoutine: vi.fn(),
  rotateSceneRoutineWebhook: vi.fn(),
  listContextGitHubInstallations: vi.fn(),
}));

const { ApiError, errorCode } = vi.hoisted(() => {
  class ApiError extends Error {
    status: number;
    statusText: string;
    body?: unknown;
    constructor(message: string, status: number, statusText = "", body?: unknown) {
      super(message);
      this.status = status;
      this.statusText = statusText;
      this.body = body;
    }
  }
  // Mirrors errorCode from @multica/core/api.
  function errorCode(err: unknown): string | undefined {
    if (err instanceof ApiError && err.body && typeof err.body === "object") {
      const code = (err.body as { code?: unknown }).code;
      if (typeof code === "string" && code.length > 0) return code;
    }
    return undefined;
  }
  return { ApiError, errorCode };
});

vi.mock("@multica/core/api", () => ({ api, ApiError, errorCode }));

vi.mock("sonner", () => ({
  toast: { error: vi.fn(), success: vi.fn(), message: vi.fn() },
}));

import {
  ContextConfigPage,
  type ContextConfigBinding,
  type ContextConfigPageProps,
} from "./context-config-page";

const copy = enAgents.context_config;

// A scene's scope key is its scene_id (a group chat or a 1:1 chat alike).
const SALES_SCENE = "66666666-6666-4666-8666-666666666666";
const OPS_SCENE = "77777777-7777-4777-8777-777777777777";
const DM_SCENE = "88888888-8888-4888-8888-888888888888";
const PARTNER_SCENE = "99999999-9999-4999-8999-999999999999";

/** Scope content from a backend without rights, prompts or MCP servers. */
const noContent = { rights: null, prompts: [], mcpConfig: null, mcpConfigRedacted: false };

const agentSummary: ContextConfigAgentSummary = {
  id: "agent-1",
  name: "Helper",
  avatarUrl: null,
  workspaceId: "ws-1",
  access: "grant",
  scopes: [],
};

function agentDetail(overrides: Partial<ContextConfigAgentDetail> = {}): ContextConfigAgentDetail {
  return {
    agent: { id: "agent-1", name: "Helper", avatarUrl: null, workspaceId: "ws-1" },
    global: { connectors: [{ id: "conn-global", name: "Docs", catalogSlug: "" }], skills: [] },
    offers: {
      connectors: [
        {
          id: "conn-wiki",
          name: "Wiki",
          tools: ["search", "read"],
          acceptsCredential: true,
          credentialRequired: true,
          catalogSlug: "",
          authMode: "bearer",
          acceptsPat: false,
          oauthAvailable: false,
          installUrl: "",
        },
      ],
      skills: [{ id: "skill-report", name: "Weekly report", description: "Writes reports" }],
    },
    person: {
      scopeKey: "staff-1",
      scopeTitle: "Alice",
      source: "agent_link",
      expiresAt: "",
      bindings: [],
      credentials: [],
      ...noContent,
    },
    scenes: [{ scopeKey: SALES_SCENE, scopeTitle: "Sales team", source: "agent_link", expiresAt: "", kind: "group", orgId: "" }],
    tenant: null,
    tenants: [],
    org: null,
    jsapiAvailable: false,
    access: "grant",
    apps: [
      { slug: "github", name: "GitHub", setup: "automatic" as const, ready: true },
      { slug: "notion", name: "Notion", setup: "automatic" as const, ready: true },
      { slug: "linear", name: "Linear", setup: "automatic" as const, ready: true },
    ],
    ...overrides,
  };
}

const sceneDetail: ContextConfigSceneDetail = {
  scene: { scopeKey: SALES_SCENE, scopeTitle: "Sales team", source: "agent_link", expiresAt: "", kind: "group", orgId: "" },
  bindings: [{ resourceType: "connector", resourceId: "conn-wiki", enabled: true, shareInGroups: false }],
  credentials: [{ connectorId: "conn-wiki", hint: "••••abcd", updatedAt: "", kind: "bearer" }],
  scope: { type: "scene", key: SALES_SCENE, title: "Sales team" },
  canConnect: true,
  sceneOAuthApps: [],
  ...noContent,
};

const githubConnector: ContextOfferedConnector = {
  id: "conn-github",
  name: "GitHub",
  tools: ["get_me", "search_code"],
  acceptsCredential: false,
  credentialRequired: true,
  catalogSlug: "github",
  authMode: "oauth",
  acceptsPat: true,
  oauthAvailable: true,
  installUrl: "https://github.com/apps/multica/installations/new",
};

function oauthDetail(overrides: Partial<ContextConfigAgentDetail> = {}): ContextConfigAgentDetail {
  const detail = agentDetail(overrides);
  return {
    ...detail,
    offers: { connectors: [githubConnector], skills: [] },
    ...overrides,
  };
}

/** Accessible name of a connector tile. */
function tileName(name: string): string {
  return copy.app_open_aria.replace("{{name}}", name);
}

/** The tile of connector `name`; its accessible name also carries its state. */
function tile(region: HTMLElement, name: string): HTMLElement {
  return within(region).getByRole("button", { name: (accessible) => accessible.split(" · ")[0] === tileName(name) });
}

/** Opens the dialog of the connector tile `name` inside `region`. */
async function openConnector(user: ReturnType<typeof userEvent.setup>, region: HTMLElement, name: string) {
  await user.click(tile(region, name));
  return screen.findByRole("dialog");
}

/** A binding that switches connector `id` on. */
function connectorOn(id: string, shareInGroups = false) {
  return { resourceType: "connector" as const, resourceId: id, enabled: true, shareInGroups };
}

function renderPage(props: Partial<ContextConfigPageProps> = {}) {
  const onAuthRequired = props.onAuthRequired ?? vi.fn();
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <QueryClientProvider client={queryClient}>
        <ContextConfigPage {...props} onAuthRequired={onAuthRequired} />
      </QueryClientProvider>
    </I18nProvider>,
  );
  return { onAuthRequired };
}

beforeEach(() => {
  vi.clearAllMocks();
  api.listContextConfigAgents.mockResolvedValue([agentSummary]);
  api.getContextConfigAgent.mockResolvedValue(agentDetail());
  api.getContextConfigScene.mockResolvedValue(sceneDetail);
  api.listSceneRoutines.mockResolvedValue([]);
  api.listContextGitHubInstallations.mockResolvedValue({
    connected: true,
    installations: [],
    error: "",
    truncated: false,
  });
  api.setContextCapabilityBinding.mockImplementation(
    async (
      _agentId: string,
      input: { resourceType: string; resourceId: string; enabled: boolean; shareInGroups?: boolean },
    ) => ({
      resourceType: input.resourceType,
      resourceId: input.resourceId,
      enabled: input.enabled,
      shareInGroups: input.shareInGroups === true,
    }),
  );
});

describe("ContextConfigPage", () => {
  it("redeems a group link once and opens that group's settings", async () => {
    api.redeemContextConfigLink.mockResolvedValue({
      agentId: "agent-1",
      workspaceId: "ws-1",
      scopeType: "scene",
      scopeKey: SALES_SCENE,
      scopeTitle: "Sales team",
    });
    const user = userEvent.setup();
    const onBind = vi.fn();
    renderPage({ linkToken: "link-token", onBind });

    const region = await screen.findByRole("region", { name: "Sales team" });
    expect(api.redeemContextConfigLink).toHaveBeenCalledTimes(1);
    expect(api.redeemContextConfigLink).toHaveBeenCalledWith("link-token");
    expect(api.getContextConfigScene).toHaveBeenCalledWith("agent-1", SALES_SCENE, "");
    expect(onBind).toHaveBeenCalledWith({ agentId: "agent-1", scopeType: "scene", scopeKey: SALES_SCENE, orgId: "" });

    // Wiki is added to the group and authorized with the group's token.
    // One line: its account status, and 配置 once it is added.
    const wiki = tile(region, "Wiki");
    expect(within(wiki).getByText(copy.connected)).toBeInTheDocument();
    expect(within(region).getByRole("button", { name: copy.app_configure_aria.replace("{{name}}", "Wiki") })).toBeInTheDocument();
    const dialog = await openConnector(user, region, "Wiki");
    expect(within(dialog).getByText(copy.added_scene)).toBeInTheDocument();
    expect(within(dialog).getByText("Credential saved (••••abcd)")).toBeInTheDocument();
    await user.keyboard("{Escape}");

    await user.click(within(region).getByRole("switch", { name: "Turn Weekly report on or off" }));

    await waitFor(() =>
      expect(api.setContextCapabilityBinding).toHaveBeenCalledWith("agent-1", {
        scopeType: "scene",
        scopeKey: SALES_SCENE,
        resourceType: "skill",
        resourceId: "skill-report",
        enabled: true,
      }),
    );
  });

  it("shows no page title or agent header, only the tabs", async () => {
    renderPage({ binding: groupBinding });

    await screen.findByRole("region", { name: "Sales team" });
    expect(screen.queryByRole("heading", { level: 1 })).not.toBeInTheDocument();
    expect(screen.queryByText(copy.page_title)).not.toBeInTheDocument();
    expect(screen.queryByText("Helper")).not.toBeInTheDocument();
    expect(screen.getAllByRole("tab").map((tab) => tab.textContent)).toEqual([copy.tab_scope, copy.tab_routines]);
  });

  it("asks the platform to sign in when the session is rejected", async () => {
    api.listContextConfigAgents.mockRejectedValue(new ApiError("unauthorized", 401));
    const { onAuthRequired } = renderPage();

    await waitFor(() => expect(onAuthRequired).toHaveBeenCalledTimes(1));
  });

  it("treats a non-DingTalk session as signed out", async () => {
    api.listContextConfigAgents.mockRejectedValue(
      new ApiError("this endpoint requires DingTalk user authentication", 403),
    );
    const { onAuthRequired } = renderPage();

    await waitFor(() => expect(onAuthRequired).toHaveBeenCalledTimes(1));
  });

  it("does not restart sign-in when a grant is missing", async () => {
    api.getContextConfigAgent.mockRejectedValue(new ApiError("no grant", 403));
    const { onAuthRequired } = renderPage({ initialAgentId: "agent-1" });

    expect(await screen.findByText(copy.no_access_title)).toBeInTheDocument();
    expect(onAuthRequired).not.toHaveBeenCalled();
  });

  it("explains an expired link and still lists existing access", async () => {
    api.redeemContextConfigLink.mockRejectedValue(new ApiError("gone", 410));
    api.listContextConfigAgents.mockResolvedValue([]);
    renderPage({ linkToken: "old" });

    expect(await screen.findByText(copy.link_expired)).toBeInTheDocument();
    expect(await screen.findByText(copy.no_access_title)).toBeInTheDocument();
  });

  it("explains a personal link whose scope already belongs to another account", async () => {
    api.redeemContextConfigLink.mockRejectedValue(new ApiError("conflict", 409));
    api.listContextConfigAgents.mockResolvedValue([]);
    renderPage({ linkToken: "forwarded" });

    expect(await screen.findByText(copy.link_taken)).toBeInTheDocument();
    expect(screen.queryByText(copy.link_failed)).not.toBeInTheDocument();
  });

  it("lays a level out as 指令, Skills, then 连接器和插件", async () => {
    api.getContextConfigScene.mockResolvedValue({ ...sceneDetail, rights: allRights, prompts: [toneprompt] });
    api.getContextConfigAgent.mockResolvedValue(
      agentDetail({
        global: {
          connectors: [{ id: "conn-global", name: "Docs", catalogSlug: "" }],
          skills: [{ id: "skill-own", name: "Own skill", description: "" }],
        },
      }),
    );
    renderPage({ binding: groupBinding });

    const region = await screen.findByRole("region", { name: "Sales team" });
    const headings = within(region)
      .getAllByRole("heading", { level: 3 })
      .map((heading) => heading.textContent);
    expect(headings.slice(0, 3)).toEqual([copy.prompts_title, copy.skills_title, copy.slot_connectors]);
    // The agent's own skill is on by default; an offered one is added here.
    const own = within(region).getByText("Own skill").closest("li") as HTMLElement;
    expect(within(own).getByText(copy.always_on)).toBeInTheDocument();
    expect(within(own).queryByRole("switch")).not.toBeInTheDocument();
    expect(within(region).getByRole("switch", { name: "Turn Weekly report on or off" })).toBeInTheDocument();
    // Which bundle an item comes from is never named.
    expect(screen.queryByText(/bundle/i)).not.toBeInTheDocument();
  });

  it("lists GitHub, Slack and Notion first, then the other apps, connectors and apps not opened", async () => {
    api.getContextConfigAgent.mockResolvedValue(
      agentDetail({
        apps: [
          { slug: "linear", name: "Linear", setup: "automatic" as const, ready: true },
          { slug: "notion", name: "Notion", setup: "automatic" as const, ready: true },
          { slug: "figma", name: "Figma", setup: "automatic" as const, ready: true },
          { slug: "github", name: "GitHub", setup: "automatic" as const, ready: true },
          { slug: "slack", name: "Slack", setup: "automatic" as const, ready: true },
        ],
        offers: {
          connectors: [
            ...agentDetail().offers.connectors,
            { ...githubConnector, id: "conn-figma", name: "Figma", catalogSlug: "figma", acceptsPat: false, installUrl: "" },
            { ...githubConnector, id: "conn-notion", name: "Notion", catalogSlug: "notion", acceptsPat: false, installUrl: "" },
          ],
          skills: [],
        },
      }),
    );
    const user = userEvent.setup();
    renderPage({ binding: groupBinding });

    const region = await screen.findByRole("region", { name: "Sales team" });
    // One row each, apps nobody opened for the agent included.
    const rows = within(within(region).getByRole("list", { name: copy.slot_connectors })).getAllByRole("listitem");
    expect(rows.map((row) => row.querySelector("button")?.getAttribute("aria-label")?.split(" · ")[0])).toEqual(
      ["GitHub", "Slack", "Notion", "Figma", "Wiki", "Docs", "Linear"].map(tileName),
    );
    // The agent's own connector is on by default.
    const docs = tile(region, "Docs");
    expect(within(docs).getByText(copy.always_on)).toBeInTheDocument();
    // An app nobody opened for the agent is added right here.
    expect(within(region).getByRole("button", { name: copy.app_add_aria.replace("{{name}}", "GitHub") })).toBeEnabled();
    const dialog = await openConnector(user, region, "GitHub");
    expect(within(dialog).getByText(copy.app_new_note.replace("{{name}}", "GitHub"))).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: copy.app_add })).toBeEnabled();
  });

  it("saves a personal credential write-only and clears the input", async () => {
    api.getContextConfigAgent.mockResolvedValue(personDetail({ bindings: [connectorOn("conn-wiki")] }));
    api.setContextConnectorCredential.mockResolvedValue({
      connectorId: "conn-wiki",
      hint: "••••cret",
      updatedAt: "",
    });
    const user = userEvent.setup();
    renderPage({ initialAgentId: "agent-1" });

    await user.click(await screen.findByRole("tab", { name: copy.level_person }));
    const region = await screen.findByRole("region", { name: "Alice" });
    const wikiTile = tile(region, "Wiki");
    expect(within(wikiTile).getByText(copy.status_unauthorized)).toBeInTheDocument();
    const dialog = await openConnector(user, region, "Wiki");
    expect(within(dialog).getByText(copy.credential_required)).toBeInTheDocument();

    await user.click(within(dialog).getByRole("button", { name: copy.set_credential }));
    const input = within(dialog).getByLabelText("Bearer token for Wiki");
    expect(input).toHaveAttribute("type", "password");
    await user.type(input, "  super-secret  ");
    await user.click(within(dialog).getByRole("button", { name: copy.save }));

    await waitFor(() =>
      expect(api.setContextConnectorCredential).toHaveBeenCalledWith("agent-1", {
        scopeType: "person",
        scopeKey: "staff-1",
        connectorId: "conn-wiki",
        bearer: "super-secret",
      }),
    );
    await waitFor(() =>
      expect(within(dialog).queryByLabelText("Bearer token for Wiki")).not.toBeInTheDocument(),
    );
    expect(screen.queryByDisplayValue(/super-secret/)).not.toBeInTheDocument();
  });

  it("adds a connector first, then authorizes it", async () => {
    const notAdded = { ...sceneDetail, bindings: [], credentials: [], rights: allRights };
    api.getContextConfigScene
      .mockResolvedValueOnce(notAdded)
      .mockResolvedValue({ ...notAdded, bindings: [connectorOn("conn-wiki")] });
    const user = userEvent.setup();
    renderPage({ binding: groupBinding });

    const region = await screen.findByRole("region", { name: "Sales team" });
    const wikiTile = tile(region, "Wiki");
    // Not added yet: its button says 添加, and no 未授权 until it is added.
    expect(within(region).getByRole("button", { name: copy.app_add_aria.replace("{{name}}", "Wiki") })).toBeInTheDocument();
    expect(within(wikiTile).queryByText(copy.status_unauthorized)).not.toBeInTheDocument();
    const dialog = await openConnector(user, region, "Wiki");
    expect(within(dialog).getByText(copy.auth_after_add)).toBeInTheDocument();
    expect(within(dialog).queryByRole("button", { name: copy.set_credential })).not.toBeInTheDocument();

    await user.click(within(dialog).getByRole("button", { name: copy.app_add }));
    await waitFor(() =>
      expect(api.setContextCapabilityBinding).toHaveBeenCalledWith("agent-1", {
        scopeType: "scene",
        scopeKey: SALES_SCENE,
        resourceType: "connector",
        resourceId: "conn-wiki",
        enabled: true,
      }),
    );
    expect(await within(dialog).findByText(copy.added_scene)).toBeInTheDocument();
    expect(within(dialog).getByText(copy.credential_required)).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: copy.set_credential })).toBeInTheDocument();
    expect(within(wikiTile).getByText(copy.status_unauthorized)).toBeInTheDocument();

    await user.click(within(dialog).getByRole("button", { name: copy.app_remove }));
    await waitFor(() =>
      expect(api.setContextCapabilityBinding).toHaveBeenLastCalledWith("agent-1", {
        scopeType: "scene",
        scopeKey: SALES_SCENE,
        resourceType: "connector",
        resourceId: "conn-wiki",
        enabled: false,
      }),
    );
  });

  it("keeps a stored account manageable after the connector is removed here", async () => {
    api.getContextConfigAgent.mockResolvedValue(
      oauthDetail({
        person: {
          ...agentDetail().person!,
          rights: allRights,
          bindings: [],
          credentials: [{ connectorId: "conn-github", hint: "@octocat", updatedAt: "", kind: "oauth" }],
        },
      }),
    );
    api.deleteContextConnectorCredential.mockResolvedValue(undefined);
    const user = userEvent.setup();
    renderPage({ binding: personBinding, openAuthorizeUrl: vi.fn() });

    const region = await screen.findByRole("region", { name: "Alice" });
    const dialog = await openConnector(user, region, "GitHub");
    expect(within(dialog).getByRole("button", { name: copy.app_add })).toBeInTheDocument();
    expect(within(dialog).queryByText(copy.auth_after_add)).not.toBeInTheDocument();
    expect(within(dialog).getByText(copy.connected_as.replace("{{account}}", "@octocat"))).toBeInTheDocument();
    // Nothing new can be connected until it is added, but the stored
    // account can be revoked.
    expect(within(dialog).queryByRole("button", { name: copy.connect })).not.toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: copy.disconnect }));
    await user.click(within(dialog).getByRole("button", { name: copy.confirm_disconnect }));
    await waitFor(() =>
      expect(api.deleteContextConnectorCredential).toHaveBeenCalledWith("agent-1", {
        scopeType: "person",
        scopeKey: "staff-1",
        connectorId: "conn-github",
      }),
    );
  });

  it("names a tile's state in its accessible name", async () => {
    renderPage({ binding: groupBinding });

    const region = await screen.findByRole("region", { name: "Sales team" });
    expect(tile(region, "Wiki")).toHaveAccessibleName(
      `${tileName("Wiki")} · ${copy.status_added} · ${copy.connected}`,
    );
  });

  it("adds an app nobody opened for the agent from its row, then opens its settings", async () => {
    const notionId = "conn-notion";
    const notion = { ...githubConnector, id: notionId, name: "Notion", catalogSlug: "notion", acceptsPat: false, installUrl: "" };
    // Before: Notion is only in the catalog; after the add, offered and on.
    api.getContextConfigAgent
      .mockResolvedValueOnce(agentDetail())
      .mockResolvedValue(agentDetail({ offers: { connectors: [...agentDetail().offers.connectors, notion], skills: [] } }));
    api.getContextConfigScene
      .mockResolvedValueOnce({ ...sceneDetail, rights: allRights })
      .mockResolvedValue({ ...sceneDetail, rights: allRights, bindings: [...sceneDetail.bindings, connectorOn(notionId)] });
    api.addContextConfigApp.mockResolvedValue({ connectorId: notionId, defaultOn: false });
    const user = userEvent.setup();
    renderPage({ binding: groupBinding, openAuthorizeUrl: vi.fn() });

    const region = await screen.findByRole("region", { name: "Sales team" });
    await user.click(within(region).getByRole("button", { name: copy.app_add_aria.replace("{{name}}", "Notion") }));
    await waitFor(() =>
      expect(api.addContextConfigApp).toHaveBeenCalledWith("agent-1", {
        slug: "notion",
        scopeType: "scene",
        scopeKey: SALES_SCENE,
      }),
    );
    // Its settings open on the added connector, ready to authorize.
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByRole("heading", { name: "Notion" })).toBeInTheDocument();
    expect(await within(dialog).findByRole("button", { name: copy.connect })).toBeInTheDocument();
    // Behind the dialog, its row now offers 配置.
    expect(
      within(region).getByRole("button", { name: copy.app_configure_aria.replace("{{name}}", "Notion"), hidden: true }),
    ).toBeInTheDocument();
  });

  const slackConnector = {
    ...githubConnector,
    id: "conn-slack",
    name: "Slack",
    catalogSlug: "slack",
    acceptsPat: false,
    oauthAvailable: false,
    installUrl: "",
  };
  const slackApps = [{ slug: "slack", name: "Slack", setup: "oauth_app" as const, ready: false }];
  const sceneOAuthApp = {
    slug: "slack",
    name: "Slack",
    fields: [
      { key: "client_id", optional: false, file: false },
      { key: "client_secret", optional: false, file: false },
    ],
    docsUrl: "https://api.slack.com/apps",
    callbackUrl: "https://fde-workbench.dingtalk.com/api/connectors/oauth/callback",
    saved: false,
    clientId: "",
    clientSecretSet: false,
    workspaceReady: false,
    ready: false,
    canEdit: true,
  };
  const sceneScope = { scopeType: "scene", scopeKey: SALES_SCENE };

  it("lets a member fill in the chat's own OAuth application here, with the callback URL to register", async () => {
    // A link holder, not a manager.
    api.getContextConfigAgent.mockResolvedValue(agentDetail({ apps: slackApps, offers: { connectors: [slackConnector], skills: [] } }));
    api.getContextConfigScene.mockResolvedValue({ ...sceneDetail, rights: allRights, bindings: [connectorOn("conn-slack")], credentials: [] });
    api.getContextConfigOAuthApp.mockResolvedValue(sceneOAuthApp);
    api.setContextConfigOAuthApp.mockResolvedValue({ ...sceneOAuthApp, saved: true, ready: true, clientId: "cid", clientSecretSet: true, canEdit: false });
    const user = userEvent.setup();
    renderPage({ binding: groupBinding, openAuthorizeUrl: vi.fn() });

    const region = await screen.findByRole("region", { name: "Sales team" });
    await user.click(within(region).getByRole("button", { name: copy.app_configure_aria.replace("{{name}}", "Slack") }));
    const dialog = await screen.findByRole("dialog");
    // No sign-in until an OAuth application exists; this chat's is filled in here.
    expect(within(dialog).queryByRole("button", { name: copy.connect })).not.toBeInTheDocument();
    expect(await within(dialog).findByText(sceneOAuthApp.callbackUrl)).toBeInTheDocument();
    expect(api.getContextConfigOAuthApp).toHaveBeenCalledWith("agent-1", "slack", sceneScope);
    expect(within(dialog).getByRole("link", { name: copy.oauth_app_console.replace("{{name}}", "Slack") })).toHaveAttribute(
      "href",
      sceneOAuthApp.docsUrl,
    );
    await user.click(within(dialog).getByRole("button", { name: copy.save }));
    expect(within(dialog).getByRole("alert")).toHaveTextContent("Client ID");
    expect(api.setContextConfigOAuthApp).not.toHaveBeenCalled();

    await user.type(within(dialog).getByLabelText("Client ID"), "cid");
    await user.type(within(dialog).getByLabelText("Client Secret"), "csecret");
    await user.click(within(dialog).getByRole("button", { name: copy.save }));
    await waitFor(() =>
      expect(api.setContextConfigOAuthApp).toHaveBeenCalledWith("agent-1", "slack", sceneScope, { clientId: "cid", clientSecret: "csecret" }),
    );
    expect(screen.queryByDisplayValue("csecret")).not.toBeInTheDocument();
  });

  it("signs in with the chat's own OAuth application, which only the agent's managers change", async () => {
    api.getContextConfigAgent.mockResolvedValue(agentDetail({ apps: slackApps, offers: { connectors: [slackConnector], skills: [] } }));
    api.getContextConfigScene.mockResolvedValue({
      ...sceneDetail,
      rights: allRights,
      bindings: [connectorOn("conn-slack")],
      credentials: [],
      sceneOAuthApps: ["slack"],
    });
    api.getContextConfigOAuthApp.mockResolvedValue({ ...sceneOAuthApp, saved: true, ready: true, clientId: "cid", clientSecretSet: true, canEdit: false });
    const user = userEvent.setup();
    renderPage({ binding: groupBinding, openAuthorizeUrl: vi.fn() });

    const region = await screen.findByRole("region", { name: "Sales team" });
    const dialog = await openConnector(user, region, "Slack");
    // The workspace has none, yet this chat signs in with its own.
    expect(within(dialog).getByRole("button", { name: copy.connect })).toBeEnabled();
    expect(await within(dialog).findByText(copy.oauth_app_scene_locked.replace("{{name}}", "Slack"))).toBeInTheDocument();
    expect(within(dialog).queryByRole("button", { name: copy.action_edit })).not.toBeInTheDocument();
    expect(within(dialog).queryByLabelText("Client ID")).not.toBeInTheDocument();
  });

  it("lets a manager change the chat's OAuth application, with a new secret for a new client, or remove it", async () => {
    api.getContextConfigAgent.mockResolvedValue(
      agentDetail({ access: "manager", apps: slackApps, offers: { connectors: [slackConnector], skills: [] } }),
    );
    api.getContextConfigScene.mockResolvedValue({
      ...sceneDetail,
      rights: allRights,
      bindings: [connectorOn("conn-slack")],
      credentials: [],
      sceneOAuthApps: ["slack"],
    });
    api.getContextConfigOAuthApp.mockResolvedValue({ ...sceneOAuthApp, saved: true, ready: true, clientId: "cid", clientSecretSet: true });
    api.deleteContextConfigOAuthApp.mockResolvedValue(undefined);
    const user = userEvent.setup();
    renderPage({ binding: groupBinding, openAuthorizeUrl: vi.fn() });

    const region = await screen.findByRole("region", { name: "Sales team" });
    const dialog = await openConnector(user, region, "Slack");
    expect(await within(dialog).findByText(copy.oauth_app_scene_ready.replace("{{name}}", "Slack"))).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: copy.action_edit }));
    const clientId = within(dialog).getByLabelText("Client ID");
    expect(clientId).toHaveValue("cid");
    await user.clear(clientId);
    await user.type(clientId, "cid-2");
    await user.click(within(dialog).getByRole("button", { name: copy.save }));
    // The stored secret belongs to the old client.
    expect(within(dialog).getByRole("alert")).toHaveTextContent("Client Secret");
    expect(api.setContextConfigOAuthApp).not.toHaveBeenCalled();

    await user.click(within(dialog).getByRole("button", { name: copy.oauth_app_remove }));
    const confirm = await screen.findByRole("alertdialog");
    await user.click(within(confirm).getByRole("button", { name: copy.oauth_app_remove }));
    await waitFor(() => expect(api.deleteContextConfigOAuthApp).toHaveBeenCalledWith("agent-1", "slack", sceneScope));
  });

  it("uses the workspace's OAuth application and offers one of the chat's own", async () => {
    const ready = { ...slackConnector, oauthAvailable: true };
    api.getContextConfigAgent.mockResolvedValue(
      agentDetail({ apps: [{ ...slackApps[0]!, ready: true }], offers: { connectors: [ready], skills: [] } }),
    );
    api.getContextConfigScene.mockResolvedValue({ ...sceneDetail, rights: allRights, bindings: [connectorOn("conn-slack")], credentials: [] });
    api.getContextConfigOAuthApp.mockResolvedValue({ ...sceneOAuthApp, workspaceReady: true, ready: true });
    const user = userEvent.setup();
    renderPage({ binding: groupBinding, openAuthorizeUrl: vi.fn() });

    const region = await screen.findByRole("region", { name: "Sales team" });
    const dialog = await openConnector(user, region, "Slack");
    expect(await within(dialog).findByText(copy.oauth_app_workspace.replace("{{name}}", "Slack"))).toBeInTheDocument();
    expect(within(dialog).queryByLabelText("Client ID")).not.toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: copy.oauth_app_own }));
    expect(within(dialog).getByLabelText("Client ID")).toBeInTheDocument();
  });

  it("keeps OAuth applications off the personal level", async () => {
    const base = personDetail({ bindings: [connectorOn("conn-slack")], credentials: [], rights: allRights });
    api.getContextConfigAgent.mockResolvedValue({ ...base, apps: slackApps, offers: { connectors: [slackConnector], skills: [] } });
    const user = userEvent.setup();
    renderPage({ binding: personBinding, openAuthorizeUrl: vi.fn() });

    const region = await screen.findByRole("region", { name: "Alice" });
    const dialog = await openConnector(user, region, "Slack");
    expect(within(dialog).getByText(copy.connect_unavailable.replace("{{name}}", "Slack"))).toBeInTheDocument();
    expect(within(dialog).queryByLabelText("Client ID")).not.toBeInTheDocument();
    expect(api.getContextConfigOAuthApp).not.toHaveBeenCalled();
  });

  it("says when an admin switched an app off instead of adding it silently", async () => {
    const { toast } = await import("sonner");
    api.getContextConfigScene.mockResolvedValue({ ...sceneDetail, rights: allRights });
    api.addContextConfigApp.mockRejectedValue(new ApiError("off", 409, "", { code: "app_disabled" }));
    const user = userEvent.setup();
    renderPage({ binding: groupBinding, openAuthorizeUrl: vi.fn() });

    const region = await screen.findByRole("region", { name: "Sales team" });
    await user.click(within(region).getByRole("button", { name: copy.app_add_aria.replace("{{name}}", "Notion") }));
    await waitFor(() => expect(toast.error).toHaveBeenCalledWith(copy.app_disabled.replace("{{name}}", "Notion")));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("confirms before removing a group credential", async () => {
    api.deleteContextConnectorCredential.mockResolvedValue(undefined);
    const user = userEvent.setup();
    renderPage({ initialAgentId: "agent-1" });

    const region = await screen.findByRole("region", { name: "Sales team" });
    const dialog = await openConnector(user, region, "Wiki");
    await user.click(within(dialog).getByRole("button", { name: copy.remove_credential }));
    expect(api.deleteContextConnectorCredential).not.toHaveBeenCalled();
    await user.click(within(dialog).getByRole("button", { name: copy.confirm_remove }));

    await waitFor(() =>
      expect(api.deleteContextConnectorCredential).toHaveBeenCalledWith("agent-1", {
        scopeType: "scene",
        scopeKey: SALES_SCENE,
        connectorId: "conn-wiki",
      }),
    );
  });

  it("explains how to get a personal link when none is granted", async () => {
    api.getContextConfigAgent.mockResolvedValue(agentDetail({ person: null }));
    const user = userEvent.setup();
    renderPage({ initialAgentId: "agent-1" });

    await user.click(await screen.findByRole("tab", { name: copy.level_person }));

    expect(screen.getByText(copy.person_empty_title)).toBeInTheDocument();
    expect(screen.getByText(copy.person_empty_hint)).toBeInTheDocument();
  });

  it("links a group through the DingTalk picker when a personal grant exists", async () => {
    api.getContextConfigAgent.mockResolvedValue(agentDetail({ scenes: [], jsapiAvailable: true }));
    api.resolveContextConfigScene.mockResolvedValue({
      scopeKey: OPS_SCENE,
      scopeTitle: "Ops",
      source: "jsapi",
      expiresAt: "",
    });
    const pickGroup = vi.fn().mockResolvedValue({ chatId: "chat-9" });
    const user = userEvent.setup();
    renderPage({ initialAgentId: "agent-1", pickGroup });

    // With only a personal grant the page opens on 个人能力.
    expect(await screen.findByRole("tab", { name: copy.level_person })).toHaveAttribute("aria-selected", "true");
    await user.click(screen.getByRole("tab", { name: copy.level_scene }));
    expect(screen.getByText(copy.scene_empty_title)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: copy.pick_group }));

    await waitFor(() =>
      expect(api.resolveContextConfigScene).toHaveBeenCalledWith("agent-1", { chatId: "chat-9" }),
    );
  });

  it("links a picked group in the page's tenant", async () => {
    const beta = { orgId: "ding2", name: "Beta", source: "created" };
    api.getContextConfigAgent.mockResolvedValue(
      agentDetail({ scenes: [], jsapiAvailable: true, tenant: beta, tenants: [beta] }),
    );
    api.resolveContextConfigScene.mockResolvedValue({
      scopeKey: OPS_SCENE,
      scopeTitle: "Ops",
      source: "jsapi",
      expiresAt: "",
      kind: "group",
      orgId: "ding2",
    });
    const pickGroup = vi.fn().mockResolvedValue({ chatId: "chat-9" });
    const user = userEvent.setup();
    renderPage({ initialAgentId: "agent-1", pickGroup });

    await user.click(await screen.findByRole("tab", { name: copy.level_scene }));
    await user.click(screen.getByRole("button", { name: copy.pick_group }));

    await waitFor(() =>
      expect(api.resolveContextConfigScene).toHaveBeenCalledWith("agent-1", { chatId: "chat-9", orgId: "ding2" }),
    );
  });

  it("needs a picked chatId; a bare openConversationId proves nothing", async () => {
    api.getContextConfigAgent.mockResolvedValue(agentDetail({ scenes: [], jsapiAvailable: true }));
    const pickGroup = vi.fn().mockResolvedValue({ openConversationId: "cid-guess" });
    const user = userEvent.setup();
    renderPage({ initialAgentId: "agent-1", pickGroup });

    await user.click(await screen.findByRole("tab", { name: copy.level_scene }));
    await user.click(screen.getByRole("button", { name: copy.pick_group }));

    await waitFor(() => expect(pickGroup).toHaveBeenCalledTimes(1));
    expect(api.resolveContextConfigScene).not.toHaveBeenCalled();
  });

  it("asks for a reload when the picker cannot sign again on this page", async () => {
    const { toast } = await import("sonner");
    api.getContextConfigAgent.mockResolvedValue(agentDetail({ scenes: [], jsapiAvailable: true }));
    const reload = Object.assign(new Error("DingTalk rejected the JSAPI signature"), { reloadRequired: true });
    const pickGroup = vi.fn().mockRejectedValue(reload);
    const user = userEvent.setup();
    renderPage({ initialAgentId: "agent-1", pickGroup });

    await user.click(await screen.findByRole("tab", { name: copy.level_scene }));
    await user.click(screen.getByRole("button", { name: copy.pick_group }));

    await waitFor(() => expect(toast.error).toHaveBeenCalledWith(copy.pick_group_reload));
  });

  it("keeps each row pending until its own write settles", async () => {
    api.getContextConfigAgent.mockResolvedValue(
      agentDetail({
        offers: {
          connectors: agentDetail().offers.connectors,
          skills: [
            { id: "skill-report", name: "Weekly report", description: "" },
            { id: "skill-digest", name: "Digest", description: "" },
          ],
        },
      }),
    );
    const pending = new Map<string, () => void>();
    api.setContextCapabilityBinding.mockImplementation(
      (_agentId: string, input: { resourceType: string; resourceId: string; enabled: boolean }) =>
        new Promise((resolve) => {
          pending.set(input.resourceId, () =>
            resolve({ resourceType: input.resourceType, resourceId: input.resourceId, enabled: input.enabled }),
          );
        }),
    );
    const user = userEvent.setup();
    renderPage({ initialAgentId: "agent-1" });

    await screen.findByRole("region", { name: "Sales team" });
    await user.click(screen.getByRole("switch", { name: "Turn Weekly report on or off" }));
    await user.click(screen.getByRole("switch", { name: "Turn Digest on or off" }));
    await waitFor(() => expect(pending.size).toBe(2));
    expect(screen.queryByRole("switch", { name: "Turn Weekly report on or off" })).not.toBeInTheDocument();
    expect(screen.queryByRole("switch", { name: "Turn Digest on or off" })).not.toBeInTheDocument();

    pending.get("skill-digest")!();
    await waitFor(() =>
      expect(screen.getByRole("switch", { name: "Turn Digest on or off" })).toBeInTheDocument(),
    );
    // The other write is still in flight, so its row stays busy.
    expect(screen.queryByRole("switch", { name: "Turn Weekly report on or off" })).not.toBeInTheDocument();

    pending.get("skill-report")!();
    await waitFor(() =>
      expect(screen.getByRole("switch", { name: "Turn Weekly report on or off" })).toBeInTheDocument(),
    );
    expect(api.setContextCapabilityBinding).toHaveBeenCalledTimes(2);
  });

  it("offers switching when the preselected agent has no grant but another one does", async () => {
    api.listContextConfigAgents.mockResolvedValue([{ ...agentSummary, id: "agent-2", name: "Planner" }]);
    api.getContextConfigAgent.mockImplementation(async (agentId: string) => {
      if (agentId === "agent-1") throw new ApiError("no grant", 403);
      return agentDetail({ agent: { id: "agent-2", name: "Planner", avatarUrl: null, workspaceId: "ws-1" } });
    });
    const user = userEvent.setup();
    renderPage({ initialAgentId: "agent-1" });

    expect(await screen.findByText(copy.no_access_title)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: copy.switch_agent }));

    await waitFor(() => expect(api.getContextConfigAgent).toHaveBeenCalledWith("agent-2", ""));
  });

  it("hides the DingTalk picker outside the DingTalk client", async () => {
    api.getContextConfigAgent.mockResolvedValue(agentDetail({ scenes: [], jsapiAvailable: true }));
    const user = userEvent.setup();
    renderPage({ initialAgentId: "agent-1" });

    await user.click(await screen.findByRole("tab", { name: copy.level_scene }));
    expect(screen.getByText(copy.scene_empty_title)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: copy.pick_group })).not.toBeInTheDocument();
  });

  describe("official app (OAuth) connectors", () => {
    const person = agentDetail().person!;
    const githubOn = { ...sceneDetail, bindings: [connectorOn("conn-github")], credentials: [] };

    beforeEach(() => {
      api.getContextConfigScene.mockResolvedValue(githubOn);
    });

    it("connects through the provider sign-in for the open scope", async () => {
      api.getContextConfigAgent.mockResolvedValue(oauthDetail());
      api.startContextConnectorConnection.mockResolvedValue(
        "https://github.com/login/oauth/authorize?state=mcpc.x",
      );
      const openAuthorizeUrl = vi.fn();
      const user = userEvent.setup();
      renderPage({ initialAgentId: "agent-1", openAuthorizeUrl });

      const region = await screen.findByRole("region", { name: "Sales team" });
      expect(region.querySelector('[data-connector-mark="github"]')).not.toBeNull();
      const githubTile = tile(region, "GitHub");
      expect(within(githubTile).getByText(copy.status_unauthorized)).toBeInTheDocument();
      const dialog = await openConnector(user, region, "GitHub");
      expect(within(dialog).getByText(copy.connect_required)).toBeInTheDocument();
      // OAuth connectors never show the raw Bearer input.
      expect(within(dialog).queryByRole("button", { name: copy.set_credential })).not.toBeInTheDocument();
      expect(within(dialog).getByRole("link", { name: copy.install_link })).toHaveAttribute(
        "href",
        githubConnector.installUrl,
      );
      expect(api.listContextGitHubInstallations).not.toHaveBeenCalled();
      expect(within(dialog).getByText("get_me")).toBeInTheDocument();

      await user.click(within(dialog).getByRole("button", { name: copy.connect }));

      await waitFor(() =>
        expect(openAuthorizeUrl).toHaveBeenCalledWith(
          "https://github.com/login/oauth/authorize?state=mcpc.x",
          { agentId: "agent-1", scopeType: "scene", scopeKey: SALES_SCENE },
        ),
      );
      expect(api.startContextConnectorConnection).toHaveBeenCalledWith("agent-1", {
        scopeType: "scene",
        scopeKey: SALES_SCENE,
        connectorId: "conn-github",
      });
      // The page is leaving for the provider; a second tap cannot start another flow.
      expect(within(dialog).getByRole("button", { name: copy.connecting })).toBeDisabled();
    });

    it("does not navigate when the server returns no usable authorization URL", async () => {
      const { toast } = await import("sonner");
      api.getContextConfigAgent.mockResolvedValue(oauthDetail());
      api.startContextConnectorConnection.mockResolvedValue("");
      const openAuthorizeUrl = vi.fn();
      const user = userEvent.setup();
      renderPage({ initialAgentId: "agent-1", openAuthorizeUrl });

      const region = await screen.findByRole("region", { name: "Sales team" });
      const dialog = await openConnector(user, region, "GitHub");
      await user.click(within(dialog).getByRole("button", { name: copy.connect }));

      await waitFor(() => expect(toast.error).toHaveBeenCalledWith(copy.connect_failed));
      expect(openAuthorizeUrl).not.toHaveBeenCalled();
      expect(within(dialog).getByRole("button", { name: copy.connect })).toBeEnabled();
    });

    it("lists GitHub App installations covered by the one authorization", async () => {
      api.getContextConfigAgent.mockResolvedValue(
        oauthDetail({
          person: {
            ...person,
            bindings: [connectorOn("conn-github")],
            credentials: [{ connectorId: "conn-github", hint: "@dingtalk-fde", updatedAt: "", kind: "oauth" }],
          },
        }),
      );
      api.listContextGitHubInstallations.mockResolvedValue({
        connected: true,
        installations: [
          {
            id: 1,
            accountLogin: "dingtalk-fde",
            accountType: "User",
            repositorySelection: "all",
            settingsUrl: "https://github.com/settings/installations/1",
          },
          {
            id: 2,
            accountLogin: "acme",
            accountType: "Organization",
            repositorySelection: "selected",
            settingsUrl: "https://github.com/organizations/acme/settings/installations/2",
          },
        ],
        error: "",
        truncated: false,
      });
      const user = userEvent.setup();
      renderPage({ initialAgentId: "agent-1", openAuthorizeUrl: vi.fn() });

      await user.click(await screen.findByRole("tab", { name: copy.level_person }));
      const region = await screen.findByRole("region", { name: "Alice" });
      const dialog = await openConnector(user, region, "GitHub");

      expect(await within(dialog).findByText("@dingtalk-fde")).toBeInTheDocument();
      expect(within(dialog).getByText("@acme")).toBeInTheDocument();
      expect(dialog).toHaveTextContent(copy.install_org);
      expect(dialog).toHaveTextContent(copy.install_selected);
      expect(
        within(dialog)
          .getAllByRole("link", { name: copy.install_settings })
          .map((link) => link.getAttribute("href")),
      ).toContain("https://github.com/organizations/acme/settings/installations/2");
      expect(api.listContextGitHubInstallations).toHaveBeenCalledWith(
        "agent-1",
        { scopeType: "person", scopeKey: "staff-1" },
        "conn-github",
      );
    });

    it("shows the connected account and disconnects only after confirmation", async () => {
      api.getContextConfigAgent.mockResolvedValue(
        oauthDetail({
          person: {
            ...person,
            bindings: [connectorOn("conn-github")],
            credentials: [{ connectorId: "conn-github", hint: "@octocat", updatedAt: "", kind: "oauth" }],
          },
        }),
      );
      api.deleteContextConnectorCredential.mockResolvedValue(undefined);
      const user = userEvent.setup();
      renderPage({ initialAgentId: "agent-1", openAuthorizeUrl: vi.fn() });

      await user.click(await screen.findByRole("tab", { name: copy.level_person }));
      const region = await screen.findByRole("region", { name: "Alice" });
      expect(within(tile(region, "GitHub")).getByText(copy.connected)).toBeInTheDocument();
      const dialog = await openConnector(user, region, "GitHub");
      expect(within(dialog).queryByRole("button", { name: copy.connect })).not.toBeInTheDocument();

      await user.click(within(dialog).getByRole("button", { name: copy.disconnect }));
      expect(api.deleteContextConnectorCredential).not.toHaveBeenCalled();
      await user.click(within(dialog).getByRole("button", { name: copy.confirm_disconnect }));

      await waitFor(() =>
        expect(api.deleteContextConnectorCredential).toHaveBeenCalledWith("agent-1", {
          scopeType: "person",
          scopeKey: "staff-1",
          connectorId: "conn-github",
        }),
      );
    });

    it("never shows a generic OAuth hint as an account name", async () => {
      api.getContextConfigAgent.mockResolvedValue(
        oauthDetail({
          person: {
            ...person,
            bindings: [connectorOn("conn-github")],
            credentials: [{ connectorId: "conn-github", hint: "OAuth", updatedAt: "", kind: "oauth" }],
          },
        }),
      );
      const user = userEvent.setup();
      renderPage({ initialAgentId: "agent-1" });

      await user.click(await screen.findByRole("tab", { name: copy.level_person }));
      const region = await screen.findByRole("region", { name: "Alice" });
      const dialog = await openConnector(user, region, "GitHub");
      expect(within(dialog).getAllByText(copy.connected).length).toBeGreaterThan(0);
      expect(within(dialog).queryByText(/OAuth$/)).not.toBeInTheDocument();
    });

    it("offers a Personal Access Token for GitHub as a secondary option", async () => {
      api.getContextConfigAgent.mockResolvedValue(
        oauthDetail({ person: { ...person, bindings: [connectorOn("conn-github")] } }),
      );
      api.setContextConnectorCredential.mockResolvedValue({
        connectorId: "conn-github",
        hint: "••••1234",
        updatedAt: "",
        kind: "bearer",
      });
      const user = userEvent.setup();
      // Without a platform navigator the provider sign-in is unavailable,
      // but the token path still works.
      renderPage({ initialAgentId: "agent-1" });

      await user.click(await screen.findByRole("tab", { name: copy.level_person }));
      const region = await screen.findByRole("region", { name: "Alice" });
      const dialog = await openConnector(user, region, "GitHub");
      expect(within(dialog).queryByRole("button", { name: copy.connect })).not.toBeInTheDocument();

      await user.click(within(dialog).getByRole("button", { name: copy.pat_connect }));
      const input = within(dialog).getByLabelText("Personal Access Token for GitHub");
      expect(input).toHaveAttribute("type", "password");
      await user.type(input, "ghp_example");
      await user.click(within(dialog).getByRole("button", { name: copy.save }));

      await waitFor(() =>
        expect(api.setContextConnectorCredential).toHaveBeenCalledWith("agent-1", {
          scopeType: "person",
          scopeKey: "staff-1",
          connectorId: "conn-github",
          bearer: "ghp_example",
        }),
      );
      await waitFor(() =>
        expect(within(dialog).queryByLabelText("Personal Access Token for GitHub")).not.toBeInTheDocument(),
      );
    });

    it("hides the token option for OAuth apps that do not accept one", async () => {
      api.getContextConfigAgent.mockResolvedValue(
        oauthDetail({
          offers: {
            connectors: [{ ...githubConnector, id: "conn-notion", name: "Notion", catalogSlug: "notion", acceptsPat: false, installUrl: "" }],
            skills: [],
          },
        }),
      );
      api.getContextConfigScene.mockResolvedValue({ ...githubOn, bindings: [connectorOn("conn-notion")] });
      const user = userEvent.setup();
      renderPage({ initialAgentId: "agent-1", openAuthorizeUrl: vi.fn() });

      const region = await screen.findByRole("region", { name: "Sales team" });
      const dialog = await openConnector(user, region, "Notion");
      expect(within(dialog).getByRole("button", { name: copy.connect })).toBeInTheDocument();
      expect(within(dialog).queryByRole("button", { name: copy.use_pat })).not.toBeInTheDocument();
      expect(within(dialog).queryByText(copy.install_hint)).not.toBeInTheDocument();
    });

    it("reopens the scope a connection started from and reports the outcome", async () => {
      api.getContextConfigAgent.mockResolvedValue(oauthDetail());
      renderPage({
        initialAgentId: "agent-1",
        initialScope: { scopeType: "person", scopeKey: "staff-1" },
        connectResult: { kind: "connected", slug: "github" },
      });

      expect(await screen.findByRole("region", { name: "Alice" })).toBeInTheDocument();
      expect(screen.getByRole("tab", { name: copy.level_person })).toHaveAttribute("aria-selected", "true");
      expect(screen.getByText(copy.returned_connected.replace("{{name}}", "GitHub"))).toBeInTheDocument();
    });

    it("explains a cancelled provider sign-in", async () => {
      api.getContextConfigAgent.mockResolvedValue(oauthDetail());
      renderPage({ initialAgentId: "agent-1", connectResult: { kind: "error", code: "access_denied" } });

      expect(await screen.findByText(copy.returned_denied)).toBeInTheDocument();
    });

    it("explains a sign-in that came back in another browser", async () => {
      api.getContextConfigAgent.mockResolvedValue(oauthDetail());
      renderPage({ initialAgentId: "agent-1", connectResult: { kind: "error", code: "browser_mismatch" } });

      expect(await screen.findByText(copy.returned_browser_mismatch)).toBeInTheDocument();
    });

    it("offers only the token when the server cannot run the app's sign-in", async () => {
      api.getContextConfigAgent.mockResolvedValue(
        oauthDetail({ offers: { connectors: [{ ...githubConnector, oauthAvailable: false }], skills: [] } }),
      );
      const user = userEvent.setup();
      renderPage({ initialAgentId: "agent-1", openAuthorizeUrl: vi.fn() });

      const region = await screen.findByRole("region", { name: "Sales team" });
      const dialog = await openConnector(user, region, "GitHub");
      expect(within(dialog).queryByRole("button", { name: copy.connect })).not.toBeInTheDocument();
      expect(within(dialog).getByRole("button", { name: copy.pat_connect })).toBeInTheDocument();
      expect(
        within(dialog).getByText(copy.connect_unavailable_pat.replace("{{name}}", "GitHub")),
      ).toBeInTheDocument();
    });

    it("reports sign-in errors that a retry cannot fix", async () => {
      const { toast } = await import("sonner");
      api.getContextConfigAgent.mockResolvedValue(oauthDetail());
      api.startContextConnectorConnection
        .mockRejectedValueOnce(
          new ApiError("OAuth is not configured for this app", 503, "", { code: "oauth_unavailable" }),
        )
        .mockRejectedValueOnce(new ApiError("not allowed", 403, "", { code: "forbidden" }));
      const openAuthorizeUrl = vi.fn();
      const user = userEvent.setup();
      renderPage({ initialAgentId: "agent-1", openAuthorizeUrl });

      const region = await screen.findByRole("region", { name: "Sales team" });
      const dialog = await openConnector(user, region, "GitHub");
      await user.click(within(dialog).getByRole("button", { name: copy.connect }));
      await waitFor(() =>
        expect(toast.error).toHaveBeenCalledWith(copy.connect_unavailable_pat.replace("{{name}}", "GitHub")),
      );
      await user.click(within(dialog).getByRole("button", { name: copy.connect }));
      await waitFor(() => expect(toast.error).toHaveBeenCalledWith(copy.connect_forbidden));
      expect(toast.error).not.toHaveBeenCalledWith(copy.connect_failed);
      expect(openAuthorizeUrl).not.toHaveBeenCalled();
    });
  });

  it("labels the chat kind of each scene and explains a 1:1 chat scene", async () => {
    api.getContextConfigAgent.mockResolvedValue(
      agentDetail({
        scenes: [
          { scopeKey: SALES_SCENE, scopeTitle: "Sales team", source: "agent_link", expiresAt: "", kind: "group", orgId: "" },
          { scopeKey: DM_SCENE, scopeTitle: "", source: "agent_link", expiresAt: "", kind: "dm", orgId: "" },
        ],
      }),
    );
    api.getContextConfigScene.mockImplementation(async (_agentId: string, sceneId: string) =>
      sceneId === DM_SCENE
        ? {
            scene: { scopeKey: DM_SCENE, scopeTitle: "", source: "agent_link", expiresAt: "", kind: "dm", orgId: "" },
            bindings: [connectorOn("conn-wiki")],
            credentials: [],
            // A 1:1 chat is its own scene, never its person's scope.
            scope: { type: "scene", key: DM_SCENE, title: "" },
            canConnect: true,
            ...noContent,
          }
        : sceneDetail,
    );
    const user = userEvent.setup();
    renderPage({ initialAgentId: "agent-1" });

    expect(await screen.findByRole("tab", { name: copy.level_scene })).toBeInTheDocument();
    const groupRegion = await screen.findByRole("region", { name: "Sales team" });
    expect(within(groupRegion).getByText(copy.kind_group)).toBeInTheDocument();

    const picker = screen.getByRole("combobox");
    // A chat without a known name is named by its kind alone.
    expect(within(picker).getByRole("option", { name: copy.scene_untitled_dm })).toBeInTheDocument();
    await user.selectOptions(picker, DM_SCENE);

    const dmRegion = await screen.findByRole("region", { name: copy.scene_untitled_dm });
    // The title is the kind, so no second kind badge.
    expect(within(dmRegion).getAllByText(copy.kind_dm)).toHaveLength(1);
    // An older backend without rights that lets the caller connect: the
    // chat's own token is set here.
    const dialog = await openConnector(user, dmRegion, "Wiki");
    expect(within(dialog).getByText(copy.added_dm)).toBeInTheDocument();
    expect(within(dialog).getByText(copy.credential_required)).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: copy.set_credential })).toBeInTheDocument();
    expect(within(dialog).queryByText(copy.owner_connects)).not.toBeInTheDocument();
    expect(api.getContextConfigScene).toHaveBeenCalledWith("agent-1", DM_SCENE, "");
  });

  it("writes a 1:1 chat's switches to the chat's own scene, with the manager's full rights", async () => {
    api.getContextConfigAgent.mockResolvedValue(
      agentDetail({
        access: "manager",
        scenes: [{ scopeKey: DM_SCENE, scopeTitle: "Bob", source: "manager", expiresAt: "", kind: "dm", orgId: "" }],
      }),
    );
    api.getContextConfigScene.mockResolvedValue({
      scene: { scopeKey: DM_SCENE, scopeTitle: "Bob", source: "manager", expiresAt: "", kind: "dm", orgId: "" },
      bindings: [],
      credentials: [],
      scope: { type: "scene", key: DM_SCENE, title: "Bob" },
      canConnect: true,
      ...noContent,
      rights: allRights,
    });
    const user = userEvent.setup();
    renderPage({ initialAgentId: "agent-1" });

    const region = await screen.findByRole("region", { name: "Bob" });
    expect(within(region).getByText(copy.kind_dm)).toBeInTheDocument();
    const dialog = await openConnector(user, region, "Wiki");
    expect(within(dialog).queryByText(copy.owner_connects)).not.toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: copy.app_add }));

    await waitFor(() =>
      expect(api.setContextCapabilityBinding).toHaveBeenCalledWith("agent-1", {
        scopeType: "scene",
        scopeKey: DM_SCENE,
        resourceType: "connector",
        resourceId: "conn-wiki",
        enabled: true,
      }),
    );
  });

  it("saves the group-chat switch of a personal connector and says it is not applied yet", async () => {
    api.getContextConfigAgent.mockResolvedValue(
      personDetail({
        bindings: [
          connectorOn("conn-wiki"),
          { resourceType: "skill", resourceId: "skill-report", enabled: true, shareInGroups: false },
        ],
      }),
    );
    const user = userEvent.setup();
    renderPage({ initialAgentId: "agent-1" });

    await user.click(await screen.findByRole("tab", { name: copy.level_person }));
    const region = await screen.findByRole("region", { name: "Alice" });
    // Skills never get the switch.
    expect(within(region).queryByRole("switch", { name: copy.share_in_groups })).not.toBeInTheDocument();
    // The runtime does not read the switch yet, so the page says so.
    const dialog = await openConnector(user, region, "Wiki");
    const share = within(dialog).getByRole("switch", { name: copy.share_in_groups });
    expect(share).not.toBeChecked();
    expect(within(dialog).getByText(copy.share_in_groups_hint)).toBeInTheDocument();
    expect(within(dialog).getByText(copy.share_in_groups_pending_note)).toBeInTheDocument();
    expect(share).toHaveAccessibleDescription(
      `${copy.share_in_groups_hint} ${copy.share_in_groups_pending_note}`,
    );

    await user.click(share);

    await waitFor(() =>
      expect(api.setContextCapabilityBinding).toHaveBeenCalledWith("agent-1", {
        scopeType: "person",
        scopeKey: "staff-1",
        resourceType: "connector",
        resourceId: "conn-wiki",
        enabled: true,
        shareInGroups: true,
      }),
    );
  });

  it("shows the group-chat switch only for added personal connectors", async () => {
    const user = userEvent.setup();
    renderPage({ initialAgentId: "agent-1" });

    // The scene editor never offers it.
    const sceneRegion = await screen.findByRole("region", { name: "Sales team" });
    let dialog = await openConnector(user, sceneRegion, "Wiki");
    expect(within(dialog).queryByRole("switch", { name: copy.share_in_groups })).not.toBeInTheDocument();
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());

    // A personal connector that is not added has nothing to share.
    await user.click(screen.getByRole("tab", { name: copy.level_person }));
    const region = await screen.findByRole("region", { name: "Alice" });
    dialog = await openConnector(user, region, "Wiki");
    expect(within(dialog).queryByRole("switch", { name: copy.share_in_groups })).not.toBeInTheDocument();
  });

  it("reflects a connector already shared with group chats", async () => {
    api.getContextConfigAgent.mockResolvedValue(personDetail({ bindings: [connectorOn("conn-wiki", true)] }));
    const user = userEvent.setup();
    renderPage({ initialAgentId: "agent-1" });

    await user.click(await screen.findByRole("tab", { name: copy.level_person }));
    const region = await screen.findByRole("region", { name: "Alice" });
    const dialog = await openConnector(user, region, "Wiki");
    expect(within(dialog).getByRole("switch", { name: copy.share_in_groups })).toBeChecked();
  });

  it("uses a centered column that widens on desktop screens", async () => {
    renderPage({ initialAgentId: "agent-1" });
    await screen.findByRole("region", { name: "Sales team" });
    const column = screen.getByRole("main").firstElementChild;
    expect(column?.className).toContain("mx-auto");
    expect(column?.className).toContain("sm:max-w-2xl");
  });

  it("switches levels with one segmented control above the level", async () => {
    renderPage({ initialAgentId: "agent-1" });
    await screen.findByRole("region", { name: "Sales team" });
    const levels = screen.getByRole("tablist", { name: copy.levels_aria });
    expect(levels.closest('[data-orientation="horizontal"]')).not.toBeNull();
    expect(within(levels).getAllByRole("tab").map((tab) => tab.textContent)).toEqual([
      copy.level_scene,
      copy.level_person,
    ]);
    expect(within(levels).getByRole("tab", { name: copy.level_scene })).toHaveAttribute("aria-selected", "true");
  });

  describe("agent managers", () => {
    const managed: ContextConfigAgentSummary = { ...agentSummary, access: "manager" };

    it("opens the admin configure link and configures every scene of a managed agent", async () => {
      api.listContextConfigAgents.mockResolvedValue([managed]);
      api.getContextConfigAgent.mockResolvedValue(
        agentDetail({
          person: null,
          access: "manager",
          scenes: [
            { scopeKey: SALES_SCENE, scopeTitle: "Sales team", source: "manager", expiresAt: "", kind: "group", orgId: "" },
            { scopeKey: DM_SCENE, scopeTitle: "Bob", source: "manager", expiresAt: "", kind: "dm", orgId: "" },
          ],
        }),
      );
      renderPage({ initialAgentId: "agent-1" });

      expect(await screen.findByRole("region", { name: "Sales team" })).toBeInTheDocument();
      expect(screen.queryByText(copy.no_access_title)).not.toBeInTheDocument();
      // No agent header: neither the name nor the manager label.
      expect(screen.queryByText("Helper")).not.toBeInTheDocument();
      expect(screen.queryByText(copy.manager_badge)).not.toBeInTheDocument();
      const picker = screen.getByRole("combobox");
      expect(within(picker).getByRole("option", { name: `${copy.kind_group} · Sales team` })).toBeInTheDocument();
      expect(within(picker).getByRole("option", { name: `${copy.kind_dm} · Bob` })).toBeInTheDocument();
    });

    it("lets a manager connect a 1:1 chat's own account: nothing is left to its person", async () => {
      api.getContextConfigAgent.mockResolvedValue(
        oauthDetail({
          person: null,
          access: "manager",
          scenes: [{ scopeKey: DM_SCENE, scopeTitle: "Bob", source: "manager", expiresAt: "", kind: "dm", orgId: "" }],
        }),
      );
      api.getContextConfigScene.mockResolvedValue({
        scene: { scopeKey: DM_SCENE, scopeTitle: "Bob", source: "manager", expiresAt: "", kind: "dm", orgId: "" },
        bindings: [connectorOn("conn-github")],
        credentials: [{ connectorId: "conn-github", hint: "@bob", updatedAt: "", kind: "oauth" }],
        scope: { type: "scene", key: DM_SCENE, title: "Bob" },
        canConnect: true,
        ...noContent,
        rights: allRights,
      });
      const user = userEvent.setup();
      renderPage({ initialAgentId: "agent-1" });

      const region = await screen.findByRole("region", { name: "Bob" });
      const dialog = await openConnector(user, region, "GitHub");
      expect(within(dialog).queryByText(copy.owner_connects)).not.toBeInTheDocument();
      expect(within(dialog).getAllByText(copy.connected_as.replace("{{account}}", "@bob")).length).toBeGreaterThan(0);
      expect(within(dialog).getByRole("button", { name: copy.disconnect })).toBeInTheDocument();
      expect(within(dialog).getByRole("button", { name: copy.app_remove })).toBeInTheDocument();
    });

    it("hides connecting whenever the server says the caller may not connect", async () => {
      api.getContextConfigAgent.mockResolvedValue(
        oauthDetail({
          scenes: [{ scopeKey: DM_SCENE, scopeTitle: "Bob", source: "agent_link", expiresAt: "", kind: "dm", orgId: "" }],
        }),
      );
      api.getContextConfigScene.mockResolvedValue({
        scene: { scopeKey: DM_SCENE, scopeTitle: "Bob", source: "agent_link", expiresAt: "", kind: "dm", orgId: "" },
        bindings: [connectorOn("conn-github")],
        credentials: [],
        scope: { type: "scene", key: DM_SCENE, title: "Bob" },
        canConnect: false,
      });
      const user = userEvent.setup();
      renderPage({ initialAgentId: "agent-1" });

      const region = await screen.findByRole("region", { name: "Bob" });
      const dialog = await openConnector(user, region, "GitHub");
      expect(within(dialog).getByText(copy.owner_connects)).toBeInTheDocument();
      expect(within(dialog).queryByRole("button", { name: copy.connect })).not.toBeInTheDocument();
    });

    it("takes manager access from the agent detail, even when the agent list fails", async () => {
      api.listContextConfigAgents.mockRejectedValue(new Error("list unavailable"));
      api.getContextConfigAgent.mockResolvedValue(agentDetail({ person: null, scenes: [], access: "manager" }));
      const user = userEvent.setup();
      renderPage({ initialAgentId: "agent-1" });

      expect(await screen.findByText(copy.scene_empty_manager_title)).toBeInTheDocument();
      await user.click(screen.getByRole("tab", { name: copy.level_person }));
      expect(screen.getByText(copy.person_manager_note)).toBeInTheDocument();
    });

    it("does not show manager copy for a grant-only detail", async () => {
      // The list claims manager access, but the detail is authoritative.
      api.listContextConfigAgents.mockResolvedValue([managed]);
      api.getContextConfigAgent.mockResolvedValue(agentDetail({ scenes: [] }));
      const user = userEvent.setup();
      renderPage({ initialAgentId: "agent-1" });

      await user.click(await screen.findByRole("tab", { name: copy.level_scene }));
      expect(screen.getByText(copy.scene_empty_title)).toBeInTheDocument();
      expect(screen.queryByText(copy.scene_empty_manager_title)).not.toBeInTheDocument();
    });

    it("lists managed agents in the picker with the manager label", async () => {
      api.listContextConfigAgents.mockResolvedValue([
        managed,
        { ...agentSummary, id: "agent-2", name: "Planner", scopes: [{ scopeType: "person", scopeKey: "staff-1", scopeTitle: "", source: "agent_link", expiresAt: "" }] },
      ]);
      renderPage();

      const helper = await screen.findByRole("button", { name: /Helper/ });
      expect(within(helper).getByText(copy.manager_badge)).toBeInTheDocument();
      expect(within(helper).getByText(copy.manager_all_scenes)).toBeInTheDocument();
      const planner = screen.getByRole("button", { name: /Planner/ });
      expect(within(planner).queryByText(copy.manager_badge)).not.toBeInTheDocument();
    });

    it("explains an empty scene list and that 个人能力 still needs a personal link", async () => {
      api.listContextConfigAgents.mockResolvedValue([managed]);
      api.getContextConfigAgent.mockResolvedValue(agentDetail({ person: null, scenes: [], access: "manager" }));
      const user = userEvent.setup();
      renderPage({ initialAgentId: "agent-1" });

      expect(await screen.findByText(copy.scene_empty_manager_title)).toBeInTheDocument();
      expect(screen.getByText(copy.scene_empty_manager_hint)).toBeInTheDocument();
      await user.click(screen.getByRole("tab", { name: copy.level_person }));
      expect(screen.getByText(copy.person_empty_hint)).toBeInTheDocument();
      expect(screen.getByText(copy.person_manager_note)).toBeInTheDocument();
    });
  });

  describe("three levels", () => {
    const acme = { orgId: "dingA", name: "Acme", source: "identity" };
    const beta = { orgId: "dingB", name: "Beta", source: "created" };
    const orgLevel = {
      scopeKey: "dingA",
      scopeTitle: "Acme",
      bindings: [
        { resourceType: "skill" as const, resourceId: "skill-report", enabled: true, shareInGroups: false },
        connectorOn("conn-wiki"),
      ],
      credentials: [{ connectorId: "conn-wiki", hint: "", updatedAt: "", kind: "bearer" as const }],
      canEdit: false,
      ...noContent,
    };

    it("shows the enterprise level read-only when it cannot be changed", async () => {
      api.getContextConfigAgent.mockResolvedValue(
        agentDetail({ tenant: acme, tenants: [acme], org: orgLevel }),
      );
      const user = userEvent.setup();
      renderPage({ initialAgentId: "agent-1" });

      await user.click(await screen.findByRole("tab", { name: copy.level_org }));
      const region = await screen.findByRole("region", { name: "Acme" });
      expect(within(region).getByText(copy.org_read_only)).toBeInTheDocument();
      // Shown, not switched: what is on carries a label instead.
      const skill = within(region).getByText("Weekly report").closest("li") as HTMLElement;
      expect(within(skill).getByText(copy.status_added)).toBeInTheDocument();
      expect(within(region).queryByRole("switch")).not.toBeInTheDocument();
      // A stored enterprise token shows, but nothing can be changed.
      const dialog = await openConnector(user, region, "Wiki");
      expect(within(dialog).getByText(copy.credential_set.replace("{{hint}}", "••••"))).toBeInTheDocument();
      expect(within(dialog).queryByRole("button", { name: copy.app_remove })).not.toBeInTheDocument();
      expect(within(dialog).queryByRole("button", { name: copy.replace_credential })).not.toBeInTheDocument();
    });

    it("offers no enterprise level when there is none to show", async () => {
      renderPage({ initialAgentId: "agent-1" });

      await screen.findByRole("region", { name: "Sales team" });
      expect(screen.queryByRole("tab", { name: copy.level_org })).not.toBeInTheDocument();
    });

    it("shows the enterprise level read-only to managers too: it is configured in the admin console", async () => {
      api.getContextConfigAgent.mockResolvedValue(
        agentDetail({
          person: null,
          access: "manager",
          tenant: acme,
          tenants: [acme],
          org: {
            ...orgLevel,
            bindings: [connectorOn("conn-wiki")],
            credentials: [],
            canEdit: true,
            rights: allRights,
            prompts: [toneprompt, { ...toneprompt, id: "p2", name: "Off", enabled: false }],
          },
        }),
      );
      const user = userEvent.setup();
      renderPage({ initialAgentId: "agent-1" });

      await user.click(await screen.findByRole("tab", { name: copy.level_org }));
      const region = await screen.findByRole("region", { name: "Acme" });
      expect(within(region).getByText(copy.org_read_only)).toBeInTheDocument();
      // Only what applies there: the enabled instruction, no skill the
      // enterprise did not turn on, the added connector.
      expect(within(region).getByRole("button", { name: "Tone" })).toBeInTheDocument();
      expect(within(region).queryByRole("button", { name: "Off" })).not.toBeInTheDocument();
      expect(within(region).queryByText("Weekly report")).not.toBeInTheDocument();
      expect(within(region).queryByRole("button", { name: copy.prompt_add })).not.toBeInTheDocument();
      expect(within(region).queryByRole("button", { name: copy.mcp_add })).not.toBeInTheDocument();
      expect(within(region).queryByRole("switch")).not.toBeInTheDocument();
      expect(within(region).queryByRole("button", { name: tileName("GitHub") })).not.toBeInTheDocument();
      const dialog = await openConnector(user, region, "Wiki");
      expect(within(dialog).queryByRole("button", { name: copy.app_remove })).not.toBeInTheDocument();
      expect(within(dialog).queryByRole("button", { name: copy.set_credential })).not.toBeInTheDocument();
      expect(api.setContextCapabilityBinding).not.toHaveBeenCalled();
    });

    it("reopens the enterprise level of a tenant, where nothing is connected from this page", async () => {
      api.getContextConfigAgent.mockResolvedValue(
        agentDetail({
          person: null,
          access: "manager",
          tenant: beta,
          tenants: [acme, beta],
          org: {
            ...orgLevel,
            scopeKey: "dingB",
            scopeTitle: "Beta",
            bindings: [connectorOn("conn-github")],
            credentials: [],
            canEdit: true,
            rights: allRights,
          },
          offers: {
            connectors: [...agentDetail().offers.connectors, githubConnector],
            skills: agentDetail().offers.skills,
          },
        }),
      );
      const user = userEvent.setup();
      renderPage({
        initialAgentId: "agent-1",
        initialScope: { scopeType: "org", scopeKey: "dingB", orgId: "dingB" },
        openAuthorizeUrl: vi.fn(),
      });

      const region = await screen.findByRole("region", { name: "Beta" });
      expect(api.getContextConfigAgent).toHaveBeenCalledWith("agent-1", "dingB");
      expect(screen.getByRole("tab", { name: copy.level_org })).toHaveAttribute("aria-selected", "true");
      const dialog = await openConnector(user, region, "GitHub");
      expect(within(dialog).getByText(copy.connect_required)).toBeInTheDocument();
      expect(within(dialog).queryByRole("button", { name: copy.connect })).not.toBeInTheDocument();
      expect(api.startContextConnectorConnection).not.toHaveBeenCalled();
    });

    it("marks what the enterprise turned on in a chat, for members too", async () => {
      // A member gets the enterprise level read-only.
      api.getContextConfigAgent.mockResolvedValue(
        agentDetail({
          tenant: acme,
          tenants: [acme],
          org: { ...orgLevel, bindings: [...orgLevel.bindings], rights: noRights },
        }),
      );
      api.getContextConfigScene.mockResolvedValue({ ...sceneDetail, bindings: [], credentials: [], rights: allRights });
      const user = userEvent.setup();
      renderPage({ binding: groupBinding });

      const region = await screen.findByRole("region", { name: "Sales team" });
      const skill = within(region).getByText("Weekly report").closest("li");
      expect(skill).not.toBeNull();
      expect(within(skill as HTMLElement).getByText(copy.on_for_org)).toBeInTheDocument();
      // The chat's own switch stays its own.
      expect(within(skill as HTMLElement).getByRole("switch")).not.toBeChecked();
      // A connector the enterprise added and authorized applies here too.
      // The enterprise's account covers it here: authorized, with 配置.
      const wiki = tile(region, "Wiki");
      expect(within(wiki).getByText(copy.connected)).toBeInTheDocument();
      expect(within(wiki).queryByText(copy.status_unauthorized)).not.toBeInTheDocument();
      const dialog = await openConnector(user, region, "Wiki");
      expect(within(dialog).getByText(copy.org_on_note)).toBeInTheDocument();
      expect(within(dialog).queryByRole("button", { name: copy.app_add })).not.toBeInTheDocument();
      // The enterprise account serves here; the chat may bring its own.
      expect(within(dialog).getByText(copy.account_from_org)).toBeInTheDocument();
      expect(within(dialog).queryByText(copy.credential_required)).not.toBeInTheDocument();
      expect(within(dialog).getByRole("button", { name: copy.set_credential })).toBeInTheDocument();
    });

    it("switches the tenant and sends it with every request there", async () => {
      api.getContextConfigAgent.mockImplementation(async (_agentId: string, orgId = "") =>
        orgId === "dingB"
          ? agentDetail({
              tenant: beta,
              tenants: [acme, beta],
              scenes: [
                { scopeKey: PARTNER_SCENE, scopeTitle: "Partner", source: "agent_link", expiresAt: "", kind: "group", orgId: "dingB" },
              ],
            })
          : agentDetail({ tenant: acme, tenants: [acme, beta] }),
      );
      api.getContextConfigScene.mockImplementation(async (_agentId: string, sceneKey: string) =>
        sceneKey === PARTNER_SCENE
          ? {
              ...sceneDetail,
              scene: { scopeKey: PARTNER_SCENE, scopeTitle: "Partner", source: "agent_link", expiresAt: "", kind: "group", orgId: "dingB" },
              scope: { type: "scene", key: PARTNER_SCENE, title: "Partner" },
              bindings: [],
            }
          : sceneDetail,
      );
      const user = userEvent.setup();
      renderPage({ initialAgentId: "agent-1" });

      await screen.findByRole("region", { name: "Sales team" });
      expect(api.getContextConfigAgent).toHaveBeenCalledWith("agent-1", "");
      const picker = screen.getByRole("combobox", { name: copy.tab_org });
      expect(picker).toHaveValue("dingA");
      await user.selectOptions(picker, "dingB");

      const region = await screen.findByRole("region", { name: "Partner" });
      expect(api.getContextConfigAgent).toHaveBeenCalledWith("agent-1", "dingB");
      expect(api.getContextConfigScene).toHaveBeenCalledWith("agent-1", PARTNER_SCENE, "dingB");
      await user.click(within(region).getByRole("switch", { name: "Turn Weekly report on or off" }));
      await waitFor(() =>
        expect(api.setContextCapabilityBinding).toHaveBeenCalledWith("agent-1", {
          scopeType: "scene",
          scopeKey: PARTNER_SCENE,
          orgId: "dingB",
          resourceType: "skill",
          resourceId: "skill-report",
          enabled: true,
        }),
      );
    });

    it("falls back to the server's tenant when the link's tenant is gone", async () => {
      api.redeemContextConfigLink.mockResolvedValue({
        agentId: "agent-1",
        workspaceId: "ws-1",
        scopeType: "scene",
        scopeKey: SALES_SCENE,
        scopeTitle: "Sales team",
        orgId: "dingGone",
      });
      api.getContextConfigAgent.mockImplementation(async (_agentId: string, orgId = "") => {
        if (orgId === "dingGone") throw new ApiError("tenant not found", 404);
        return agentDetail({ tenant: acme, tenants: [acme] });
      });
      renderPage({ linkToken: "group" });

      expect(await screen.findByRole("region", { name: "Sales team" })).toBeInTheDocument();
      expect(api.getContextConfigAgent).toHaveBeenCalledWith("agent-1", "dingGone");
      expect(api.getContextConfigAgent).toHaveBeenLastCalledWith("agent-1", "");
    });

    it("opens 个人能力 in the link's tenant after a personal link and switches, stores a token and connects there", async () => {
      api.redeemContextConfigLink.mockResolvedValue({
        agentId: "agent-1",
        workspaceId: "ws-1",
        scopeType: "person",
        scopeKey: "staff-1",
        scopeTitle: "Alice",
        orgId: "dingB",
      });
      api.getContextConfigAgent.mockResolvedValue(
        agentDetail({
          tenant: beta,
          tenants: [beta],
          person: { ...agentDetail().person!, bindings: [connectorOn("conn-wiki"), connectorOn("conn-github")] },
          offers: {
            connectors: [...agentDetail().offers.connectors, githubConnector],
            skills: agentDetail().offers.skills,
          },
        }),
      );
      api.setContextConnectorCredential.mockResolvedValue({
        connectorId: "conn-wiki",
        hint: "••••cret",
        updatedAt: "",
        kind: "bearer",
      });
      api.startContextConnectorConnection.mockResolvedValue("https://github.com/login/oauth/authorize?state=x");
      const openAuthorizeUrl = vi.fn();
      const user = userEvent.setup();
      renderPage({ linkToken: "personal", openAuthorizeUrl });

      const region = await screen.findByRole("region", { name: "Alice" });
      expect(api.getContextConfigAgent).toHaveBeenCalledWith("agent-1", "dingB");
      // Bound to the person, with no 1:1 chat and no enterprise level: no
      // other level to open.
      expect(screen.queryByRole("tablist", { name: copy.levels_aria })).not.toBeInTheDocument();

      await user.click(within(region).getByRole("switch", { name: "Turn Weekly report on or off" }));
      await waitFor(() =>
        expect(api.setContextCapabilityBinding).toHaveBeenCalledWith("agent-1", {
          scopeType: "person",
          scopeKey: "staff-1",
          orgId: "dingB",
          resourceType: "skill",
          resourceId: "skill-report",
          enabled: true,
        }),
      );

      let dialog = await openConnector(user, region, "Wiki");
      await user.click(within(dialog).getByRole("button", { name: copy.set_credential }));
      await user.type(within(dialog).getByLabelText("Bearer token for Wiki"), "secret");
      await user.click(within(dialog).getByRole("button", { name: copy.save }));
      await waitFor(() =>
        expect(api.setContextConnectorCredential).toHaveBeenCalledWith("agent-1", {
          scopeType: "person",
          scopeKey: "staff-1",
          orgId: "dingB",
          connectorId: "conn-wiki",
          bearer: "secret",
        }),
      );
      await user.keyboard("{Escape}");
      await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());

      dialog = await openConnector(user, region, "GitHub");
      await user.click(within(dialog).getByRole("button", { name: copy.connect }));
      await waitFor(() =>
        expect(openAuthorizeUrl).toHaveBeenCalledWith("https://github.com/login/oauth/authorize?state=x", {
          agentId: "agent-1",
          scopeType: "person",
          scopeKey: "staff-1",
          orgId: "dingB",
        }),
      );
      expect(api.startContextConnectorConnection).toHaveBeenCalledWith("agent-1", {
        scopeType: "person",
        scopeKey: "staff-1",
        orgId: "dingB",
        connectorId: "conn-github",
      });
    });
  });

  it("lets a person with several agents choose one", async () => {
    api.listContextConfigAgents.mockResolvedValue([
      agentSummary,
      { ...agentSummary, id: "agent-2", name: "Planner" },
    ]);
    const user = userEvent.setup();
    renderPage();

    await user.click(await screen.findByRole("button", { name: /Helper/ }));

    await waitFor(() => expect(api.getContextConfigAgent).toHaveBeenCalledWith("agent-1", ""));
    expect(await screen.findByRole("button", { name: copy.switch_agent })).toBeInTheDocument();
  });
});

const allRights = { toggle: true, connect: true, editPrompts: true, editMcp: true, editRoutines: false };
const noRights = { toggle: false, connect: false, editPrompts: false, editMcp: false, editRoutines: false };
const groupBinding: ContextConfigBinding = { agentId: "agent-1", scopeType: "scene", scopeKey: SALES_SCENE, orgId: "" };
const personBinding: ContextConfigBinding = { agentId: "agent-1", scopeType: "person", scopeKey: "staff-1", orgId: "" };
const toneprompt = { id: "p1", name: "Tone", order: 1, text: "Be brief.", enabled: true, updatedByName: "", updatedAt: "" };

function personDetail(overrides: Partial<NonNullable<ContextConfigAgentDetail["person"]>> = {}) {
  const detail = agentDetail();
  return agentDetail({ person: { ...detail.person!, ...overrides } });
}

describe("bound configuration page", () => {
  it("shows only the bound group: no agent, tenant, level, scene or group switchers", async () => {
    const acme = { orgId: "dingA", name: "Acme", source: "identity" };
    const beta = { orgId: "dingB", name: "Beta", source: "created" };
    api.getContextConfigAgent.mockResolvedValue(
      agentDetail({
        tenant: acme,
        tenants: [acme, beta],
        jsapiAvailable: true,
        scenes: [
          { scopeKey: SALES_SCENE, scopeTitle: "Sales team", source: "agent_link", expiresAt: "", kind: "group", orgId: "" },
          { scopeKey: OPS_SCENE, scopeTitle: "Ops", source: "agent_link", expiresAt: "", kind: "group", orgId: "" },
        ],
      }),
    );
    api.listContextConfigAgents.mockResolvedValue([agentSummary, { ...agentSummary, id: "agent-2", name: "Planner" }]);
    renderPage({ binding: groupBinding, pickGroup: vi.fn() });

    expect(await screen.findByRole("region", { name: "Sales team" })).toBeInTheDocument();
    expect(api.redeemContextConfigLink).not.toHaveBeenCalled();
    expect(api.listContextConfigAgents).not.toHaveBeenCalled();
    expect(api.getContextConfigAgent).toHaveBeenCalledWith("agent-1", "");
    // A member's group link has a single level: no level tabs at all.
    expect(screen.queryByRole("tablist", { name: copy.levels_aria })).not.toBeInTheDocument();
    expect(screen.queryByRole("combobox")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: copy.switch_agent })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: copy.pick_group })).not.toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "Ops" })).not.toBeInTheDocument();
  });

  it("binds a personal link to the person only", async () => {
    renderPage({ binding: personBinding });

    expect(await screen.findByRole("region", { name: "Alice" })).toBeInTheDocument();
    expect(api.getContextConfigScene).not.toHaveBeenCalled();
    expect(screen.queryByRole("region", { name: "Sales team" })).not.toBeInTheDocument();
  });

  it("opens a personal link on its 1:1 chat, with the person's own level beside it", async () => {
    const dmScene = { scopeKey: DM_SCENE, scopeTitle: "Alice", source: "agent_link", expiresAt: "", kind: "dm" as const, orgId: "" };
    api.getContextConfigAgent.mockResolvedValue(agentDetail({ scenes: [dmScene] }));
    api.getContextConfigScene.mockResolvedValue({ ...sceneDetail, scene: dmScene });
    const user = userEvent.setup();
    renderPage({ binding: personBinding });

    const levels = await screen.findByRole("tablist", { name: copy.levels_aria });
    expect(within(levels).getAllByRole("tab").map((tab) => tab.textContent)).toEqual([
      copy.level_scene,
      copy.level_person,
    ]);
    expect(within(levels).getByRole("tab", { name: copy.level_scene })).toHaveAttribute("aria-selected", "true");
    await waitFor(() => expect(api.getContextConfigScene).toHaveBeenCalledWith("agent-1", DM_SCENE, ""));
    expect(await screen.findByText(copy.kind_dm)).toBeInTheDocument();

    await user.click(within(levels).getByRole("tab", { name: copy.level_person }));
    expect(await screen.findByRole("region", { name: "Alice" })).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "Sales team" })).not.toBeInTheDocument();
  });

  it("opens no other person's scope", async () => {
    renderPage({ binding: { ...personBinding, scopeKey: "staff-2" } });

    expect(await screen.findByText(copy.scene_no_access)).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "Alice" })).not.toBeInTheDocument();
  });

  it("adds a read-only 企业能力 above 当前会话 for everyone who gets the enterprise level", async () => {
    const acme = { orgId: "dingA", name: "Acme", source: "identity" };
    const org = { scopeKey: "dingA", scopeTitle: "Acme", bindings: [], credentials: [], canEdit: false, ...noContent };
    api.getContextConfigAgent.mockResolvedValue(
      agentDetail({ tenant: acme, tenants: [acme], org: { ...org, rights: noRights } }),
    );
    const user = userEvent.setup();
    renderPage({ binding: groupBinding });

    const levels = await screen.findByRole("tablist", { name: copy.levels_aria });
    expect(within(levels).getAllByRole("tab").map((tab) => tab.textContent)).toEqual([
      copy.level_org,
      copy.level_scene,
    ]);
    // The chat opens first.
    expect(await screen.findByRole("region", { name: "Sales team" })).toBeInTheDocument();
    await user.click(within(levels).getByRole("tab", { name: copy.level_org }));
    const region = await screen.findByRole("region", { name: "Acme" });
    expect(within(region).getByText(copy.org_read_only)).toBeInTheDocument();
  });

  it("offers no 企业能力 when the server sends no enterprise level", async () => {
    renderPage({ binding: groupBinding });

    expect(await screen.findByRole("region", { name: "Sales team" })).toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: copy.level_org })).not.toBeInTheDocument();
  });

  it("lets a group link holder only view when the server says so", async () => {
    api.getContextConfigScene.mockResolvedValue({
      ...sceneDetail,
      rights: noRights,
      prompts: [toneprompt],
      mcpConfig: { mcpServers: { docs: { url: "https://mcp.example/docs" } } },
    });
    const user = userEvent.setup();
    renderPage({ binding: groupBinding });

    const region = await screen.findByRole("region", { name: "Sales team" });
    expect(within(region).getByText(copy.scene_read_only)).toBeInTheDocument();
    for (const toggle of within(region).getAllByRole("switch")) {
      expect(toggle).toHaveAttribute("aria-disabled", "true");
    }
    expect(within(region).getByRole("button", { name: "Tone" })).toBeInTheDocument();
    expect(within(region).getByText("docs")).toBeInTheDocument();
    expect(within(region).queryByRole("button", { name: copy.mcp_edit.replace("{{name}}", "docs") })).not.toBeInTheDocument();
    expect(within(region).queryByRole("button", { name: copy.prompt_add })).not.toBeInTheDocument();
    expect(within(region).queryByRole("button", { name: copy.mcp_add })).not.toBeInTheDocument();
    // The stored account shows; nothing can be stored, removed or added.
    const dialog = await openConnector(user, region, "Wiki");
    expect(within(dialog).getByText("Credential saved (••••abcd)")).toBeInTheDocument();
    expect(within(dialog).queryByRole("button", { name: copy.app_remove })).not.toBeInTheDocument();
    expect(within(dialog).queryByRole("button", { name: copy.replace_credential })).not.toBeInTheDocument();
  });

  it("lets a 1:1 chat link holder only view when the server says so", async () => {
    api.getContextConfigAgent.mockResolvedValue(
      agentDetail({
        scenes: [{ scopeKey: DM_SCENE, scopeTitle: "Bob", source: "agent_link", expiresAt: "", kind: "dm", orgId: "" }],
      }),
    );
    api.getContextConfigScene.mockResolvedValue({
      scene: { scopeKey: DM_SCENE, scopeTitle: "Bob", source: "agent_link", expiresAt: "", kind: "dm", orgId: "" },
      bindings: [],
      credentials: [],
      scope: { type: "scene", key: DM_SCENE, title: "Bob" },
      canConnect: false,
      ...noContent,
      rights: noRights,
    });
    const user = userEvent.setup();
    renderPage({ binding: { ...groupBinding, scopeKey: DM_SCENE } });

    const region = await screen.findByRole("region", { name: "Bob" });
    expect(within(region).getByText(copy.scene_read_only_dm)).toBeInTheDocument();
    expect(within(region).queryByText(copy.person_read_only)).not.toBeInTheDocument();
    expect(api.getContextConfigScene).toHaveBeenCalledWith("agent-1", DM_SCENE, "");
    const dialog = await openConnector(user, region, "Wiki");
    expect(within(dialog).getByRole("button", { name: copy.app_add })).toBeDisabled();
    expect(within(dialog).getByText(copy.app_add_read_only)).toBeInTheDocument();
  });

  it("returns a provider sign-in to the bound page", async () => {
    api.getContextConfigAgent.mockResolvedValue(
      oauthDetail({ person: { ...personDetail().person!, rights: allRights, bindings: [connectorOn("conn-github")] } }),
    );
    api.startContextConnectorConnection.mockResolvedValue("https://github.com/login/oauth/authorize?state=b");
    const openAuthorizeUrl = vi.fn();
    const returnTo = "/dingtalk/configure?agent=agent-1&scope_type=person&scope_key=staff-1";
    const user = userEvent.setup();
    renderPage({ binding: personBinding, openAuthorizeUrl, connectReturnTo: returnTo });

    const region = await screen.findByRole("region", { name: "Alice" });
    const dialog = await openConnector(user, region, "GitHub");
    await user.click(within(dialog).getByRole("button", { name: copy.connect }));
    await waitFor(() =>
      expect(api.startContextConnectorConnection).toHaveBeenCalledWith("agent-1", {
        scopeType: "person",
        scopeKey: "staff-1",
        connectorId: "conn-github",
        returnTo,
      }),
    );
  });
});

describe("top-level tabs", () => {
  it("has no 公开能力 tab: an old public link opens 场域能力", async () => {
    const onTabChange = vi.fn();
    const user = userEvent.setup();
    renderPage({ binding: groupBinding, initialTab: "public", onTabChange });

    expect(await screen.findByRole("region", { name: "Sales team" })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: copy.tab_scope })).toHaveAttribute("aria-selected", "true");
    await user.click(screen.getByRole("tab", { name: copy.tab_routines }));
    expect(onTabChange).toHaveBeenLastCalledWith("routines");
    expect(screen.queryByRole("region", { name: "Sales team" })).not.toBeInTheDocument();
  });

  it("keeps the browse page's levels under 场域能力", async () => {
    const user = userEvent.setup();
    renderPage({ initialAgentId: "agent-1" });

    expect(await screen.findByRole("tab", { name: copy.level_scene })).toBeInTheDocument();
    await user.click(screen.getByRole("tab", { name: copy.tab_routines }));
    expect(screen.queryByRole("tab", { name: copy.level_scene })).not.toBeInTheDocument();
    await user.click(screen.getByRole("tab", { name: copy.tab_scope }));
    expect(await screen.findByRole("region", { name: "Sales team" })).toBeInTheDocument();
  });
});

describe("scope prompts", () => {
  beforeEach(() => {
    api.getContextConfigScene.mockResolvedValue({ ...sceneDetail, rights: allRights, prompts: [toneprompt] });
    api.setContextConfigPrompts.mockResolvedValue([]);
  });

  it("adds a prompt in a dialog after checking it, saving the whole list", async () => {
    const user = userEvent.setup();
    renderPage({ binding: groupBinding });

    const region = await screen.findByRole("region", { name: "Sales team" });
    await user.click(within(region).getByRole("button", { name: copy.prompt_add }));
    const dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByRole("button", { name: copy.save }));
    expect(within(dialog).getByRole("alert")).toHaveTextContent(
      enAgents.tab_body.context_builder.prompt_name_required,
    );
    await user.type(within(dialog).getByLabelText(copy.prompt_name), "Tone");
    await user.type(within(dialog).getByLabelText(copy.prompt_text), "x");
    await user.click(within(dialog).getByRole("button", { name: copy.save }));
    expect(within(dialog).getByRole("alert")).toHaveTextContent(
      enAgents.tab_body.context_builder.prompt_name_duplicate,
    );
    expect(api.setContextConfigPrompts).not.toHaveBeenCalled();

    await user.clear(within(dialog).getByLabelText(copy.prompt_name));
    await user.type(within(dialog).getByLabelText(copy.prompt_name), "Format");
    await user.clear(within(dialog).getByLabelText(copy.prompt_text));
    await user.type(within(dialog).getByLabelText(copy.prompt_text), "Use lists.");
    await user.click(within(dialog).getByRole("button", { name: copy.save }));
    await waitFor(() =>
      expect(api.setContextConfigPrompts).toHaveBeenCalledWith("agent-1", { scopeType: "scene", scopeKey: SALES_SCENE }, [
        { name: "Tone", order: 1, text: "Be brief.", enabled: true },
        { name: "Format", order: 2, text: "Use lists.", enabled: true },
      ]),
    );
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });

  it("switches a prompt on its row and opens it to edit or delete", async () => {
    const user = userEvent.setup();
    renderPage({ binding: groupBinding });

    const region = await screen.findByRole("region", { name: "Sales team" });
    // One line: no text on the row.
    expect(within(region).queryByText("Be brief.")).not.toBeInTheDocument();
    await user.click(within(region).getByRole("switch", { name: "Turn Tone on or off" }));
    await waitFor(() =>
      expect(api.setContextConfigPrompts).toHaveBeenLastCalledWith(
        "agent-1",
        { scopeType: "scene", scopeKey: SALES_SCENE },
        [{ name: "Tone", order: 1, text: "Be brief.", enabled: false }],
      ),
    );

    await user.click(await within(region).findByRole("button", { name: "Tone" }));
    let dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText("Be brief.")).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: copy.action_edit }));
    const text = within(dialog).getByLabelText(copy.prompt_text);
    expect(text).toHaveValue("Be brief.");
    await user.clear(text);
    await user.type(text, "Be very brief.");
    await user.click(within(dialog).getByRole("button", { name: copy.save }));
    await waitFor(() =>
      expect(api.setContextConfigPrompts).toHaveBeenLastCalledWith(
        "agent-1",
        { scopeType: "scene", scopeKey: SALES_SCENE },
        [{ name: "Tone", order: 1, text: "Be very brief.", enabled: true }],
      ),
    );
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());

    await user.click(within(region).getByRole("button", { name: "Tone" }));
    dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByRole("button", { name: copy.action_delete }));
    const confirm = await screen.findByRole("alertdialog");
    await user.click(within(confirm).getByRole("button", { name: copy.prompt_delete.replace("{{name}}", "Tone") }));
    await waitFor(() =>
      expect(api.setContextConfigPrompts).toHaveBeenLastCalledWith(
        "agent-1",
        { scopeType: "scene", scopeKey: SALES_SCENE },
        [],
      ),
    );
  });

  it("offers no prompt or MCP editing on a backend without rights", async () => {
    api.getContextConfigScene.mockResolvedValue(sceneDetail);
    renderPage({ binding: groupBinding });

    const region = await screen.findByRole("region", { name: "Sales team" });
    expect(within(region).queryByText(copy.prompts_title)).not.toBeInTheDocument();
    expect(within(region).queryByText(copy.mcp_title)).not.toBeInTheDocument();
  });
});

describe("scope MCP servers", () => {
  beforeEach(() => {
    api.getContextConfigAgent.mockResolvedValue(personDetail({ rights: allRights }));
    api.setContextConfigMcpConfig.mockImplementation(
      async (_agentId: string, _scope: unknown, config: Record<string, unknown> | null) => config,
    );
  });

  it("adds a remote URL server only, in a dialog", async () => {
    const user = userEvent.setup();
    renderPage({ binding: personBinding });

    const region = await screen.findByRole("region", { name: "Alice" });
    await user.click(within(region).getByRole("button", { name: copy.mcp_add }));
    const dialog = await screen.findByRole("dialog");
    // A remote server only: no command, arguments or environment.
    expect(within(dialog).queryByLabelText(/command/i)).not.toBeInTheDocument();
    expect(within(dialog).getByText(copy.mcp_remote_only)).toBeInTheDocument();

    await user.type(within(dialog).getByLabelText(copy.mcp_name), "multica");
    await user.type(within(dialog).getByLabelText(copy.mcp_url), "https://mcp.example/docs");
    await user.click(within(dialog).getByRole("button", { name: copy.save }));
    expect(within(dialog).getByRole("alert")).toHaveTextContent(copy.mcp_name_reserved);

    await user.clear(within(dialog).getByLabelText(copy.mcp_name));
    await user.type(within(dialog).getByLabelText(copy.mcp_name), "docs");
    await user.clear(within(dialog).getByLabelText(copy.mcp_url));
    await user.type(within(dialog).getByLabelText(copy.mcp_url), "file:///usr/bin/server");
    await user.click(within(dialog).getByRole("button", { name: copy.save }));
    expect(within(dialog).getByRole("alert")).toHaveTextContent(copy.mcp_url_invalid);
    expect(api.setContextConfigMcpConfig).not.toHaveBeenCalled();

    await user.clear(within(dialog).getByLabelText(copy.mcp_url));
    await user.type(within(dialog).getByLabelText(copy.mcp_url), "https://mcp.example/docs");
    await user.click(within(dialog).getByRole("button", { name: copy.mcp_header_add }));
    await user.type(within(dialog).getByLabelText(copy.mcp_header_name), "Authorization");
    await user.type(within(dialog).getByLabelText(copy.mcp_header_value), "Bearer t");
    await user.click(within(dialog).getByRole("button", { name: copy.save }));
    await waitFor(() =>
      expect(api.setContextConfigMcpConfig).toHaveBeenCalledWith(
        "agent-1",
        { scopeType: "person", scopeKey: "staff-1" },
        {
          mcpServers: {
            docs: { type: "http", url: "https://mcp.example/docs", headers: { Authorization: "Bearer t" } },
          },
        },
      ),
    );
  });

  it("configures a server from its row: switch it off and delete it", async () => {
    api.getContextConfigAgent.mockResolvedValue(
      personDetail({ rights: allRights, mcpConfig: { mcpServers: { docs: { url: "https://mcp.example/docs" } } } }),
    );
    const user = userEvent.setup();
    renderPage({ binding: personBinding });

    const region = await screen.findByRole("region", { name: "Alice" });
    // One line: its name and host, then 配置.
    expect(within(region).getByText("mcp.example")).toBeInTheDocument();
    await user.click(within(region).getByRole("button", { name: copy.mcp_edit.replace("{{name}}", "docs") }));
    const dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByRole("switch", { name: "Turn docs on or off" }));
    await waitFor(() =>
      expect(api.setContextConfigMcpConfig).toHaveBeenLastCalledWith(
        "agent-1",
        { scopeType: "person", scopeKey: "staff-1" },
        { mcpServers: { docs: { url: "https://mcp.example/docs", disabled: true } } },
      ),
    );

    await user.click(within(dialog).getByRole("button", { name: copy.mcp_delete.replace("{{name}}", "docs") }));
    const confirm = await screen.findByRole("alertdialog");
    await user.click(within(confirm).getByRole("button", { name: copy.mcp_delete.replace("{{name}}", "docs") }));
    await waitFor(() =>
      expect(api.setContextConfigMcpConfig).toHaveBeenLastCalledWith(
        "agent-1",
        { scopeType: "person", scopeKey: "staff-1" },
        { mcpServers: {} },
      ),
    );
  });

  it("lists a document holding a local server read-only", async () => {
    api.getContextConfigAgent.mockResolvedValue(
      personDetail({
        rights: allRights,
        mcpConfig: {
          mcpServers: { docs: { url: "https://mcp.example/docs" }, local: { command: "npx", args: ["server"] } },
        },
      }),
    );
    renderPage({ binding: personBinding });

    const region = await screen.findByRole("region", { name: "Alice" });
    expect(within(region).getByText("local")).toBeInTheDocument();
    expect(within(region).getByText(copy.mcp_locked)).toBeInTheDocument();
    expect(within(region).queryByRole("button", { name: copy.mcp_add })).not.toBeInTheDocument();
    expect(within(region).queryByRole("button", { name: copy.mcp_edit.replace("{{name}}", "docs") })).not.toBeInTheDocument();
  });
});

const routineCopy = copy.routines;

const standupRoutine = {
  id: "routine-1",
  sceneId: SALES_SCENE,
  sceneKind: "group",
  autopilotId: "ap-1",
  title: "Weekday standup",
  instructions: "Remind the team.",
  enabled: true,
  pauseReason: "",
  trigger: {
    id: "trigger-1",
    kind: "schedule",
    cron: "0 9 * * 1-5",
    timezone: "Asia/Shanghai",
    nextRunAt: "2026-10-05T01:00:00Z",
    nextRuns: ["2026-10-05T01:00:00Z"],
    webhookUrlMasked: "",
    webhookUrl: "",
  },
  lastRun: null,
  createdByType: "agent",
  createdAt: "",
  updatedAt: "",
};

describe("routines tab", () => {
  it("lists a group's routines read-only without the routines right", async () => {
    api.listSceneRoutines.mockResolvedValue([standupRoutine]);
    renderPage({ binding: groupBinding, initialTab: "routines" });

    expect(await screen.findByText("Weekday standup")).toBeInTheDocument();
    expect(api.listSceneRoutines).toHaveBeenCalledWith({ kind: "config", agentId: "agent-1", sceneId: SALES_SCENE, orgId: "" });
    // Just the list: no heading or explanation.
    expect(screen.queryByText(routineCopy.read_only)).not.toBeInTheDocument();
    expect(screen.queryByText(routineCopy.subtitle)).not.toBeInTheDocument();
    expect(screen.queryByText(routineCopy.created_in_chat)).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: routineCopy.add })).not.toBeInTheDocument();
    expect(screen.queryByRole("switch")).not.toBeInTheDocument();
  });

  it("creates a webhook routine for a manager and shows its URL once", async () => {
    api.getContextConfigScene.mockResolvedValue({ ...sceneDetail, rights: { ...allRights, editRoutines: true } });
    api.createSceneRoutine.mockResolvedValue({
      updated: false,
      routine: {
        ...standupRoutine,
        id: "routine-2",
        title: "Deploy summary",
        trigger: {
          ...standupRoutine.trigger,
          kind: "webhook",
          cron: "",
          timezone: "",
          nextRunAt: null,
          nextRuns: [],
          webhookUrl: "https://multica.example/api/webhooks/autopilots/awt_secret_token",
        },
      },
    });
    const user = userEvent.setup();
    renderPage({ binding: groupBinding, initialTab: "routines" });

    await user.click(await screen.findByRole("button", { name: routineCopy.add }));
    const dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByRole("button", { name: routineCopy.create }));
    expect(within(dialog).getByText(routineCopy.field_title_required)).toBeInTheDocument();
    expect(api.createSceneRoutine).not.toHaveBeenCalled();

    await user.type(within(dialog).getByLabelText(routineCopy.field_title), "Deploy summary");
    await user.type(within(dialog).getByLabelText(routineCopy.field_instructions), "Summarize the deploy.");
    await user.click(within(dialog).getByRole("tab", { name: routineCopy.trigger_webhook }));
    await user.click(within(dialog).getByRole("button", { name: routineCopy.create }));

    await waitFor(() =>
      expect(api.createSceneRoutine).toHaveBeenCalledWith(
        { kind: "config", agentId: "agent-1", sceneId: SALES_SCENE, orgId: "" },
        { title: "Deploy summary", instructions: "Summarize the deploy.", trigger: { kind: "webhook" } },
      ),
    );
    expect(
      await screen.findByRole("heading", { name: routineCopy.webhook_reveal_title.replace("{{name}}", "Deploy summary") }),
    ).toBeInTheDocument();
  });

  it("pauses a routine through its switch", async () => {
    api.getContextConfigScene.mockResolvedValue({ ...sceneDetail, rights: { ...allRights, editRoutines: true } });
    api.listSceneRoutines.mockResolvedValue([standupRoutine]);
    api.updateSceneRoutine.mockResolvedValue({ updated: false, routine: { ...standupRoutine, enabled: false } });
    const user = userEvent.setup();
    renderPage({ binding: groupBinding, initialTab: "routines" });

    await user.click(await screen.findByRole("switch", { name: routineCopy.toggle.replace("{{name}}", "Weekday standup") }));
    await waitFor(() =>
      expect(api.updateSceneRoutine).toHaveBeenCalledWith(
        { kind: "config", agentId: "agent-1", sceneId: SALES_SCENE, orgId: "" },
        "routine-1",
        { enabled: false },
      ),
    );
  });

  it("opens a routine on click with its run history", async () => {
    api.listSceneRoutines.mockResolvedValue([standupRoutine]);
    api.listSceneRoutineRuns.mockResolvedValue([
      {
        id: "run-2",
        status: "failed",
        source: "manual",
        failureReason: "agent offline",
        createdAt: "2026-10-02T01:00:00Z",
        completedAt: "2026-10-02T01:01:00Z",
      },
      { id: "run-1", status: "completed", source: "schedule", failureReason: "", createdAt: "2026-10-01T01:00:00Z", completedAt: "2026-10-01T01:03:00Z" },
    ]);
    const user = userEvent.setup();
    renderPage({ binding: groupBinding, initialTab: "routines" });

    // The history is fetched only when a routine is opened.
    await user.click(
      await screen.findByRole("button", { name: /^Weekday standup/ }),
    );
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByRole("heading", { name: "Weekday standup" })).toBeInTheDocument();
    expect(within(dialog).getByText("Remind the team.")).toBeInTheDocument();
    const history = await within(dialog).findByRole("list", { name: routineCopy.history_title });
    const rows = within(history).getAllByRole("listitem");
    expect(rows).toHaveLength(2);
    expect(rows[0]).toHaveTextContent(routineCopy.status_failed);
    expect(rows[0]).toHaveTextContent(routineCopy.source_manual);
    expect(rows[0]).toHaveTextContent("agent offline");
    expect(rows[1]).toHaveTextContent(routineCopy.status_completed);
    expect(rows[1]).toHaveTextContent(routineCopy.source_schedule);
    expect(api.listSceneRoutineRuns).toHaveBeenCalledWith(
      { kind: "config", agentId: "agent-1", sceneId: SALES_SCENE, orgId: "" },
      "routine-1",
    );
  });

  it("says a routine has no runs yet", async () => {
    api.listSceneRoutines.mockResolvedValue([standupRoutine]);
    api.listSceneRoutineRuns.mockResolvedValue([]);
    const user = userEvent.setup();
    renderPage({ binding: groupBinding, initialTab: "routines" });

    await user.click(
      await screen.findByRole("button", { name: /^Weekday standup/ }),
    );
    expect(await within(await screen.findByRole("dialog")).findByText(routineCopy.history_empty)).toBeInTheDocument();
    expect(api.listSceneRoutineRuns).toHaveBeenCalledTimes(1);
  });

  it("says a person level without its 1:1 chat has no routines", async () => {
    renderPage({ binding: personBinding, initialTab: "routines" });
    expect(await screen.findByText(routineCopy.not_scene_title)).toBeInTheDocument();
    expect(api.listSceneRoutines).not.toHaveBeenCalled();
  });

  it("opens the routines of the 1:1 chat a personal link came from", async () => {
    const dmScene = { scopeKey: DM_SCENE, scopeTitle: "Alice", source: "agent_link", expiresAt: "", kind: "dm" as const, orgId: "" };
    api.getContextConfigAgent.mockResolvedValue(agentDetail({ scenes: [dmScene] }));
    api.getContextConfigScene.mockResolvedValue({ ...sceneDetail, scene: dmScene, rights: { ...allRights, editRoutines: true } });
    api.listSceneRoutines.mockResolvedValue([standupRoutine]);
    renderPage({ binding: personBinding, initialTab: "routines" });

    expect(await screen.findByText("Weekday standup")).toBeInTheDocument();
    expect(api.listSceneRoutines).toHaveBeenCalledWith({ kind: "config", agentId: "agent-1", sceneId: DM_SCENE, orgId: "" });
  });
});
