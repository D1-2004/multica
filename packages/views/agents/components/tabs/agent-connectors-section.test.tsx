// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { Agent } from "@multica/core/types";
import { I18nProvider } from "@multica/core/i18n/react";
import { configStore } from "@multica/core/config";
import { WorkspaceSlugProvider } from "@multica/core/paths";
import enCommon from "../../../locales/en/common.json";
import enAgents from "../../../locales/en/agents.json";
import { NavigationProvider, type NavigationAdapter } from "../../../navigation";
import { AgentConnectorsSection } from "./agent-connectors-section";

const mocks = vi.hoisted(() => ({
  role: "owner" as string,
  list: vi.fn(),
  available: vi.fn(),
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
  memberListOptions: () => ({
    queryKey: ["members", "workspace-1"],
    queryFn: async () => [{ user_id: "user-1", role: mocks.role }],
  }),
}));
vi.mock("@multica/core/api", () => ({
  api: {
    listInternalConnectors: mocks.list,
    listAvailableInternalConnectors: mocks.available,
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
vi.mock("sonner", () => ({ toast: { error: vi.fn(), success: vi.fn() } }));

const copy = enAgents.tab_body.connectors;
const catalogCopy = enAgents.internal_mcp.catalog;

const agent = { id: "agent-1", name: "Helper" } as Agent;

const aone = {
  id: "11111111-1111-4111-8111-111111111111",
  workspaceId: "workspace-1",
  name: "Knowledge",
  upstreamUrl: "https://faas.example/mcp",
  credentialRef: "REF",
  credentialReady: true,
  credentialSource: "workspace",
  credentialOptional: false,
  authMode: "bearer",
  allowedTools: ["read_knowledge"],
  agentIds: ["agent-1"],
  enabled: true,
  catalogSlug: "",
  writeEnabled: false,
  discoveredToolCount: 0,
  credentialAccount: "",
};

const otherAone = { ...aone, id: "22222222-2222-4222-8222-222222222222", name: "Tickets", agentIds: [] };

const github = {
  ...aone,
  id: "33333333-3333-4333-8333-333333333333",
  name: "GitHub",
  upstreamUrl: "https://api.githubcopilot.com/mcp/",
  authMode: "oauth",
  credentialOptional: true,
  agentIds: ["agent-1"],
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
  connectorId: github.id as string | null,
  installUrl: "https://github.com/apps/multica/installations/new",
};

const notionApp = {
  ...githubApp,
  slug: "notion",
  name: "Notion",
  authKind: "oauth_dcr",
  allowsPat: false,
  connectorId: null as string | null,
  installUrl: "",
};

function renderSection(search = "view=mcp_config") {
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
            <AgentConnectorsSection agent={agent} />
          </QueryClientProvider>
        </NavigationProvider>
      </WorkspaceSlugProvider>
    </I18nProvider>,
  );
  return { navigation };
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.role = "owner";
  mocks.list.mockResolvedValue([aone, otherAone, github]);
  mocks.catalog.mockResolvedValue([githubApp, notionApp]);
  mocks.update.mockResolvedValue(undefined);
  mocks.available.mockResolvedValue([]);
});

afterEach(() => {
  delete (window as unknown as { desktopAPI?: unknown }).desktopAPI;
  configStore.getState().setDaemonConfig({});
});

describe("AgentConnectorsSection for workspace admins", () => {
  it("lists only the connectors enabled for this agent, official apps with their controls", async () => {
    renderSection();

    const card = await screen.findByRole("article", { name: "GitHub" });
    expect(within(card).getByText("3 tools · Connected @octocat")).toBeInTheDocument();
    expect(within(card).getByRole("button", { name: catalogCopy.reconnect_shared })).toBeInTheDocument();
    expect(within(card).getByRole("switch", { name: catalogCopy.write_label })).toBeInTheDocument();
    expect(within(card).getByRole("link", { name: catalogCopy.install_link })).toHaveAttribute("href", githubApp.installUrl);

    const knowledge = screen.getByRole("article", { name: "Knowledge" });
    expect(within(knowledge).getByText(copy.kind_aone)).toBeInTheDocument();
    // A library connector granted to other agents only is not listed.
    expect(screen.queryByRole("article", { name: "Tickets" })).not.toBeInTheDocument();
  });

  it("starts the shared-account sign-in with a return to this agent's connector tab", async () => {
    mocks.startOAuth.mockResolvedValue("");
    const user = userEvent.setup();
    renderSection();

    const card = await screen.findByRole("article", { name: "GitHub" });
    await user.click(within(card).getByRole("button", { name: catalogCopy.reconnect_shared }));

    await waitFor(() =>
      expect(mocks.startOAuth).toHaveBeenCalledWith("workspace-1", github.id, "/acme/agents/agent-1?view=mcp_config"),
    );
    expect(await within(card).findByText("Couldn't start sign-in for GitHub.")).toBeInTheDocument();
  });

  it("sends a desktop admin to this agent page on the web instead of starting the sign-in", async () => {
    const openExternal = vi.fn();
    (window as unknown as { desktopAPI: unknown }).desktopAPI = { pickDirectory: vi.fn(), openExternal };
    configStore.getState().setDaemonConfig({ daemonAppUrl: "https://app.example/" });
    const user = userEvent.setup();
    renderSection();

    const card = await screen.findByRole("article", { name: "GitHub" });
    await user.click(within(card).getByRole("button", { name: catalogCopy.reconnect_shared }));

    await waitFor(() =>
      expect(openExternal).toHaveBeenCalledWith("https://app.example/acme/agents/agent-1?view=mcp_config"),
    );
    expect(mocks.startOAuth).not.toHaveBeenCalled();
  });

  it("toggles write actions with the full connector body", async () => {
    const user = userEvent.setup();
    renderSection();

    const card = await screen.findByRole("article", { name: "GitHub" });
    await user.click(within(card).getByRole("switch", { name: catalogCopy.write_label }));

    await waitFor(() =>
      expect(mocks.update).toHaveBeenCalledWith("workspace-1", github.id, {
        name: "GitHub",
        upstream_url: github.upstreamUrl,
        allowed_tools: github.allowedTools,
        agent_ids: ["agent-1"],
        enabled: true,
        auth_mode: "oauth",
        write_enabled: true,
      }),
    );
  });

  it("explains a refresh when nobody has connected an account", async () => {
    mocks.refreshTools.mockRejectedValue(
      Object.assign(new Error("connect an account"), { body: { code: "no_connected_account" } }),
    );
    const user = userEvent.setup();
    renderSection();

    const card = await screen.findByRole("article", { name: "GitHub" });
    await user.click(within(card).getByRole("button", { name: catalogCopy.refresh_tools }));

    expect(await within(card).findByRole("alert")).toHaveTextContent(catalogCopy.refresh_no_account);
  });

  it("saves a shared Personal Access Token write-only", async () => {
    mocks.setCredential.mockResolvedValue(undefined);
    const user = userEvent.setup();
    renderSection();

    const card = await screen.findByRole("article", { name: "GitHub" });
    await user.click(within(card).getByRole("button", { name: catalogCopy.use_pat }));
    const input = within(card).getByLabelText("Personal Access Token for GitHub");
    expect(input).toHaveAttribute("type", "password");
    await user.type(input, " ghp_example ");
    await user.click(within(card).getByRole("button", { name: catalogCopy.save }));

    await waitFor(() => expect(mocks.setCredential).toHaveBeenCalledWith("workspace-1", github.id, "ghp_example"));
  });

  it("removes a connector from this agent only after confirmation", async () => {
    const user = userEvent.setup();
    renderSection();

    await screen.findByRole("article", { name: "Knowledge" });
    await user.click(screen.getByRole("button", { name: "Remove Knowledge from this agent" }));
    expect(mocks.update).not.toHaveBeenCalled();
    const dialog = await screen.findByRole("alertdialog");
    await user.click(within(dialog).getByRole("button", { name: copy.remove }));

    await waitFor(() =>
      expect(mocks.update).toHaveBeenCalledWith(
        "workspace-1",
        aone.id,
        expect.objectContaining({ agent_ids: [] }),
      ),
    );
  });

  it("adds an official app from the gallery and grants it to this agent", async () => {
    const notion = { ...github, id: "44444444-4444-4444-8444-444444444444", name: "Notion", catalogSlug: "notion", agentIds: [] };
    mocks.addCatalog.mockResolvedValue(notion);
    const user = userEvent.setup();
    renderSection();

    await screen.findByRole("article", { name: "GitHub" });
    await user.click(screen.getByRole("button", { name: copy.add }));
    const dialog = await screen.findByRole("dialog");
    // Already enabled apps are marked instead of offered again.
    expect(within(dialog).getByText(copy.dialog_added)).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "Add Notion" }));

    await waitFor(() => expect(mocks.addCatalog).toHaveBeenCalledWith("workspace-1", "notion"));
    await waitFor(() =>
      expect(mocks.update).toHaveBeenCalledWith(
        "workspace-1",
        notion.id,
        expect.objectContaining({ agent_ids: ["agent-1"] }),
      ),
    );
  });

  it("grants an Aone FaaS connector from the workspace library", async () => {
    const user = userEvent.setup();
    renderSection();

    await screen.findByRole("article", { name: "Knowledge" });
    await user.click(screen.getByRole("button", { name: copy.add }));
    const dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByRole("tab", { name: copy.source_aone }));
    // Already granted connectors are not offered again.
    expect(within(dialog).queryByText("Knowledge")).not.toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "Add Tickets" }));

    await waitFor(() =>
      expect(mocks.update).toHaveBeenCalledWith(
        "workspace-1",
        otherAone.id,
        expect.objectContaining({ agent_ids: ["agent-1"] }),
      ),
    );
  });

  it("reports the provider sign-in outcome once and strips it from the address", async () => {
    const { navigation } = renderSection("view=mcp_config&connected=github");

    expect(await screen.findByText("GitHub connected.")).toBeInTheDocument();
    expect(navigation.replace).toHaveBeenCalledWith("/acme/agents/agent-1?view=mcp_config");
  });

  it("explains a sign-in that came back in another browser", async () => {
    renderSection("view=mcp_config&connect_error=browser_mismatch");

    expect(await screen.findByText(catalogCopy.returned_browser_mismatch)).toBeInTheDocument();
  });
});

describe("AgentConnectorsSection for members", () => {
  it("shows the assigned connectors read-only without the admin library", async () => {
    mocks.role = "member";
    mocks.available.mockResolvedValue([
      {
        id: aone.id,
        name: "Knowledge",
        serverName: "c1111111111114111",
        agentId: "agent-1",
        agentName: "Helper",
        tools: ["read_knowledge"],
      },
      {
        id: otherAone.id,
        name: "Tickets",
        serverName: "c2222222222224222",
        agentId: "agent-2",
        agentName: "Other",
        tools: [],
      },
    ]);
    renderSection();

    expect(await screen.findByText("Knowledge")).toBeInTheDocument();
    expect(screen.getByText("1 available tools")).toBeInTheDocument();
    expect(screen.queryByText("Tickets")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: copy.add })).not.toBeInTheDocument();
    expect(screen.getByText(copy.admin_only)).toBeInTheDocument();
    expect(mocks.list).not.toHaveBeenCalled();
    expect(mocks.catalog).not.toHaveBeenCalled();
  });
});
