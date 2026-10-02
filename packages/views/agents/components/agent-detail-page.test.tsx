// @vitest-environment jsdom

import { describe, it, expect, vi, beforeEach } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { Agent } from "@multica/core/types";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/en/common.json";
import enAgents from "../../locales/en/agents.json";
import {
  NavigationProvider,
  type NavigationAdapter,
} from "../../navigation";

const TEST_RESOURCES = { en: { common: enCommon, agents: enAgents } };

// Keep the real message settings for mutation/cache integration coverage;
// unrelated tabs and avatar/presence widgets are stubbed.
vi.mock("./agent-overview-pane", async () => {
  const { InboundCoordinatorSetting } = await import("./agent-message-settings");
  return {
    AgentOverviewPane: ({ agent, canEdit, onUpdate }: {
      agent: Agent;
      canEdit: boolean;
      onUpdate: (id: string, data: Record<string, unknown>) => Promise<void>;
    }) => <InboundCoordinatorSetting agent={agent} canEdit={canEdit} onUpdate={(data) => onUpdate(agent.id, data)} />,
  };
});
vi.mock("../../common/actor-avatar", () => ({
  ActorAvatar: () => <div>actor-avatar</div>,
}));
vi.mock("./agent-presence-indicator", () => ({
  AgentPresenceIndicator: () => null,
}));

const agentsRef = vi.hoisted(() => ({ current: [] as unknown[] }));
const membersRef = vi.hoisted(() => ({ current: [] as unknown[] }));
// When set, the member query never resolves — the "membership still loading"
// window in which the DM decision is undetermined.
const membersPendingRef = vi.hoisted(() => ({ current: false }));
const currentUserRef = vi.hoisted(() => ({
  current: { id: "user-1" } as { id: string } | null,
}));
const mockToastError = vi.hoisted(() => vi.fn());
const mockUpdateAgent = vi.hoisted(() => vi.fn());
const mockModalOpen = vi.hoisted(() => vi.fn());

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));
vi.mock("@multica/core/agents", () => ({
  isAgentRuntimeBound: (agent: { runtime_id: string; runtime_bound?: boolean }) =>
    agent.runtime_bound !== false && agent.runtime_id.length > 0,
  useWorkspacePresenceMap: () => ({ byAgent: new Map() }),
  agentSourceOptions: (wsId: string, agentId: string) => ({
    queryKey: ["agent-source", wsId, agentId],
    queryFn: () => Promise.resolve(undefined),
  }),
  agentSourceKeys: {
    detail: (wsId: string, agentId: string) => ["agent-source", wsId, agentId],
  },
}));
vi.mock("@multica/core/workspace/queries", () => ({
  agentListOptions: (wsId: string) => ({
    queryKey: ["agents", wsId],
    queryFn: () => Promise.resolve(agentsRef.current),
  }),
  memberListOptions: (wsId: string) => ({
    queryKey: ["members", wsId],
    queryFn: () =>
      membersPendingRef.current
        ? new Promise(() => {})
        : Promise.resolve(membersRef.current),
  }),
  workspaceKeys: {
    agents: (wsId: string) => ["agents", wsId],
    skills: (wsId: string) => ["skills", wsId],
  },
}));
vi.mock("@multica/core/runtimes", () => ({
  runtimeListOptions: (wsId: string) => ({
    queryKey: ["runtimes", wsId],
    queryFn: () => Promise.resolve([]),
  }),
}));
vi.mock("@multica/core/auth", () => {
  type AuthState = { user: { id: string } | null };
  const state = (): AuthState => ({ user: currentUserRef.current });
  const useAuthStore = Object.assign(
    (selector?: (s: AuthState) => unknown) =>
      selector ? selector(state()) : state(),
    { getState: state },
  );
  return { useAuthStore };
});
vi.mock("@multica/core/modals", () => ({
  useModalStore: Object.assign(vi.fn(), {
    getState: () => ({ open: mockModalOpen }),
  }),
}));
vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({
    agents: () => "/acme/agents",
    chat: () => "/acme/chat",
  }),
}));
vi.mock("@multica/core/api", () => {
  class ApiError extends Error {
    status: number;
    constructor(status: number, message: string) {
      super(message);
      this.status = status;
    }
  }
  return {
    api: { getAgent: vi.fn(() => Promise.reject(new ApiError(404, "not found"))), updateAgent: mockUpdateAgent },
    ApiError,
  };
});
vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: mockToastError },
}));

