// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import { configStore } from "@multica/core/config";
import { WorkspaceSlugProvider } from "@multica/core/paths";
import enCommon from "../locales/en/common.json";
import enAgents from "../locales/en/agents.json";
import { InternalConnectorsPage } from "./internal-connectors-page";

const mocks = vi.hoisted(() => ({
  list: vi.fn(),
  update: vi.fn(),
  catalog: vi.fn(),
  addCatalog: vi.fn(),
  startOAuth: vi.fn(),
  refreshTools: vi.fn(),
  setCredential: vi.fn(),
}));

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "workspace-1" }));
vi.mock("@multica/core/auth", () => ({
  useAuthStore: (selector: (state: { user: { id: string } }) => unknown) => selector({ user: { id: "user-1" } }),
}));
vi.mock("@multica/core/workspace/queries", () => ({
  memberListOptions: () => ({ queryKey: ["members", "workspace-1"], queryFn: async () => [{ user_id: "user-1", role: "owner" }] }),
  agentListOptions: () => ({ queryKey: ["agents", "workspace-1"], queryFn: async () => [{ id: "agent-1", name: "Probe" }] }),
}));
vi.mock("@multica/core/api", () => ({
  api: {
    listInternalConnectors: mocks.list,
    updateInternalConnector: mocks.update,
    listConnectorCatalog: mocks.catalog,
    addCatalogConnector: mocks.addCatalog,
    startInternalConnectorOAuth: mocks.startOAuth,
    refreshInternalConnectorTools: mocks.refreshTools,
    setInternalConnectorCredential: mocks.setCredential,
  },
  // Mirrors the real helper: the stable `code` of a writeErrorCode body.
  errorCode: (error: unknown) => {
    const body = (error as { body?: { code?: unknown } } | null)?.body;
    return typeof body?.code === "string" ? body.code : undefined;
  },
}));

const catalogCopy = enAgents.internal_mcp.catalog;

const connector = {
  id: "11111111-1111-4111-8111-111111111111",
  workspaceId: "workspace-1",
  name: "Knowledge",
  upstreamUrl: "https://approved.example/mcp",
  credentialRef: "MULTICA_INTERNAL_MCP_BEARER_TEST",
  credentialReady: true,
  credentialSource: "workspace",
  credentialOptional: false,
  authMode: "bearer",
  allowedTools: ["read_knowledge"],
  agentIds: ["agent-1"],
  enabled: false,
  catalogSlug: "",
  writeEnabled: false,
  discoveredToolCount: 0,
  credentialAccount: "",
};

const githubConnector = {
  ...connector,
  id: "33333333-3333-4333-8333-333333333333",
  name: "GitHub",
  upstreamUrl: "https://api.githubcopilot.com/mcp/",
  authMode: "oauth",
  credentialSource: "workspace",
  credentialOptional: true,
  enabled: true,
  agentIds: [],
  allowedTools: ["get_me", "search_code", "get_file_contents"],
  catalogSlug: "github",
  discoveredToolCount: 48,
  credentialAccount: "@octocat",
};

const githubApp = {
  slug: "github",
  name: "GitHub",
  mcpUrl: "https://api.githubcopilot.com/mcp/",
  authKind: "oauth_github_app",
  allowsPat: true,
  oauthAvailable: true,
  connectorId: null as string | null,
  installUrl: "https://github.com/apps/multica/installations/new",
};

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(
    <I18nProvider locale="en" resources={{ en: { common: enCommon, agents: enAgents } }}>
      <WorkspaceSlugProvider slug="acme">
        <QueryClientProvider client={client}><InternalConnectorsPage /></QueryClientProvider>
      </WorkspaceSlugProvider>
    </I18nProvider>,
  );
}

async function findApp(name: string) {
  return screen.findByRole("article", { name });
}

