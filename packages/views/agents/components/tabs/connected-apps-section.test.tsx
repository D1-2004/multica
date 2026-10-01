// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { Agent } from "@multica/core/types";
import type {
  AgentContextCapabilities,
  ConnectedApp,
  ConnectedAppDetail,
} from "@multica/core/context-capabilities";
import { I18nProvider } from "@multica/core/i18n/react";
import { WorkspaceSlugProvider } from "@multica/core/paths";
import enCommon from "../../../locales/en/common.json";
import enAgents from "../../../locales/en/agents.json";
import { toast } from "sonner";
import { NavigationProvider, type NavigationAdapter } from "../../../navigation";
import { ConnectedAppsSection } from "./connected-apps-section";

const mocks = vi.hoisted(() => ({
  apps: vi.fn(),
  app: vi.fn(),
  getCaps: vi.fn(),
  setOffers: vi.fn(),
  list: vi.fn(),
  update: vi.fn(),
  addCatalog: vi.fn(),
  startOAuth: vi.fn(),
  deleteCredential: vi.fn(),
  setCredential: vi.fn(),
  refreshTools: vi.fn(),
}));

vi.mock("@multica/core/api", async () => {
  const actual = await vi.importActual<typeof import("@multica/core/api")>("@multica/core/api");
  return {
    ...actual,
    api: {
      listAgentConnectedApps: mocks.apps,
      getAgentConnectedApp: mocks.app,
      getAgentContextCapabilities: mocks.getCaps,
      setAgentContextCapabilityOffers: mocks.setOffers,
      listInternalConnectors: mocks.list,
      updateInternalConnector: mocks.update,
      addCatalogConnector: mocks.addCatalog,
      startInternalConnectorOAuth: mocks.startOAuth,
      deleteInternalConnectorCredential: mocks.deleteCredential,
      setInternalConnectorCredential: mocks.setCredential,
      refreshInternalConnectorTools: mocks.refreshTools,
    },
  };
});
vi.mock("sonner", () => ({ toast: { error: vi.fn(), success: vi.fn(), info: vi.fn() } }));

const copy = enAgents.tab_body.connected_apps;
const agent = { id: "agent-1", name: "Helper" } as Agent;
const GITHUB_ID = "33333333-3333-4333-8333-333333333333";

const noUsage = { scenesEnabled: 0, scenesConnected: 0, personsEnabled: 0, personsConnected: 0 };

function app(overrides: Partial<ConnectedApp>): ConnectedApp {
  return {
    slug: "notion",
    name: "Notion",
    oauthAvailable: true,
    allowsPat: false,
    installUrl: "",
    connectorId: null,
    added: false,
    enabledInWorkspace: false,
    globalEnabled: false,
    offered: false,
    writeEnabled: false,
    tools: { discovered: 0, allowed: 0 },
    sharedAccount: { connected: false, account: "", source: "" },
    usage: noUsage,
    ...overrides,
  };
}

const github = app({
  slug: "github",
  name: "GitHub",
  oauthAvailable: false,
  allowsPat: true,
  installUrl: "https://github.com/apps/multica/installations/new",
  connectorId: GITHUB_ID,
  added: true,
  enabledInWorkspace: true,
  globalEnabled: true,
  offered: true,
  tools: { discovered: 48, allowed: 20 },
  sharedAccount: { connected: true, account: "@octocat", source: "workspace" },
  usage: { scenesEnabled: 2, scenesConnected: 1, personsEnabled: 1, personsConnected: 3 },
});
const notion = app({});
const sentry = app({
  slug: "sentry",
  name: "Sentry",
  connectorId: "44444444-4444-4444-8444-444444444444",
  added: true,
  enabledInWorkspace: true,
  globalEnabled: true,
});
const figma = app({
  slug: "figma",
  name: "Figma",
  connectorId: "55555555-5555-4555-8555-555555555555",
  added: true,
  enabledInWorkspace: false,
  offered: true,
});