import { AgentDetailPage } from "./agent-detail-page";

const baseAgent: Agent = {
  id: "agent-1",
  workspace_id: "ws-1",
  runtime_id: "runtime-1",
  name: "Lambda",
  description: "",
  instructions: "",
  avatar_url: null,
  runtime_mode: "local",
  runtime_config: {},
  custom_args: [],
  visibility: "workspace",
  permission_mode: "public_to",
  invocation_targets: [{ target_type: "workspace", target_id: null }],
  status: "idle",
  max_concurrent_tasks: 1,
  model: "",
  owner_id: "user-2",
  skills: [],
  created_at: "2026-05-28T00:00:00Z",
  updated_at: "2026-05-28T00:00:00Z",
  archived_at: null,
  archived_by: null,
};

function renderPage() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const push = vi.fn();
  const navigation: NavigationAdapter = {
    push,
    replace: vi.fn(),
    back: vi.fn(),
    pathname: "/acme/agents/agent-1",
    searchParams: new URLSearchParams(),
    getShareableUrl: (path) => path,
  };
  render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <NavigationProvider value={navigation}>
        <QueryClientProvider client={queryClient}>
          <AgentDetailPage agentId="agent-1" />
        </QueryClientProvider>
      </NavigationProvider>
    </I18nProvider>,
  );
  return { push, queryClient };
}

beforeEach(() => {
  vi.clearAllMocks();
  currentUserRef.current = { id: "user-1" };
  membersRef.current = [{ user_id: "user-1", role: "member" }];
  membersPendingRef.current = false;
  agentsRef.current = [baseAgent];
});

