// @vitest-environment jsdom

import { describe, it, expect, vi, beforeEach } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Agent, AgentRuntime } from "@multica/core/types";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../../locales/en/common.json";
import enAgents from "../../../locales/en/agents.json";

const TEST_RESOURCES = { en: { common: enCommon, agents: enAgents } };

const mockListDshPlugins = vi.hoisted(() => vi.fn());
const mockListAgentDshPlugins = vi.hoisted(() => vi.fn());
const mockGetProfile = vi.hoisted(() => vi.fn());
const mockPrepareProfile = vi.hoisted(() => vi.fn());
const mockGetHome = vi.hoisted(() => vi.fn());
const mockGetConfig = vi.hoisted(() => vi.fn());
const mockUpdateConfig = vi.hoisted(() => vi.fn());
const mockSetAgentDshPlugins = vi.hoisted(() => vi.fn());

const { ApiError } = vi.hoisted(() => {
  class ApiError extends Error {
    status: number;
    statusText: string;
    constructor(message: string, status: number, statusText: string) {
      super(message);
      this.status = status;
      this.statusText = statusText;
    }
  }
  return { ApiError };
});

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

vi.mock("@multica/core/api", () => ({
  api: {
    getDSHHome: (...args: unknown[]) => mockGetHome(...args),
    getDSHProfile: (...args: unknown[]) => mockGetProfile(...args),
    prepareDSHProfile: (...args: unknown[]) => mockPrepareProfile(...args),
    getAgentDshPluginConfig: (...args: unknown[]) => mockGetConfig(...args),
    updateAgentDshPluginConfig: (...args: unknown[]) => mockUpdateConfig(...args),
    listDshPlugins: (...args: unknown[]) => mockListDshPlugins(...args),
    listAgentDshPlugins: (...args: unknown[]) => mockListAgentDshPlugins(...args),
    setAgentDshPlugins: (...args: unknown[]) => mockSetAgentDshPlugins(...args),
  },
  ApiError,
}));

vi.mock("sonner", () => ({
  toast: { error: vi.fn(), success: vi.fn() },
}));

import { DshPluginsTab } from "./dsh-plugins-tab";

const agent: Agent = {
  id: "agent-1",
  workspace_id: "ws-1",
  runtime_id: "runtime-1",
  name: "Agent",
  description: "",
  instructions: "",
  avatar_url: null,
  runtime_mode: "cloud",
  runtime_config: {},
  custom_args: [],
  visibility: "workspace",
  permission_mode: "public_to",
  invocation_targets: [{ target_type: "workspace", target_id: null }],
  status: "idle",
  max_concurrent_tasks: 1,
  model: "",
  owner_id: "user-1",
  skills: [],
  created_at: "2026-04-16T00:00:00Z",
  updated_at: "2026-04-16T00:00:00Z",
  archived_at: null,
  archived_by: null,
};

const dshRuntime: AgentRuntime = {
  id: "runtime-1",
  workspace_id: "ws-1",
  daemon_id: "daemon-1",
  name: "DSH",
  runtime_mode: "cloud",
  provider: "dsh",
  launch_header: "",
  status: "online",
  device_info: "",
  metadata: {},
  owner_id: "user-1",
  visibility: "public",
  last_seen_at: null,
  created_at: "2026-07-11T00:00:00Z",
  updated_at: "2026-07-11T00:00:00Z",
};

function plugin(id: string, packageName: string, enabled = true) {
  return {
    id,
    workspaceId: "ws-1",
    packageName,
    displayName: packageName,
    description: `${packageName} does a thing`,
    homepage: "",
    sourceKind: "npm" as const,
    sourceSpec: `npm:${packageName}@1.0.0`,
    resolvedVersion: "1.0.0",
    integrity: `sha256-${"a".repeat(64)}`,
    bundleRows: ["a-row"],
    configRow: "",
    config: {},
    catalog: "",
    validatedDshVersion: "",
    createdBy: null,
    createdAt: "2026-04-16T00:00:00Z",
    updatedAt: "2026-04-16T00:00:00Z",
    enabled,
  };
}

