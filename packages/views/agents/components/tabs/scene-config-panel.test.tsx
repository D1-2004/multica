// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { toast } from "sonner";
import type { Agent } from "@multica/core/types";
import type {
  AgentSceneDetail,
  AgentSceneOfferedConnector,
  AgentSceneSummary,
} from "@multica/core/context-capabilities";
import { I18nProvider } from "@multica/core/i18n/react";
import { WorkspaceSlugProvider } from "@multica/core/paths";
import enCommon from "../../../locales/en/common.json";
import enAgents from "../../../locales/en/agents.json";
import { NavigationProvider, type NavigationAdapter } from "../../../navigation";
import { SceneConfigPanel } from "./scene-config-panel";

const mocks = vi.hoisted(() => ({
  getCaps: vi.fn(),
  setBinding: vi.fn(),
  setMcpConfig: vi.fn(),
  setCredential: vi.fn(),
  deleteCredential: vi.fn(),
  startConnection: vi.fn(),
  setPrompt: vi.fn(),
}));

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/api", async () => {
  const actual = await vi.importActual<typeof import("@multica/core/api")>("@multica/core/api");
  return {
    ...actual,
    api: {
      getAgentContextCapabilities: mocks.getCaps,
      setAgentSceneBinding: mocks.setBinding,
      setAgentSceneMcpConfig: mocks.setMcpConfig,
      setContextConnectorCredential: mocks.setCredential,
      deleteContextConnectorCredential: mocks.deleteCredential,
      startContextConnectorConnection: mocks.startConnection,
      setAgentScenePrompt: mocks.setPrompt,
    },
  };
});
vi.mock("sonner", () => ({ toast: { error: vi.fn(), success: vi.fn(), info: vi.fn() } }));

const copy = enAgents.tab_body.scenes;
const apps = enAgents.tab_body.connected_apps;
const agent = { id: "agent-1", workspace_id: "ws-1", name: "Helper" } as Agent;

const groupScene: AgentSceneSummary = {
  sceneKey: "cid-group",
  kind: "group",
  title: "Release crew",
  orgId: "org",
  lastActiveAt: "",
  inboundSessionId: "",
  inboundCount: 0,
  memoryId: "",
  hasPrompt: false,
};

function connector(overrides: Partial<AgentSceneOfferedConnector>): AgentSceneOfferedConnector {
  return {
    id: "conn-wiki",
    name: "Wiki",
    catalogSlug: "",
    authMode: "bearer",
    acceptsCredential: true,
    acceptsPat: false,
    oauthAvailable: false,
    installUrl: "",
    credential: { connected: false, account: "" },
    ...overrides,
  };
}

const wiki = connector({});
const github = connector({
  id: "conn-github",
  name: "GitHub",
  catalogSlug: "github",
  authMode: "oauth",
  acceptsPat: true,
  oauthAvailable: true,
  installUrl: "https://github.com/apps/qwen-tag-pre/installations/new",
  credential: { connected: true, account: "@team" },
});
const notion = connector({ id: "conn-notion", name: "Notion", catalogSlug: "notion", authMode: "oauth", acceptsCredential: false, oauthAvailable: true });

function detailOf(overrides: Partial<AgentSceneDetail> = {}): AgentSceneDetail {
  return {
    scene: groupScene,
    prompt: { text: "", updatedAt: "", updatedByName: "" },
    bindings: [
      { resourceType: "connector", resourceId: "conn-wiki", enabled: true, updatedByName: "", updatedAt: "" },
      { resourceType: "connector", resourceId: "conn-github", enabled: true, updatedByName: "", updatedAt: "" },
    ],
    offers: {
      connectors: [wiki, github, notion],
      skills: [{ id: "skill-report", name: "Weekly report", description: "Writes reports" }],
    },
    scope: { type: "scene", key: "cid-group", title: "Release crew" },
    mcpConfig: { mcpServers: { docs: { url: "https://mcp.example/docs" } } },
    mcpConfigSupported: true,
    mcpConfigRedacted: false,
    canConnect: true,
    ...overrides,
  };
}