describe("AgentDetailPage DM button", () => {
  it("navigates to the chat deep link when the user can chat with the agent", async () => {
    const { push } = renderPage();
    fireEvent.click(await screen.findByRole("button", { name: "DM" }));
    expect(push).toHaveBeenCalledWith("/acme/chat?agent=agent-1");
    expect(mockToastError).not.toHaveBeenCalled();
  });

  it("shows a toast instead of navigating when the user lacks chat access", async () => {
    // Post-MUL-3963 a workspace admin can VIEW another member's private agent
    // but can no longer invoke (chat with) it — the exact case where the DM
    // button must explain itself rather than navigate.
    agentsRef.current = [
      { ...baseAgent, permission_mode: "private", invocation_targets: [] },
    ];
    membersRef.current = [{ user_id: "user-1", role: "admin" }];
    const { push } = renderPage();
    fireEvent.click(await screen.findByRole("button", { name: "DM" }));
    expect(mockToastError).toHaveBeenCalledWith(
      "You don't have access to chat with this agent.",
    );
    expect(push).not.toHaveBeenCalled();
  });

  it("disables DM while membership is resolving instead of toasting a false deny", async () => {
    // Review P2: a pending member query collapses role to null, which the
    // rules read as not_member — a legitimate public_to+workspace member
    // would get a wrong "no access" toast. Undetermined must disable, not deny.
    membersPendingRef.current = true;
    const { push } = renderPage();
    // The control is an anchor now, so "disabled" is expressed the only way a
    // link can express it: aria-disabled plus removal from the tab order.
    const dm = await screen.findByRole("button", { name: "DM" });
    expect(dm).toHaveAttribute("aria-disabled", "true");
    expect(dm).toHaveAttribute("tabindex", "-1");
    fireEvent.click(dm);
    expect(mockToastError).not.toHaveBeenCalled();
    expect(push).not.toHaveBeenCalled();
  });

  it("hides the DM button on an archived agent", async () => {
    agentsRef.current = [
      { ...baseAgent, archived_at: "2026-06-01T00:00:00Z" },
    ];
    renderPage();
    // The archived banner is the signal the page has settled past loading.
    await screen.findByText(/This agent is archived/);
    expect(screen.queryByRole("button", { name: "DM" })).not.toBeInTheDocument();
  });

  it("hides the more-actions trigger when no menu actions are available", async () => {
    // The gate must survive a caller who genuinely CAN archive: an admin
    // passes `canEditAgent`, so `canArchive` is true. The only thing keeping
    // the menu empty is the system agent's undefined `onArchive`. Assert the
    // real aria label (locale `detail.more_actions_aria` = "Agent actions"),
    // so removing the `hasMoreActions` gate would render the empty shell and
    // fail this test.
    agentsRef.current = [
      { ...baseAgent, system_key: "mika" },
    ];
    membersRef.current = [{ user_id: "user-1", role: "admin" }];
    renderPage();

    await screen.findByRole("button", { name: "Assign work" });
    expect(
      screen.queryByLabelText("Agent actions"),
    ).not.toBeInTheDocument();
  });

  it("keeps the more-actions trigger for an editable non-system agent", async () => {
    // Positive counterpart: an owner of a normal agent has a real archive
    // action, so the menu trigger must still render. Guards the gate against
    // over-hiding.
    agentsRef.current = [{ ...baseAgent, owner_id: "user-1" }];
    membersRef.current = [{ user_id: "user-1", role: "member" }];
    renderPage();

    await screen.findByRole("button", { name: "Assign work" });
    expect(
      screen.getByLabelText("Agent actions"),
    ).toBeInTheDocument();
  });

  it("explains an unbound agent and blocks run actions without losing the profile", async () => {
    agentsRef.current = [
      {
        ...baseAgent,
        owner_id: "user-1",
        runtime_id: "",
        runtime_bound: false,
      },
    ];
    renderPage();

    expect(
      await screen.findByText(/needs a runtime before it can run/i),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Bind runtime" }),
    ).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "DM" }));
    expect(mockToastError).toHaveBeenCalledWith(
      "Bind a runtime before running this agent.",
    );
    expect(mockModalOpen).not.toHaveBeenCalled();
  });
});