describe("InternalConnectorsPage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.list.mockResolvedValue([connector]);
    mocks.update.mockResolvedValue(undefined);
    mocks.catalog.mockResolvedValue([]);
  });

  it("keeps the workspace page focused on management and enables from the card", async () => {
    const user = userEvent.setup();
    renderPage();
    expect(await screen.findByText("Knowledge")).toBeInTheDocument();
    expect(screen.queryByText("Available connectors")).not.toBeInTheDocument();
    expect(screen.queryByText("Copy question and open chat")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Enable connector" }));
    await waitFor(() => expect(mocks.update).toHaveBeenCalledWith("workspace-1", connector.id, expect.objectContaining({ enabled: true })));
    // Custom connectors never send the catalog-only field.
    expect(mocks.update.mock.calls[0]![2]).not.toHaveProperty("write_enabled");
  });

  it("blocks enabling a connector without a workspace credential unless groups and people may bring their own", async () => {
    const user = userEvent.setup();
    mocks.list.mockResolvedValue([{ ...connector, credentialReady: false, credentialOptional: false }]);
    const { unmount } = renderPage();
    await screen.findByText("Knowledge");
    expect(screen.getByText("Credential needed")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Enable connector" })).toBeDisabled();
    unmount();

    mocks.list.mockResolvedValue([{ ...connector, credentialReady: false, credentialOptional: true }]);
    renderPage();
    await screen.findByText("Knowledge");
    expect(screen.getByText("Per-person or per-group credentials")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Enable connector" }));
    await waitFor(() => expect(mocks.update).toHaveBeenCalledWith("workspace-1", connector.id, expect.objectContaining({ enabled: true })));
  });

  it("uses explicit state actions inside management instead of an enable checkbox", async () => {
    const user = userEvent.setup();
    renderPage();
    await screen.findByText("Knowledge");
    await user.click(screen.getByRole("button", { name: "Manage" }));
    expect(screen.getByText("Availability")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Keep disabled" })).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: "Enable connector" })).toHaveLength(2);
    expect(screen.queryByRole("checkbox", { name: "Enable connector" })).not.toBeInTheDocument();
  });
});

describe("InternalConnectorsPage official apps", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.update.mockResolvedValue(undefined);
  });

  afterEach(() => {
    delete (window as unknown as { desktopAPI?: unknown }).desktopAPI;
    configStore.getState().setDaemonConfig({});
    window.history.replaceState({}, "", "/");
  });

  it("lists official apps above custom connectors and adds one to the workspace", async () => {
    mocks.list.mockResolvedValue([connector]);
    mocks.catalog.mockResolvedValue([githubApp]);
    mocks.addCatalog.mockResolvedValue({ ...githubConnector, discoveredToolCount: 0, allowedTools: [], credentialReady: false, credentialAccount: "" });
    const user = userEvent.setup();
    renderPage();

    const card = await findApp("GitHub");
    expect(within(card).getByText(catalogCopy.status_not_added)).toBeInTheDocument();
    expect(within(card).getByText(catalogCopy.apps.github)).toBeInTheDocument();
    expect(card.querySelector('[data-connector-mark="github"]')).not.toBeNull();
    // The custom connector keeps its own section below the gallery.
    const headings = screen.getAllByRole("heading", { level: 2 }).map((heading) => heading.textContent);
    expect(headings).toEqual([catalogCopy.title, enAgents.internal_mcp.managed]);
    expect(screen.getByText("Knowledge")).toBeInTheDocument();

    await user.click(within(card).getByRole("button", { name: catalogCopy.add }));
    await waitFor(() => expect(mocks.addCatalog).toHaveBeenCalledWith("workspace-1", "github"));
  });

  it("claims the catalog connector for its card and shows tools and the shared account", async () => {
    mocks.list.mockResolvedValue([connector, githubConnector]);
    mocks.catalog.mockResolvedValue([{ ...githubApp, connectorId: githubConnector.id }]);
    renderPage();

    const card = await findApp("GitHub");
    expect(within(card).getByText("3 tools · Connected @octocat")).toBeInTheDocument();
    expect(within(card).getByRole("button", { name: catalogCopy.reconnect_shared })).toBeInTheDocument();
    expect(within(card).getByRole("link", { name: catalogCopy.install_link })).toHaveAttribute("href", githubApp.installUrl);
    // Not listed a second time among custom connectors.
    expect(screen.getAllByRole("heading", { name: "GitHub" })).toHaveLength(1);
  });

  it("lets tools be refreshed without a shared account and explains when nobody is connected", async () => {
    mocks.list.mockResolvedValue([{ ...githubConnector, allowedTools: [], discoveredToolCount: 0, credentialReady: false, credentialAccount: "" }]);
    mocks.catalog.mockResolvedValue([{ ...githubApp, connectorId: githubConnector.id }]);
    // The server refreshes with any connected account (shared, group or
    // person) and answers no_connected_account when there is none.
    mocks.refreshTools.mockRejectedValue(
      Object.assign(new Error("connect an account before refreshing tools"), {
        body: { error: "connect an account before refreshing tools", code: "no_connected_account" },
      }),
    );
    const user = userEvent.setup();
    renderPage();

    const card = await findApp("GitHub");
    expect(within(card).getByText(catalogCopy.status_pending)).toBeInTheDocument();
    expect(within(card).getByRole("button", { name: catalogCopy.connect_shared })).toBeEnabled();
    const refreshButton = within(card).getByRole("button", { name: catalogCopy.refresh_tools });
    expect(refreshButton).toBeEnabled();
    await user.click(refreshButton);

    await waitFor(() => expect(mocks.refreshTools).toHaveBeenCalledWith("workspace-1", githubConnector.id));
    expect(await within(card).findByRole("alert")).toHaveTextContent(catalogCopy.refresh_no_account);
  });

  it("hides the shared sign-in when the server cannot run the OAuth flow", async () => {
    mocks.list.mockResolvedValue([githubConnector]);
    mocks.catalog.mockResolvedValue([{ ...githubApp, connectorId: githubConnector.id, oauthAvailable: false }]);
    renderPage();

    const card = await findApp("GitHub");
    expect(within(card).queryByRole("button", { name: catalogCopy.reconnect_shared })).not.toBeInTheDocument();
    expect(within(card).getByRole("button", { name: catalogCopy.use_pat })).toBeInTheDocument();
  });

  it("toggles write actions with the full connector body", async () => {
    mocks.list.mockResolvedValue([githubConnector]);
    mocks.catalog.mockResolvedValue([{ ...githubApp, connectorId: githubConnector.id }]);
    const user = userEvent.setup();
    renderPage();

    const card = await findApp("GitHub");
    await user.click(within(card).getByRole("switch", { name: catalogCopy.write_label }));

    await waitFor(() =>
      expect(mocks.update).toHaveBeenCalledWith("workspace-1", githubConnector.id, {
        name: "GitHub",
        upstream_url: githubConnector.upstreamUrl,
        allowed_tools: githubConnector.allowedTools,
        agent_ids: [],
        enabled: true,
        auth_mode: "oauth",
        write_enabled: true,
      }),
    );
  });

  it("saves a shared Personal Access Token write-only", async () => {
    mocks.list.mockResolvedValue([githubConnector]);
    mocks.catalog.mockResolvedValue([{ ...githubApp, connectorId: githubConnector.id }]);
    mocks.setCredential.mockResolvedValue(undefined);
    const user = userEvent.setup();
    renderPage();

    const card = await findApp("GitHub");
    await user.click(within(card).getByRole("button", { name: catalogCopy.use_pat }));
    const input = within(card).getByLabelText("Personal Access Token for GitHub");
    expect(input).toHaveAttribute("type", "password");
    await user.type(input, " ghp_example ");
    await user.click(within(card).getByRole("button", { name: catalogCopy.save }));

    await waitFor(() => expect(mocks.setCredential).toHaveBeenCalledWith("workspace-1", githubConnector.id, "ghp_example"));
    expect(await within(card).findByText(catalogCopy.pat_saved)).toBeInTheDocument();
    expect(within(card).queryByLabelText("Personal Access Token for GitHub")).not.toBeInTheDocument();
  });

  it("refreshes tools and reports what was pinned", async () => {
    mocks.list.mockResolvedValue([githubConnector]);
    mocks.catalog.mockResolvedValue([{ ...githubApp, connectorId: githubConnector.id }]);
    mocks.refreshTools.mockResolvedValue({ discovered: 48, allowedTools: ["get_me", "search_code"] });
    const user = userEvent.setup();
    renderPage();

    const card = await findApp("GitHub");
    await user.click(within(card).getByRole("button", { name: catalogCopy.refresh_tools }));

    await waitFor(() => expect(mocks.refreshTools).toHaveBeenCalledWith("workspace-1", githubConnector.id));
    expect(await within(card).findByText("Tools found: 48 · available to agents: 2")).toBeInTheDocument();
  });

  it("sends a desktop admin to the web connectors page instead of starting the sign-in", async () => {
    const openExternal = vi.fn();
    (window as unknown as { desktopAPI: unknown }).desktopAPI = { pickDirectory: vi.fn(), openExternal };
    configStore.getState().setDaemonConfig({ daemonAppUrl: "https://app.example/" });
    mocks.list.mockResolvedValue([githubConnector]);
    mocks.catalog.mockResolvedValue([{ ...githubApp, connectorId: githubConnector.id }]);
    const user = userEvent.setup();
    renderPage();

    const card = await findApp("GitHub");
    await user.click(within(card).getByRole("button", { name: catalogCopy.reconnect_shared }));

    // A start would bind the sign-in to the app's own cookie jar, so the
    // desktop never calls it; the admin connects on the web page.
    await waitFor(() => expect(openExternal).toHaveBeenCalledWith("https://app.example/acme/internal-connectors"));
    expect(mocks.startOAuth).not.toHaveBeenCalled();
    expect(within(card).getByRole("status")).toHaveTextContent(catalogCopy.continue_in_browser);
  });

  it("asks a desktop admin to open the web version when the app URL is unknown", async () => {
    const openExternal = vi.fn();
    (window as unknown as { desktopAPI: unknown }).desktopAPI = { pickDirectory: vi.fn(), openExternal };
    mocks.list.mockResolvedValue([githubConnector]);
    mocks.catalog.mockResolvedValue([{ ...githubApp, connectorId: githubConnector.id }]);
    const user = userEvent.setup();
    renderPage();

    const card = await findApp("GitHub");
    await user.click(within(card).getByRole("button", { name: catalogCopy.reconnect_shared }));

    expect(await within(card).findByRole("alert")).toHaveTextContent(catalogCopy.connect_on_web);
    expect(openExternal).not.toHaveBeenCalled();
    expect(mocks.startOAuth).not.toHaveBeenCalled();
  });

  it("does not navigate when the server returns no usable authorization URL", async () => {
    mocks.list.mockResolvedValue([githubConnector]);
    mocks.catalog.mockResolvedValue([{ ...githubApp, connectorId: githubConnector.id }]);
    mocks.startOAuth.mockResolvedValue("");
    const user = userEvent.setup();
    renderPage();

    const card = await findApp("GitHub");
    await user.click(within(card).getByRole("button", { name: catalogCopy.reconnect_shared }));

    expect(await within(card).findByText("Couldn't start sign-in for GitHub.")).toBeInTheDocument();
    // The web start binds this browser, the one that will navigate.
    expect(mocks.startOAuth).toHaveBeenCalledWith("workspace-1", githubConnector.id, undefined);
    expect(within(card).getByRole("button", { name: catalogCopy.reconnect_shared })).toBeEnabled();
  });

  it("reports the provider sign-in outcome once and strips it from the address bar", async () => {
    window.history.replaceState({}, "", "/acme/internal-connectors?connected=github&tab=x");
    mocks.list.mockResolvedValue([githubConnector]);
    mocks.catalog.mockResolvedValue([{ ...githubApp, connectorId: githubConnector.id }]);
    renderPage();

    expect(await screen.findByText("GitHub connected.")).toBeInTheDocument();
    expect(window.location.pathname).toBe("/acme/internal-connectors");
    expect(window.location.search).toBe("?tab=x");
  });

  it("explains a sign-in that came back in another browser", async () => {
    window.history.replaceState({}, "", "/acme/internal-connectors?connect_error=browser_mismatch");
    mocks.list.mockResolvedValue([]);
    mocks.catalog.mockResolvedValue([githubApp]);
    renderPage();

    expect(await screen.findByText(catalogCopy.returned_browser_mismatch)).toBeInTheDocument();
  });

  it("explains a failed provider sign-in", async () => {
    window.history.replaceState({}, "", "/acme/internal-connectors?connect_error=exchange_failed");
    mocks.list.mockResolvedValue([]);
    mocks.catalog.mockResolvedValue([githubApp]);
    renderPage();

    expect(await screen.findByText(catalogCopy.returned_error)).toBeInTheDocument();
    expect(window.location.search).toBe("");
  });

  it("keeps catalog connectors manageable when the catalog cannot load", async () => {
    mocks.list.mockResolvedValue([githubConnector]);
    mocks.catalog.mockRejectedValue(new Error("boom"));
    renderPage();

    expect(await screen.findByText(catalogCopy.load_failed)).toBeInTheDocument();
    expect(await screen.findByRole("heading", { name: "GitHub" })).toBeInTheDocument();
  });
});