function renderTab(canEdit = true, agentOverrides: Partial<Agent> = {}, runtime = dshRuntime) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const view = render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <QueryClientProvider client={queryClient}>
        <DshPluginsTab
          agent={{ ...agent, ...agentOverrides }}
          runtime={runtime}
          canEdit={canEdit}
        />
      </QueryClientProvider>
    </I18nProvider>,
  );
  return {...view, queryClient};
}

describe("DshPluginsTab", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockGetProfile.mockResolvedValue({state:"applied",current:true,desiredRevision:"1",appliedRevision:"1",builds:[]});
    mockSetAgentDshPlugins.mockResolvedValue(undefined);
  });

  it("tells the operator to import first when the workspace has no plugins", async () => {
    mockListDshPlugins.mockResolvedValue([]);
    mockListAgentDshPlugins.mockResolvedValue([]);
    renderTab();

    expect(await screen.findByText("No plugins attached")).toBeTruthy();
    // The hint has to distinguish "nothing imported" from "nothing attached",
    // or the empty state points at a button that is disabled.
    expect(
      screen.getByText(/No plugins have been imported into this workspace yet/i),
    ).toBeTruthy();
  });

  it("attaching sends the whole set, not a delta", async () => {
    mockListDshPlugins.mockResolvedValue([
      plugin("p-1", "dsh-mcp-lens"),
      plugin("p-2", "dsh-context"),
    ]);
    mockListAgentDshPlugins.mockResolvedValue([plugin("p-1", "dsh-mcp-lens")]);
    renderTab();

    await screen.findByText("dsh-mcp-lens");
    await userEvent.click(screen.getByRole("button", { name: "Attach" }));
    const dialog = await screen.findByRole("dialog");
    const row = within(dialog).getByText("dsh-context").closest("li");
    expect(row).toBeTruthy();
    await userEvent.click(
      within(row as HTMLElement).getByRole("button", { name: "Attach" }),
    );

    expect(mockSetAgentDshPlugins).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", {name:"Submit configuration changes"}));
    // The endpoint replaces the set, so the already-attached plugin must be
    // re-sent. Sending only the new one would silently detach the other.
    await waitFor(() => {
      expect(mockSetAgentDshPlugins).toHaveBeenCalledWith("agent-1", [
        { id: "p-1", enabled: true },
        { id: "p-2", enabled: true },
      ], [plugin("p-1", "dsh-mcp-lens")]);
    });
  });

  it("detaching sends the set without that plugin", async () => {
    mockListDshPlugins.mockResolvedValue([
      plugin("p-1", "dsh-mcp-lens"),
      plugin("p-2", "dsh-context"),
    ]);
    mockListAgentDshPlugins.mockResolvedValue([
      plugin("p-1", "dsh-mcp-lens"),
      plugin("p-2", "dsh-context"),
    ]);
    renderTab();

    await screen.findByText("dsh-mcp-lens");
    await userEvent.click(
      screen.getByRole("button", { name: "Detach dsh-mcp-lens" }),
    );

    expect(mockSetAgentDshPlugins).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", {name:"Submit configuration changes"}));
    await waitFor(() => {
      expect(mockSetAgentDshPlugins).toHaveBeenCalledWith("agent-1", [
        { id: "p-2", enabled: true },
      ], [plugin("p-1", "dsh-mcp-lens"), plugin("p-2", "dsh-context")]);
    });
  });

  it("disabling keeps the plugin bound so its configuration survives", async () => {
    mockListDshPlugins.mockResolvedValue([plugin("p-1", "dsh-mcp-lens")]);
    mockListAgentDshPlugins.mockResolvedValue([plugin("p-1", "dsh-mcp-lens")]);
    renderTab();

    await screen.findByText("dsh-mcp-lens");
    await userEvent.click(
      screen.getByRole("switch", { name: "Enable dsh-mcp-lens" }),
    );

    expect(mockSetAgentDshPlugins).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", {name:"Submit configuration changes"}));
    await waitFor(() => {
      expect(mockSetAgentDshPlugins).toHaveBeenCalledWith("agent-1", [
        { id: "p-1", enabled: false },
      ], [plugin("p-1", "dsh-mcp-lens")]);
    });
  });

  it("offers no write controls to a viewer who cannot edit", async () => {
    mockListDshPlugins.mockResolvedValue([plugin("p-1", "dsh-mcp-lens")]);
    mockListAgentDshPlugins.mockResolvedValue([plugin("p-1", "dsh-mcp-lens")]);
    renderTab(false);

    await screen.findByText("dsh-mcp-lens");
    expect(screen.queryByRole("button", { name: "Attach" })).toBeNull();
    expect(screen.queryByRole("switch")).toBeNull();
    expect(
      screen.queryByRole("button", { name: "Detach dsh-mcp-lens" }),
    ).toBeNull();
  });
});