const githubDetail: ConnectedAppDetail = {
  ...github,
  scenes: [
    { sceneKey: "cidGroup==", title: "Release crew", kind: "group", enabled: true, connected: false, account: "" },
    { sceneKey: "cidDm==", title: "", kind: "dm", enabled: false, connected: true, account: "@team" },
  ],
  persons: [{ scopeKey: "staff-1", title: "Ada", enabled: true, connected: true, account: "@ada", shareInGroups: true }],
  toolList: [
    { name: "search_code", readOnly: true, allowed: true },
    { name: "create_issue", readOnly: false, allowed: false },
  ],
  canAdmin: true,
};

const libraryGithub = {
  id: GITHUB_ID,
  workspaceId: "ws-1",
  name: "GitHub",
  upstreamUrl: "https://api.githubcopilot.com/mcp/",
  credentialRef: "REF",
  credentialReady: true,
  credentialSource: "workspace",
  credentialOptional: true,
  authMode: "oauth",
  allowedTools: ["search_code"],
  agentIds: ["agent-other", "agent-1"],
  enabled: true,
  catalogSlug: "github",
  writeEnabled: false,
  discoveredToolCount: 48,
  credentialAccount: "@octocat",
};

function caps(connectorIds: string[]): AgentContextCapabilities {
  return {
    enabled: true,
    library: { connectors: [], skills: [] },
    offers: { connectorIds, skillIds: ["skill-1"] },
    orgs: [],
    scenes: [],
    persons: [],
    configureUrl: "https://app.example/dingtalk/configure?agent=agent-1",
  };
}

function renderSection({ search = "view=mcp_config", canEdit = true }: { search?: string; canEdit?: boolean } = {}) {
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
            <ConnectedAppsSection agent={agent} wsId="ws-1" canEdit={canEdit} />
          </QueryClientProvider>
        </NavigationProvider>
      </WorkspaceSlugProvider>
    </I18nProvider>,
  );
  return { navigation };
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.apps.mockResolvedValue({ apps: [github, notion, sentry, figma], canAdmin: true });
  mocks.app.mockImplementation(async (_ws: string, _agent: string, slug: string) =>
    slug === "github" ? githubDetail : { ...notion, scenes: [], persons: [], toolList: [], canAdmin: true },
  );
  mocks.getCaps.mockResolvedValue(caps([GITHUB_ID, "other-offer"]));
  mocks.setOffers.mockImplementation(async (_ws: string, _agent: string, input: { connectorIds: string[] }) =>
    caps(input.connectorIds),
  );
  mocks.list.mockResolvedValue([libraryGithub]);
  mocks.update.mockResolvedValue(undefined);
  mocks.deleteCredential.mockResolvedValue(undefined);
});