describe("AgentDetailPage audience saves", () => {
  beforeEach(() => {
    agentsRef.current = [{ ...baseAgent, owner_id: "user-1", inbound_coordinator: true, inbound_coordinator_user_decision_mode: "named", inbound_coordinator_user_decision_names: ["Alice"] }];
  });

  it("keeps the saved coordination mode in React Query until confirmation", async () => {
    agentsRef.current = [{ ...agentsRef.current[0] as Agent, coordination_mode: "coordinator", employee_loop_ready: true }];
    let resolveUpdate!: (agent: Agent) => void;
    mockUpdateAgent.mockImplementation(() => new Promise<Agent>((resolve) => { resolveUpdate = resolve; }));
    const { queryClient } = renderPage();
    fireEvent.click(await screen.findByRole("radio", { name: "EmployeeLoop" }));
    expect(mockUpdateAgent).toHaveBeenCalledWith("agent-1", { coordination_mode: "employee" });
    expect(queryClient.getQueryData<Agent[]>(["agents", "ws-1"])?.[0]?.coordination_mode).toBe("coordinator");
    expect(screen.getByRole("radio", { name: "Coordinator" })).toBeChecked();
    const updated = { ...agentsRef.current[0] as Agent, coordination_mode: "employee" as const };
    agentsRef.current = [updated];
    await act(async () => { resolveUpdate(updated); });
    await waitFor(() => expect(screen.getByRole("radio", { name: "EmployeeLoop" })).toBeChecked());
  });

  it("waits for the server before showing a new audience", async () => {
    let resolveUpdate!: (agent: Agent) => void;
    mockUpdateAgent.mockImplementation(() => new Promise<Agent>((resolve) => { resolveUpdate = resolve; }));
    const { queryClient } = renderPage();
    fireEvent.click(await screen.findByRole("radio", { name: "Everyone" }));
    expect(mockUpdateAgent).toHaveBeenCalledWith("agent-1", { inbound_coordinator_user_decision_mode: "all" });
    expect(screen.getByRole("radio", { name: "Named people" })).toBeChecked();
    expect(screen.getByRole("textbox")).toHaveValue("Alice");
    expect(queryClient.getQueryData<Agent[]>(["agents", "ws-1"])?.[0]?.inbound_coordinator_user_decision_mode).toBe("named");
    const updated = { ...agentsRef.current[0] as Agent, inbound_coordinator_user_decision_mode: "all" as const };
    agentsRef.current = [updated];
    await act(async () => { resolveUpdate(updated); });
    await waitFor(() => expect(screen.getByRole("radio", { name: "Everyone" })).toBeChecked());
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
  });

  it("preserves draft names after a rejected save and refetch", async () => {
    let rejectUpdate!: (reason: Error) => void;
    mockUpdateAgent.mockImplementation(() => new Promise<Agent>((_, reject) => { rejectUpdate = reject; }));
    const { queryClient } = renderPage();
    fireEvent.change(await screen.findByRole("textbox"), { target: { value: "Bob" } });
    fireEvent.click(screen.getByRole("button", { name: "Save names" }));
    expect(queryClient.getQueryData<Agent[]>(["agents", "ws-1"])?.[0]?.inbound_coordinator_user_decision_names).toEqual(["Alice"]);
    await act(async () => { rejectUpdate(new Error("save failed")); });
    await waitFor(() => expect(screen.getByRole("button", { name: "Save names" })).not.toBeDisabled());
    expect(screen.getByRole("textbox")).toHaveValue("Bob");
    expect(screen.getByRole("radio", { name: "Named people" })).toBeChecked();
    expect(mockToastError).toHaveBeenCalledWith("save failed");
  });

  it("keeps the saved audience after a rejected mode change", async () => {
    mockUpdateAgent.mockRejectedValue(new Error("save failed"));
    renderPage();
    fireEvent.click(await screen.findByRole("radio", { name: "Off" }));
    await waitFor(() => expect(mockToastError).toHaveBeenCalledWith("save failed"));
    expect(screen.getByRole("radio", { name: "Named people" })).toBeChecked();
    expect(screen.getByRole("textbox")).toHaveValue("Alice");
  });

  it("waits for the server for the whole coordinator disable request", async () => {
    mockUpdateAgent.mockImplementation(() => new Promise<Agent>(() => {}));
    const { queryClient } = renderPage();
    fireEvent.click(await screen.findByRole("switch", { name: "Judge before sandbox" }));
    expect(mockUpdateAgent).toHaveBeenCalledWith("agent-1", { inbound_coordinator: false, inbound_coordinator_user_decision_mode: "off", event_trigger_enabled: false });
    expect(queryClient.getQueryData<Agent[]>(["agents", "ws-1"])?.[0]).toMatchObject({ inbound_coordinator: true, inbound_coordinator_user_decision_mode: "named" });
  });

  it("preserves optimistic updates for independent settings", async () => {
    agentsRef.current = [{ ...baseAgent, owner_id: "user-1", inbound_coordinator: false }];
    mockUpdateAgent.mockImplementation(() => new Promise<Agent>(() => {}));
    const { queryClient } = renderPage();
    fireEvent.click(await screen.findByRole("switch", { name: "Judge before sandbox" }));
    expect(mockUpdateAgent).toHaveBeenCalledWith("agent-1", { inbound_coordinator: true });
    expect(queryClient.getQueryData<Agent[]>(["agents", "ws-1"])?.[0]?.inbound_coordinator).toBe(true);
  });
});