describe("DshPluginsTab on a runtime that does not load plugins", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockGetProfile.mockResolvedValue({state:"applied",current:true,desiredRevision:"1",appliedRevision:"1",builds:[]});
    mockListDshPlugins.mockResolvedValue([]);
    mockListAgentDshPlugins.mockResolvedValue([]);
  });

  // The alternative was hiding the tab, which strands whatever is already
  // bound: invisible, unremovable, and live again as soon as the agent moves
  // back to a cloud runtime. Saying it keeps the bindings reachable.
  it("says so when the agent runs on a local daemon", async () => {
    renderTab(true, { runtime_mode: "local" });
    expect(
      await screen.findByText(
        enAgents.tab_body.dsh_plugins.local_runtime_notice,
      ),
    ).toBeInTheDocument();
  });

  it("says nothing of the sort on a cloud agent", async () => {
    renderTab(true, { runtime_mode: "cloud" });
    await screen.findByText(enAgents.tab_body.dsh_plugins.empty_title);
    expect(
      screen.queryByText(enAgents.tab_body.dsh_plugins.local_runtime_notice),
    ).not.toBeInTheDocument();
  });
});


describe("employee plugin settings", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockGetProfile.mockResolvedValue({state:"applied",current:true,desiredRevision:"1",appliedRevision:"1",builds:[]});
    mockListDshPlugins.mockResolvedValue([plugin("p-1", "dsh-mcp-lens")]);
    mockListAgentDshPlugins.mockResolvedValue([plugin("p-1", "dsh-mcp-lens")]);
    mockGetConfig.mockResolvedValue({ agentId: "agent-1", pluginId: "p-1", revision: 12, inherited: false, rowId: "a-row", config: { token: "fixture-private" } });
    mockUpdateConfig.mockResolvedValue({ agentId: "agent-1", pluginId: "p-1", revision: 14, inherited: false, rowId: "a-row", config: { token: "fixture-private" } });
  });
  it("reveals only on explicit read and saves with the observed revision", async () => {
    renderTab();
    await userEvent.click(await screen.findByRole("button", { name: "Configure" }));
    expect(mockGetConfig).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Read settings" }));
    const config = await screen.findByLabelText("Configuration (JSON)");
    expect((config as HTMLTextAreaElement).value).toContain("fixture-private");
    await userEvent.click(screen.getByRole("button", { name: "Stage settings" }));
    expect(mockUpdateConfig).not.toHaveBeenCalled();
    expect(mockSetAgentDshPlugins).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", {name:"Submit configuration changes"}));
    await waitFor(() => expect(mockSetAgentDshPlugins).toHaveBeenCalledWith("agent-1", [{id:"p-1",enabled:true,configChange:{expectedRevision:12,override:{rowId:"a-row",config:{token:"fixture-private"}}}}], [plugin("p-1", "dsh-mcp-lens")]));
  });
  it("keeps staged settings editable when the atomic submission fails", async () => {
    mockSetAgentDshPlugins.mockRejectedValue(new ApiError("Reload settings", 409, "Conflict"));
    renderTab();
    await userEvent.click(await screen.findByRole("button", {name:"Configure"}));
    await userEvent.click(screen.getByRole("button", {name:"Read settings"}));
    await screen.findByLabelText("Configuration (JSON)");
    await userEvent.click(screen.getByRole("button", {name:"Stage settings"}));
    await userEvent.click(screen.getByRole("button", {name:"Submit configuration changes"}));
    await waitFor(() => expect(mockSetAgentDshPlugins).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(screen.getByRole("button", {name:"Submit configuration changes"})).not.toBeDisabled());
    expect(mockUpdateConfig).not.toHaveBeenCalled();
    expect(screen.getByText("You have unsubmitted changes")).toBeTruthy();
  });
});