describe("connected apps gallery", () => {
  it("shows every app as a compact tile with a status built from the connected-apps response", async () => {
    renderSection();

    const gh = await screen.findByRole("button", { name: "Configure GitHub" });
    expect(within(gh).getByText(copy.status_everyone)).toBeInTheDocument();
    expect(
      within(screen.getByRole("button", { name: "Configure Notion" })).getByText(copy.status_not_added),
    ).toBeInTheDocument();
    expect(
      within(screen.getByRole("button", { name: "Configure Sentry" })).getByText(copy.status_everyone_no_account),
    ).toBeInTheDocument();
    expect(
      within(screen.getByRole("button", { name: "Configure Figma" })).getByText(
        enAgents.tab_body.connectors.disabled_in_workspace,
      ),
    ).toBeInTheDocument();
    // Tiles carry only logo, name and status: no descriptions or counts.
    expect(gh.textContent).toBe(`GitHub${copy.status_everyone}`);
    // The configure page is a label and its URL.
    expect(await screen.findByDisplayValue("https://app.example/dingtalk/configure?agent=agent-1")).toBeInTheDocument();
    expect(screen.getByText(enAgents.tab_body.context_offers.configure_title)).toBeInTheDocument();
  });

  it("opens an app's dialog through the address", async () => {
    const user = userEvent.setup();
    const { navigation } = renderSection();

    await user.click(await screen.findByRole("button", { name: "Configure GitHub" }));

    expect(navigation.replace).toHaveBeenCalledWith("/acme/agents/agent-1?view=mcp_config&app=github");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("closing the dialog removes the app from the address", async () => {
    const user = userEvent.setup();
    const { navigation } = renderSection({ search: "view=mcp_config&app=github" });

    const dialog = await screen.findByRole("dialog");
    await within(dialog).findByRole("region", { name: copy.section_shared });
    expect(within(dialog).getByRole("heading", { name: "GitHub" })).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "Close" }));

    expect(navigation.replace).toHaveBeenLastCalledWith("/acme/agents/agent-1?view=mcp_config");
  });

  it("does not load apps for people who cannot manage the agent", () => {
    renderSection({ canEdit: false, search: "view=mcp_config&app=github" });

    expect(screen.getByText(copy.viewer_only)).toBeInTheDocument();
    expect(mocks.apps).not.toHaveBeenCalled();
    expect(mocks.app).not.toHaveBeenCalled();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("shows a load error instead of an empty gallery for a malformed response", async () => {
    mocks.apps.mockResolvedValue(null);
    renderSection();

    expect(await screen.findByText(copy.load_failed)).toBeInTheDocument();
  });
});

describe("app dialog", () => {
  const openGithub = () => renderSection({ search: "view=mcp_config&app=github" });

  it("says why the shared account uses a token and links the app installation", async () => {
    openGithub();

    const shared = await screen.findByRole("region", { name: copy.section_shared });
    expect(within(shared).getByText(copy.auth_pat_only.replace("{{name}}", "GitHub"))).toBeInTheDocument();
    expect(within(shared).getByRole("link", { name: enAgents.internal_mcp.catalog.install_link })).toHaveAttribute(
      "href",
      github.installUrl,
    );
    // The header carries the status next to the name.
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByText(copy.status_everyone, { selector: "span" })).toBeInTheDocument();
  });

  it("never offers an OAuth connect the server cannot run; a token replaces the shared account", async () => {
    const user = userEvent.setup();
    mocks.setCredential.mockResolvedValue(undefined);
    openGithub();

    const shared = await screen.findByRole("region", { name: copy.section_shared });
    expect(within(shared).getByText("Connected @octocat")).toBeInTheDocument();
    expect(within(shared).queryByRole("button", { name: copy.connect })).not.toBeInTheDocument();
    expect(within(shared).queryByRole("button", { name: copy.reconnect })).not.toBeInTheDocument();
    await user.click(within(shared).getByRole("button", { name: copy.replace_pat }));
    await user.type(within(shared).getByLabelText("Personal Access Token for GitHub"), "ghp_token");
    await user.click(within(shared).getByRole("button", { name: enAgents.internal_mcp.catalog.save }));

    await waitFor(() => expect(mocks.setCredential).toHaveBeenCalled());
    expect(JSON.stringify(mocks.setCredential.mock.calls[0])).toContain("ghp_token");
  });

  it("disconnects the shared account after confirmation", async () => {
    const user = userEvent.setup();
    openGithub();

    const shared = await screen.findByRole("region", { name: copy.section_shared });
    await user.click(within(shared).getByRole("button", { name: copy.disconnect }));
    const dialog = await screen.findByRole("alertdialog");
    expect(within(dialog).getByText(/On for everyone stops working for GitHub on every agent/)).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: copy.disconnect }));

    await waitFor(() => expect(mocks.deleteCredential).toHaveBeenCalledWith("ws-1", GITHUB_ID));
  });

  it("turns off 对所有用户启用 by revoking only this agent's grant", async () => {
    const user = userEvent.setup();
    openGithub();

    await user.click(await screen.findByRole("switch", { name: copy.global_label }));

    await waitFor(() =>
      expect(mocks.update).toHaveBeenCalledWith(
        "ws-1",
        GITHUB_ID,
        expect.objectContaining({ agent_ids: ["agent-other"], write_enabled: false }),
      ),
    );
  });

  it("cannot turn on 对所有用户启用 without a working shared account", async () => {
    mocks.app.mockResolvedValue({
      ...githubDetail,
      globalEnabled: false,
      sharedAccount: { connected: false, account: "", source: "" },
    });
    openGithub();

    const toggle = await screen.findByRole("switch", { name: copy.global_label });
    expect(toggle).toHaveAttribute("aria-disabled", "true");
    expect(screen.getByText(copy.global_needs_account)).toBeInTheDocument();
  });

  it("lists group, 1:1 and personal use with 已开启 and 已连接 reported separately", async () => {
    openGithub();

    const scoped = await screen.findByRole("region", { name: copy.section_scoped });
    const group = within(scoped).getByText("Release crew").closest("li")!;
    expect(within(group).getByText(copy.usage_enabled)).toBeInTheDocument();
    expect(within(group).getByText(copy.shared_none)).toBeInTheDocument();
    const dm = within(scoped).getByText(enAgents.context_config.scene_untitled_dm).closest("li")!;
    expect(within(dm).getByText(copy.usage_not_enabled)).toBeInTheDocument();
    expect(within(dm).getByText("Connected @team")).toBeInTheDocument();
    const person = within(scoped).getByText("Ada").closest("li")!;
    expect(within(person).getByText("Connected @ada")).toBeInTheDocument();
    expect(within(person).getByText(copy.usage_share_in_groups)).toBeInTheDocument();
  });

  it("shows one line instead of empty usage lists", async () => {
    mocks.app.mockResolvedValue({ ...githubDetail, scenes: [], persons: [] });
    openGithub();

    const scoped = await screen.findByRole("region", { name: copy.section_scoped });
    expect(within(scoped).getByText(copy.usage_none)).toBeInTheDocument();
    expect(within(scoped).queryByText(copy.scenes_title)).not.toBeInTheDocument();
    expect(within(scoped).queryByText(copy.persons_title)).not.toBeInTheDocument();
  });

  it("asks before taking away the offer while groups or people use it", async () => {
    const user = userEvent.setup();
    openGithub();

    await user.click(await screen.findByRole("switch", { name: copy.offer_label }));
    expect(mocks.setOffers).not.toHaveBeenCalled();
    const dialog = await screen.findByRole("alertdialog");
    expect(
      within(dialog).getByText("It turns off right away in 2 scenes and for 1 people. Their connected accounts stay stored."),
    ).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: enAgents.tab_body.connectors.offer_off_confirm }));

    await waitFor(() =>
      expect(mocks.setOffers).toHaveBeenCalledWith("ws-1", "agent-1", {
        connectorIds: ["other-offer"],
        skillIds: ["skill-1"],
      }),
    );
  });

  it("lists tools with read-only badges, says they are workspace-wide and toggles write access", async () => {
    const user = userEvent.setup();
    openGithub();

    const tools = await screen.findByRole("region", { name: copy.section_tools });
    expect(within(tools).getByText("search_code")).toBeInTheDocument();
    expect(within(tools).getAllByText(copy.read_only)).toHaveLength(1);
    expect(within(tools).getByText(copy.tools_scope_hint)).toBeInTheDocument();
    await user.click(within(tools).getByRole("switch", { name: enAgents.internal_mcp.catalog.write_label }));

    await waitFor(() =>
      expect(mocks.update).toHaveBeenCalledWith("ws-1", GITHUB_ID, expect.objectContaining({ write_enabled: true })),
    );
  });

  it("removes the app from the agent: grant and offer", async () => {
    const user = userEvent.setup();
    openGithub();

    await user.click(await screen.findByRole("button", { name: copy.remove_action }));
    const dialog = await screen.findByRole("alertdialog");
    await user.click(within(dialog).getByRole("button", { name: copy.remove_action }));

    await waitFor(() =>
      expect(mocks.update).toHaveBeenCalledWith("ws-1", GITHUB_ID, expect.objectContaining({ agent_ids: ["agent-other"] })),
    );
    await waitFor(() =>
      expect(mocks.setOffers).toHaveBeenCalledWith("ws-1", "agent-1", {
        connectorIds: ["other-offer"],
        skillIds: ["skill-1"],
      }),
    );
  });

  it("adds an app that is not in the workspace yet: connector plus offer, never a grant", async () => {
    const notionId = "77777777-7777-4777-8777-777777777777";
    mocks.addCatalog.mockResolvedValue({ ...libraryGithub, id: notionId, name: "Notion", catalogSlug: "notion", agentIds: [] });
    const user = userEvent.setup();
    renderSection({ search: "view=mcp_config&app=notion" });

    const add = await screen.findByRole("button", { name: copy.add });
    expect(screen.getByText(copy.add_hint)).toBeInTheDocument();
    // Without a workspace connector there is no account to manage yet.
    expect(screen.queryByRole("region", { name: copy.section_shared })).not.toBeInTheDocument();
    await user.click(add);

    await waitFor(() => expect(mocks.addCatalog).toHaveBeenCalledWith("ws-1", "notion"));
    await waitFor(() =>
      expect(mocks.setOffers).toHaveBeenCalledWith("ws-1", "agent-1", {
        connectorIds: [GITHUB_ID, "other-offer", notionId],
        skillIds: ["skill-1"],
      }),
    );
    // On for everyone needs a working shared account first.
    expect(mocks.update).not.toHaveBeenCalled();
  });

  it("offers 添加 for an app whose workspace connector exists but this agent does not use", async () => {
    mocks.app.mockResolvedValue({
      ...githubDetail,
      added: false,
      globalEnabled: false,
      offered: false,
      usage: noUsage,
      scenes: [],
      persons: [],
    });
    openGithub();

    expect(await screen.findByRole("button", { name: copy.add })).toBeInTheDocument();
    // Nothing to remove from the agent.
    expect(screen.queryByRole("button", { name: copy.remove_action })).not.toBeInTheDocument();
  });

  it("starts the shared-account sign-in with a return to this app's dialog", async () => {
    mocks.app.mockResolvedValue({
      ...notion,
      connectorId: "66666666-6666-4666-8666-666666666666",
      added: true,
      enabledInWorkspace: true,
      scenes: [],
      persons: [],
      toolList: [],
      canAdmin: true,
    });
    mocks.startOAuth.mockResolvedValue("");
    const user = userEvent.setup();
    renderSection({ search: "view=mcp_config&app=notion" });

    const shared = await screen.findByRole("region", { name: copy.section_shared });
    await user.click(within(shared).getByRole("button", { name: copy.connect }));

    await waitFor(() =>
      expect(mocks.startOAuth).toHaveBeenCalledWith(
        "ws-1",
        "66666666-6666-4666-8666-666666666666",
        "/acme/agents/agent-1?view=mcp_config&app=notion",
      ),
    );
    await waitFor(() => expect(toast.error).toHaveBeenCalledWith("Couldn't start sign-in for Notion."));
  });

  it("keeps admin controls when only the list request fails: can_admin comes from the detail", async () => {
    mocks.apps.mockRejectedValue(new Error("list unavailable"));
    openGithub();

    const shared = await screen.findByRole("region", { name: copy.section_shared });
    expect(within(shared).getByRole("button", { name: copy.replace_pat })).toBeInTheDocument();
    expect(screen.getByRole("switch", { name: copy.global_label })).not.toHaveAttribute("aria-disabled", "true");
    expect(screen.queryByText(copy.admin_only)).not.toBeInTheDocument();
  });

  it("reads only for a caller the detail does not call an admin", async () => {
    mocks.app.mockResolvedValue({ ...githubDetail, canAdmin: false });
    openGithub();

    const shared = await screen.findByRole("region", { name: copy.section_shared });
    expect(within(shared).queryByRole("button", { name: copy.disconnect })).not.toBeInTheDocument();
    expect(screen.getByRole("switch", { name: copy.global_label })).toHaveAttribute("aria-disabled", "true");
    expect(screen.getByText(copy.admin_only)).toBeInTheDocument();
  });

  it("does not report 对所有用户启用 as working while no tool is allowed", async () => {
    // The runtime mounts an app only with at least one allowed tool; a
    // connected shared account means discovery already ran.
    const noTools = {
      ...githubDetail,
      tools: { discovered: 0, allowed: 0 },
      toolList: [],
    };
    mocks.apps.mockResolvedValue({ apps: [{ ...github, tools: noTools.tools }], canAdmin: true });
    mocks.app.mockResolvedValue(noTools);
    openGithub();

    const dialog = await screen.findByRole("dialog");
    expect(await within(dialog).findByText(copy.status_no_tools)).toBeInTheDocument();
    expect(within(dialog).queryByText(copy.tools_pending)).not.toBeInTheDocument();
    // The switch label reads the same as the green status; only the label
    // may remain.
    expect(within(dialog).getAllByText(copy.status_everyone).every((node) => node.tagName === "LABEL")).toBe(true);
    expect(within(dialog).getByText(copy.global_no_tools)).toBeInTheDocument();
    const tools = within(dialog).getByRole("region", { name: copy.section_tools });
    expect(within(tools).getByText(copy.tools_refresh_needed)).toBeInTheDocument();
  });

  it("marks a tile without allowed tools once an account is connected", async () => {
    mocks.apps.mockResolvedValue({
      apps: [{ ...github, tools: { discovered: 0, allowed: 0 } }],
      canAdmin: true,
    });
    renderSection();

    const gh = await screen.findByRole("button", { name: "Configure GitHub" });
    expect(within(gh).getByText(copy.status_no_tools)).toBeInTheDocument();
  });

  it("does not offer to disconnect a shared account that comes from the deployment environment", async () => {
    mocks.app.mockResolvedValue({
      ...githubDetail,
      sharedAccount: { connected: true, account: "", source: "environment" },
    });
    openGithub();

    const shared = await screen.findByRole("region", { name: copy.section_shared });
    expect(within(shared).getByText(copy.shared_connected)).toBeInTheDocument();
    expect(within(shared).getByText(copy.shared_env)).toBeInTheDocument();
    expect(within(shared).queryByRole("button", { name: copy.disconnect })).not.toBeInTheDocument();
  });

  it("says when the app does not exist", async () => {
    const { ApiError } = await import("@multica/core/api");
    mocks.app.mockRejectedValue(new ApiError("not found", 404, "Not Found"));
    renderSection({ search: "view=mcp_config&app=nope" });

    expect(await screen.findByText(copy.not_found)).toBeInTheDocument();
  });
});