function renderPanel({
  detail = detailOf(),
  search = "view=scenes&scene=cid-group&scene_tab=config",
  canEdit = true,
}: { detail?: AgentSceneDetail; search?: string; canEdit?: boolean } = {}) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const navigation: NavigationAdapter = {
    push: vi.fn(),
    replace: vi.fn(),
    back: vi.fn(),
    pathname: "/acme/agents/agent-1",
    searchParams: new URLSearchParams(search),
    getShareableUrl: (path) => path,
  };
  render(
    <I18nProvider locale="en" resources={{ en: { common: enCommon, agents: enAgents } }}>
      <WorkspaceSlugProvider slug="acme">
        <NavigationProvider value={navigation}>
          <QueryClientProvider client={client}>
            <SceneConfigPanel agent={agent} detail={detail} canEdit={canEdit} />
          </QueryClientProvider>
        </NavigationProvider>
      </WorkspaceSlugProvider>
    </I18nProvider>,
  );
  return { navigation };
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.getCaps.mockResolvedValue({
    enabled: true,
    library: { connectors: [], skills: [] },
    offers: { connectorIds: [], skillIds: [] },
    scenes: [],
    persons: [],
    configureUrl: "https://app.example/dingtalk/configure?agent=agent-1",
  });
  mocks.setCredential.mockResolvedValue({ connectorId: "conn-wiki", hint: "••••oken", updatedAt: "", kind: "bearer" });
  mocks.deleteCredential.mockResolvedValue(undefined);
  mocks.setMcpConfig.mockImplementation(async (_ws: string, _agent: string, _key: string, config: unknown) => config);
  mocks.startConnection.mockResolvedValue("");
});

describe("scene MCP", () => {
  it("lists offered Aone FaaS connectors with their scene state and stores the scene's token", async () => {
    const user = userEvent.setup();
    renderPanel();

    const row = screen.getByRole("listitem", { name: "Wiki" });
    expect(within(row).getByText(apps.usage_enabled)).toBeInTheDocument();
    expect(within(row).getByText(apps.shared_none)).toBeInTheDocument();
    expect(within(row).getByRole("switch", { name: "Turn on Wiki in this scene" })).toBeChecked();
    // Official apps are tiles in 连接应用, not MCP rows.
    expect(screen.queryByRole("listitem", { name: "GitHub" })).not.toBeInTheDocument();

    await user.click(within(row).getByRole("button", { name: copy.token_set }));
    await user.type(within(row).getByLabelText("Wiki token"), "secret-token");
    await user.click(within(row).getByRole("button", { name: enAgents.internal_mcp.catalog.save }));

    // The configure-page credential route with the scene key; the server maps
    // a 1:1 chat to its person.
    await waitFor(() =>
      expect(mocks.setCredential).toHaveBeenCalledWith("agent-1", {
        scopeType: "scene",
        scopeKey: "cid-group",
        connectorId: "conn-wiki",
        bearer: "secret-token",
      }),
    );
    expect(toast.success).toHaveBeenCalledWith(copy.token_saved);
  });

  it("removes a stored scene token after confirmation", async () => {
    const user = userEvent.setup();
    renderPanel({
      detail: detailOf({
        offers: {
          connectors: [connector({ credential: { connected: true, account: "••••abcd" } })],
          skills: [],
        },
      }),
    });

    const row = screen.getByRole("listitem", { name: "Wiki" });
    expect(within(row).getByText("Connected ••••abcd")).toBeInTheDocument();
    await user.click(within(row).getByRole("button", { name: copy.token_remove }));
    const dialog = await screen.findByRole("alertdialog");
    await user.click(within(dialog).getByRole("button", { name: copy.token_remove }));

    await waitFor(() =>
      expect(mocks.deleteCredential).toHaveBeenCalledWith("agent-1", {
        scopeType: "scene",
        scopeKey: "cid-group",
        connectorId: "conn-wiki",
      }),
    );
  });

  it("switches an offered connector for the scene", async () => {
    mocks.setBinding.mockResolvedValue({ resourceType: "connector", resourceId: "conn-wiki", enabled: false, updatedByName: "", updatedAt: "" });
    const user = userEvent.setup();
    renderPanel();

    await user.click(screen.getByRole("switch", { name: "Turn on Wiki in this scene" }));

    await waitFor(() =>
      expect(mocks.setBinding).toHaveBeenCalledWith("ws-1", "agent-1", "cid-group", {
        resourceType: "connector",
        resourceId: "conn-wiki",
        enabled: false,
      }),
    );
  });

  it("adds a custom MCP server to the scene's own document", async () => {
    const user = userEvent.setup();
    renderPanel();

    const custom = screen.getByRole("group", { name: enAgents.tab_body.connectors.custom_title });
    expect(within(custom).getByText("docs")).toBeInTheDocument();
    await user.click(within(custom).getByRole("button", { name: enAgents.tab_body.mcp_config.add_action }));
    await user.type(screen.getByLabelText("Name"), "fetch");
    await user.type(screen.getByLabelText("Command"), "uvx");
    await user.click(screen.getByRole("button", { name: /add server/i }));

    await waitFor(() =>
      expect(mocks.setMcpConfig).toHaveBeenCalledWith("ws-1", "agent-1", "cid-group", {
        mcpServers: { docs: { url: "https://mcp.example/docs" }, fetch: { command: "uvx" } },
      }),
    );
  });

  it("clears the document when the last custom server is deleted", async () => {
    const user = userEvent.setup();
    renderPanel();

    await user.click(screen.getByRole("button", { name: /delete mcp server docs/i }));
    const dialog = await screen.findByRole("alertdialog");
    await user.click(within(dialog).getByRole("button", { name: enAgents.tab_body.mcp_config.delete_action }));

    await waitFor(() => expect(mocks.setMcpConfig).toHaveBeenCalledWith("ws-1", "agent-1", "cid-group", null));
  });

  it("never offers to save over custom servers the workspace withholds", () => {
    renderPanel({ detail: detailOf({ mcpConfig: null, mcpConfigRedacted: true }) });

    const custom = screen.getByRole("group", { name: enAgents.tab_body.connectors.custom_title });
    expect(within(custom).getByText(enAgents.tab_body.mcp_config.redacted_title)).toBeInTheDocument();
    expect(within(custom).queryByRole("button", { name: enAgents.tab_body.mcp_config.add_action })).toBeNull();
    expect(within(custom).queryByText(copy.none)).toBeNull();
  });

  it("says the scene's custom servers are stored only", () => {
    renderPanel();

    const custom = screen.getByRole("group", { name: enAgents.tab_body.connectors.custom_title });
    expect(within(custom).getByText(copy.prompt_hint)).toBeInTheDocument();
  });

  it("hides the custom MCP editor on a backend without the route", () => {
    renderPanel({ detail: detailOf({ mcpConfig: null, mcpConfigSupported: false }) });

    expect(screen.queryByRole("group", { name: enAgents.tab_body.connectors.custom_title })).toBeNull();
    expect(screen.queryByRole("button", { name: enAgents.tab_body.mcp_config.add_action })).toBeNull();
    // The offered connectors still show.
    expect(screen.getByRole("listitem", { name: "Wiki" })).toBeInTheDocument();
  });
});

