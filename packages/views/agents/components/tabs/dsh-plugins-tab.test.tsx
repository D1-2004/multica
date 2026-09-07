// @vitest-environment jsdom

import { describe, it, expect, vi, beforeEach } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Agent, AgentRuntime } from "@multica/core/types";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../../locales/en/common.json";
import enAgents from "../../../locales/en/agents.json";

const TEST_RESOURCES = { en: { common: enCommon, agents: enAgents } };

const mockListDshPlugins = vi.hoisted(() => vi.fn());
const mockListAgentDshPlugins = vi.hoisted(() => vi.fn());
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

function renderTab(canEdit = true, agentOverrides: Partial<Agent> = {}) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <QueryClientProvider client={queryClient}>
        <DshPluginsTab
          agent={{ ...agent, ...agentOverrides }}
          runtime={dshRuntime}
          canEdit={canEdit}
        />
      </QueryClientProvider>
    </I18nProvider>,
  );
}

describe("DshPluginsTab", () => {
  beforeEach(() => {
    vi.clearAllMocks();
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

    // The endpoint replaces the set, so the already-attached plugin must be
    // re-sent. Sending only the new one would silently detach the other.
    await waitFor(() => {
      expect(mockSetAgentDshPlugins).toHaveBeenCalledWith("agent-1", [
        { id: "p-1", enabled: true },
        { id: "p-2", enabled: true },
      ]);
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

    await waitFor(() => {
      expect(mockSetAgentDshPlugins).toHaveBeenCalledWith("agent-1", [
        { id: "p-2", enabled: true },
      ]);
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

    await waitFor(() => {
      expect(mockSetAgentDshPlugins).toHaveBeenCalledWith("agent-1", [
        { id: "p-1", enabled: false },
      ]);
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