describe("provider sign-in results", () => {
  it("reports a shared-account sign-in once as a toast and reopens that app's dialog", async () => {
    const { navigation } = renderSection({ search: "view=mcp_config&app=github&connected=github" });

    await waitFor(() => expect(toast.success).toHaveBeenCalledWith("GitHub connected."));
    expect(toast.success).toHaveBeenCalledTimes(1);
    expect(navigation.replace).toHaveBeenCalledWith("/acme/agents/agent-1?view=mcp_config&app=github");
    expect(await screen.findByRole("region", { name: copy.section_shared })).toBeInTheDocument();
  });

  it("reopens the connected app even when the return lost the app parameter", async () => {
    const { navigation } = renderSection({ search: "view=mcp_config&connected=github" });

    await waitFor(() =>
      expect(navigation.replace).toHaveBeenCalledWith("/acme/agents/agent-1?view=mcp_config&app=github"),
    );
  });

  it("explains a cancelled sign-in", async () => {
    renderSection({ search: "view=mcp_config&app=github&connect_error=access_denied" });
    await waitFor(() => expect(toast.error).toHaveBeenCalledWith(enAgents.internal_mcp.catalog.returned_denied));
  });

  it("explains a sign-in that came back in another browser", async () => {
    renderSection({ search: "view=mcp_config&connect_error=browser_mismatch" });
    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith(enAgents.internal_mcp.catalog.returned_browser_mismatch),
    );
  });
});