it("disables all plugin writes until the employee filesystem is ready", async () => {
  vi.clearAllMocks();
  mockGetHome.mockResolvedValue({provisioned: false, state: "unprovisioned", step: 0});
  mockListAgentDshPlugins.mockResolvedValue([plugin("one", "plugin-one")]);
  mockListDshPlugins.mockResolvedValue([plugin("one", "plugin-one"), plugin("two", "plugin-two")]);
  renderTab(true, {}, {...dshRuntime, metadata: {kind: "fc-e2b"}});
  await screen.findByText("Open native DSH to prepare the filesystem before configuring plugins.");
  await screen.findByText("plugin-one");
  expect(screen.getByRole("switch")).toHaveAttribute("aria-disabled", "true");
  for (const button of screen.getAllByRole("button")) expect(button).toBeDisabled();
  expect(mockSetAgentDshPlugins).not.toHaveBeenCalled();
});

it("stages multiple operations in one submission and locks editing until the exact revision is confirmed", async () => {
  vi.clearAllMocks();
  mockGetHome.mockResolvedValue({provisioned:true,state:"running",step:6});
  mockGetProfile.mockResolvedValue({state:"applied",current:true,desiredRevision:"10",appliedRevision:"10",builds:[]});
  mockPrepareProfile.mockResolvedValue({state:"pending_host",current:false,desiredRevision:"11",appliedRevision:"10",builds:[]});
  mockSetAgentDshPlugins.mockImplementation(async () => { mockListAgentDshPlugins.mockResolvedValue([plugin("p2","second",false)]); });
  mockListDshPlugins.mockResolvedValue([plugin("p1","first"),plugin("p2","second")]);
  mockListAgentDshPlugins.mockResolvedValue([plugin("p1","first"),plugin("p2","second")]);
  const {queryClient} = renderTab(true,{}, {...dshRuntime,metadata:{kind:"fc-e2b"}});
  await userEvent.click(await screen.findByRole("button",{name:"Detach first"}));
  await userEvent.click(screen.getByRole("switch",{name:"Enable second"}));
  expect(mockSetAgentDshPlugins).not.toHaveBeenCalled();
  await userEvent.click(screen.getByRole("button",{name:"Submit configuration changes"}));
  await waitFor(() => expect(mockPrepareProfile).toHaveBeenCalledWith("ws-1","agent-1"));
  expect(mockSetAgentDshPlugins).toHaveBeenCalledExactlyOnceWith("agent-1",[{id:"p2",enabled:false}], [plugin("p1","first"),plugin("p2","second")]);
  expect(screen.getByRole("button",{name:"Applying configuration…"})).toBeDisabled();
  expect(screen.getByRole("button",{name:"Configure"})).toBeDisabled();
  expect(screen.getByRole("switch")).toHaveAttribute("aria-disabled","true");
  await act(async () => queryClient.setQueryData(["workspace","ws-1","agents","agent-1","dsh-profile"],{state:"applied",current:true,desiredRevision:"11",appliedRevision:"11",builds:[]}));
  await waitFor(() => expect(screen.getByRole("button",{name:"Configure"})).not.toBeDisabled());
  expect(screen.getByText("No pending configuration changes")).toBeTruthy();
});

it("keeps the observed base when native changes arrive while a draft is open", async () => {
  vi.clearAllMocks();
  mockGetHome.mockResolvedValue({provisioned:true,state:"running",step:6});
  mockGetProfile.mockResolvedValue({state:"applied",current:true,desiredRevision:"10",appliedRevision:"10",builds:[]});
  const original = {...plugin("p1","existing"),configRevision:11};
  mockListAgentDshPlugins.mockResolvedValue([original]);
  mockListDshPlugins.mockResolvedValue([original]);
  mockSetAgentDshPlugins.mockResolvedValue(undefined);
  const {queryClient} = renderTab();
  await userEvent.click(await screen.findByRole("switch",{name:"Enable existing"}));
  await act(async () => queryClient.setQueryData(["workspaces","ws-1","dsh-plugins","agent","agent-1"],[original,plugin("p2","native-installed")]));
  await userEvent.click(screen.getByRole("button",{name:"Submit configuration changes"}));
  await waitFor(() => expect(mockSetAgentDshPlugins).toHaveBeenCalledWith("agent-1",[{id:"p1",enabled:false}],[original]));
});