describe("scene apps", () => {
  it("shows offered apps as tiles with the scene's status and opens one through the address", async () => {
    const user = userEvent.setup();
    const { navigation } = renderPanel();

    const tile = screen.getByRole("button", { name: "Configure GitHub" });
    expect(within(tile).getByText(apps.usage_enabled)).toBeInTheDocument();
    expect(within(tile).getByText("Connected @team")).toBeInTheDocument();
    expect(
      within(screen.getByRole("button", { name: "Configure Notion" })).getByText(apps.usage_not_enabled),
    ).toBeInTheDocument();

    await user.click(tile);
    expect(navigation.replace).toHaveBeenCalledWith(
      "/acme/agents/agent-1?view=scenes&scene=cid-group&scene_tab=config&app=github",
    );
  });

  it("configures an app in its dialog and connects the scene's account in this browser", async () => {
    const user = userEvent.setup();
    renderPanel({ search: "view=scenes&scene=cid-group&scene_tab=config&app=notion" });

    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByRole("switch", { name: copy.enable_label })).not.toBeChecked();
    await user.click(within(dialog).getByRole("button", { name: apps.connect }));

    await waitFor(() =>
      expect(mocks.startConnection).toHaveBeenCalledWith("agent-1", {
        scopeType: "scene",
        scopeKey: "cid-group",
        connectorId: "conn-notion",
        returnTo: "/acme/agents/agent-1?view=scenes&scene=cid-group&scene_tab=config&app=notion",
      }),
    );
    // No usable authorization URL: nothing navigates.
    await waitFor(() => expect(toast.error).toHaveBeenCalledWith("Couldn't start sign-in for Notion."));
  });

  it("disconnects the scene's account and closes the dialog through the address", async () => {
    const user = userEvent.setup();
    const { navigation } = renderPanel({ search: "view=scenes&scene=cid-group&scene_tab=config&app=github" });

    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText("Connected @team")).toBeInTheDocument();
    expect(within(dialog).getByRole("switch", { name: copy.enable_label })).toBeChecked();
    await user.click(within(dialog).getByRole("button", { name: apps.disconnect }));
    const confirm = await screen.findByRole("alertdialog");
    await user.click(within(confirm).getByRole("button", { name: apps.disconnect }));
    await waitFor(() =>
      expect(mocks.deleteCredential).toHaveBeenCalledWith("agent-1", {
        scopeType: "scene",
        scopeKey: "cid-group",
        connectorId: "conn-github",
      }),
    );

    await user.click(within(dialog).getByRole("button", { name: "Close" }));
    expect(navigation.replace).toHaveBeenLastCalledWith("/acme/agents/agent-1?view=scenes&scene=cid-group&scene_tab=config");
  });

  it("reports a sign-in result as a toast and keeps the app open", async () => {
    const { navigation } = renderPanel({
      search: "view=scenes&scene=cid-group&scene_tab=config&app=github&connected=github",
    });

    await waitFor(() => expect(toast.success).toHaveBeenCalledWith("GitHub connected."));
    expect(navigation.replace).toHaveBeenCalledWith(
      "/acme/agents/agent-1?view=scenes&scene=cid-group&scene_tab=config&app=github",
    );
    expect(await screen.findByRole("dialog")).toBeInTheDocument();
  });
});

describe("who may connect, and 1:1 chats", () => {
  it("points to the configure page instead of a token control when the caller cannot connect", () => {
    renderPanel({ detail: detailOf({ canConnect: false }) });

    const row = screen.getByRole("listitem", { name: "Wiki" });
    expect(within(row).queryByRole("button", { name: copy.token_set })).not.toBeInTheDocument();
    expect(within(row).getAllByText(copy.configure_connects).length).toBeGreaterThan(0);
    // Switching the connector for the scene stays a manager action.
    expect(within(row).getByRole("switch", { name: "Turn on Wiki in this scene" })).not.toHaveAttribute(
      "aria-disabled",
      "true",
    );
  });

  it("shows an app account read only when the caller cannot connect", async () => {
    renderPanel({
      detail: detailOf({ canConnect: false }),
      search: "view=scenes&scene=cid-group&scene_tab=config&app=github",
    });

    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText("Connected @team")).toBeInTheDocument();
    expect(within(dialog).queryByRole("button", { name: apps.reconnect })).not.toBeInTheDocument();
    expect(within(dialog).queryByRole("button", { name: apps.disconnect })).not.toBeInTheDocument();
    expect(within(dialog).getByText(copy.configure_connects)).toBeInTheDocument();
  });

  it("says the person connects in their 1:1 chat when a manager cannot", () => {
    renderPanel({
      detail: detailOf({
        scene: { ...groupScene, sceneKey: "cid-dm", kind: "dm", title: "" },
        scope: { type: "person", key: "staff-1", title: "Ada" },
        canConnect: false,
      }),
    });

    const row = screen.getByRole("listitem", { name: "Wiki" });
    expect(within(row).getAllByText(copy.owner_connects).length).toBeGreaterThan(0);
    expect(within(row).queryByText(copy.configure_connects)).not.toBeInTheDocument();
  });

  it("says a 1:1 chat's configuration is its person's", () => {
    renderPanel({
      detail: detailOf({
        scene: { ...groupScene, sceneKey: "cid-dm", kind: "dm", title: "" },
        scope: { type: "person", key: "staff-1", title: "Ada" },
      }),
    });

    expect(screen.getByText("1:1 chat · bound to Ada: these are their personal settings")).toBeInTheDocument();
    expect(screen.getByRole("listitem", { name: "Wiki" })).toBeInTheDocument();
  });

  it("configures nothing for a 1:1 chat whose person is unknown", () => {
    renderPanel({
      detail: detailOf({ scene: { ...groupScene, sceneKey: "cid-dm", kind: "dm" }, scope: null }),
    });

    expect(screen.getByText(copy.scope_unknown)).toBeInTheDocument();
    expect(screen.queryByRole("listitem", { name: "Wiki" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Configure GitHub" })).not.toBeInTheDocument();
    // The scene prompt belongs to the chat itself and stays editable.
    expect(screen.getByRole("textbox", { name: copy.prompt_title })).toBeInTheDocument();
  });

  it("does not call a group with an unreadable scope a 1:1 chat", () => {
    renderPanel({ detail: detailOf({ scope: null }) });

    expect(screen.getByText(copy.scope_unavailable)).toBeInTheDocument();
    expect(screen.queryByText(copy.scope_unknown)).not.toBeInTheDocument();
    expect(screen.queryByRole("listitem", { name: "Wiki" })).not.toBeInTheDocument();
  });

  it("keeps the configure page link to a label and the URL", async () => {
    renderPanel();

    expect(await screen.findByDisplayValue("https://app.example/dingtalk/configure?agent=agent-1")).toBeInTheDocument();
    expect(screen.getByText(enAgents.tab_body.context_offers.configure_title)).toBeInTheDocument();
  });
});
