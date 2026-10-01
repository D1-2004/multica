// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider, queryOptions } from "@tanstack/react-query";
import type { Agent, AgentSceneMemory, ChatMessage } from "@multica/core/types";
import type {
  AgentSceneSummary,
  AgentTenantPerson,
  AgentTenantsList,
  ContextNodeDetail,
} from "@multica/core/context-capabilities";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../../locales/en/common.json";
import enAgents from "../../../locales/en/agents.json";
import { NavigationProvider, type NavigationAdapter } from "../../../navigation";
import { ScenesTab } from "./scenes-tab";

const mocks = vi.hoisted(() => ({
  listTenants: vi.fn(),
  createTenant: vi.fn(),
  renameTenant: vi.fn(),
  deleteTenant: vi.fn(),
  listGroups: vi.fn(),
  listPersons: vi.fn(),
  getNode: vi.fn(),
  setPrompts: vi.fn(),
  setBinding: vi.fn(),
  getCapabilities: vi.fn(),
  messages: vi.fn(),
  conversations: vi.fn(),
  getMemory: vi.fn(),
  compact: false,
}));

const { ApiError, errorCode } = vi.hoisted(() => {
  class ApiError extends Error {
    status: number;
    body?: unknown;
    constructor(message: string, status: number, body?: unknown) {
      super(message);
      this.status = status;
      this.body = body;
    }
  }
  function errorCode(err: unknown): string | undefined {
    if (err instanceof ApiError && err.body && typeof err.body === "object") {
      const code = (err.body as { code?: unknown }).code;
      if (typeof code === "string") return code;
    }
    return undefined;
  }
  return { ApiError, errorCode };
});

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/paths", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/paths")>()),
  useWorkspacePaths: () => ({ agentDetail: (id: string) => `/acme/agents/${id}` }),
}));
vi.mock("@multica/core/api", () => ({
  api: {
    listAgentTenants: mocks.listTenants,
    createAgentTenant: mocks.createTenant,
    renameAgentTenant: mocks.renameTenant,
    deleteAgentTenant: mocks.deleteTenant,
    listAgentTenantGroups: mocks.listGroups,
    listAgentTenantPersons: mocks.listPersons,
    getContextNode: mocks.getNode,
    setContextNodePrompts: mocks.setPrompts,
    setContextNodeBinding: mocks.setBinding,
    getAgentContextCapabilities: mocks.getCapabilities,
    listAgentCoordinatorConversationMessages: mocks.messages,
    listAgentCoordinatorConversations: mocks.conversations,
  },
  ApiError,
  errorCode,
}));
vi.mock("@multica/core/agents", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@multica/core/agents")>();
  return {
    ...actual,
    agentSceneMemoryDetailOptions: (wsId: string, agentId: string, memoryId: string, enabled = true) =>
      queryOptions({
        queryKey: ["mem", wsId, agentId, memoryId, enabled],
        queryFn: () => mocks.getMemory(memoryId) as Promise<AgentSceneMemory>,
        enabled: enabled && Boolean(memoryId),
      }),
  };
});
vi.mock("@multica/ui/hooks/use-mobile", () => ({ useIsCompact: () => mocks.compact }));
vi.mock("./scene-memory-tab", () => ({
  MemoryFlagBar: () => <div>memory-flag-bar</div>,
  SceneMemoryDetail: ({ memory }: { memory: AgentSceneMemory }) => <div>{`memory-detail:${memory.id}`}</div>,
  SceneMemoryTab: () => <div>all-memory-list</div>,
}));
vi.mock("../../../chat/components/chat-message-list", () => ({
  ChatMessageSkeleton: () => <div>message skeleton</div>,
  ChatMessageList: ({ messages }: { messages: ChatMessage[] }) => (
    <div data-testid="transcript">
      {messages.map((message) => (
        <p key={message.id}>{message.content}</p>
      ))}
    </div>
  ),
}));
vi.mock("sonner", () => ({ toast: { error: vi.fn(), success: vi.fn(), info: vi.fn() } }));

const copy = enAgents.tab_body.scenes;
const builder = enAgents.tab_body.context_builder;

const agent = {
  id: "agent-1",
  workspace_id: "ws-1",
  name: "Helper",
  scene_memory_ui_enabled: true,
} as Agent;

const tenants: AgentTenantsList = {
  tenants: [
    { orgId: "dingA", name: "Acme", source: "identity", groupCount: 1, personCount: 1 },
    { orgId: "dingB", name: "Beta", source: "created", groupCount: 0, personCount: 0 },
  ],
  unassignedOrgs: [{ orgId: "dingC", groupCount: 2, personCount: 0 }],
};

const groupScene: AgentSceneSummary = {
  sceneKey: "cid-group",
  kind: "group",
  title: "Release crew",
  orgId: "dingA",
  lastActiveAt: "2026-09-30T08:00:00Z",
  inboundSessionId: "session-1",
  inboundCount: 3,
  memoryId: "memory-1",
};

const person: AgentTenantPerson = {
  staffId: "staff-1",
  title: "Ada",
  dmSceneKey: "cid-dm",
  lastActiveAt: "",
};

// The person's 1:1 chat, as the person node reports it.
const dmScene: AgentSceneSummary = {
  sceneKey: "cid-dm",
  kind: "dm",
  title: "Ada",
  orgId: "dingA",
  lastActiveAt: "",
  inboundSessionId: "",
  inboundCount: 0,
  memoryId: "memory-dm",
};

function nodeDetail(overrides: Partial<ContextNodeDetail> = {}): ContextNodeDetail {
  return {
    scope: { type: "org", orgId: "dingA", key: "dingA", title: "Acme" },
    scene: null,
    prompts: [
      { id: "p1", name: "Tone", order: 1, text: "Answer briefly.", enabled: true, updatedByName: "Ada", updatedAt: "" },
    ],
    connectors: [],
    skills: [{ id: "skill-report", name: "Weekly report", description: "", enabled: false }],
    mcpConfig: null,
    mcpConfigRedacted: false,
    canConnect: true,
    rights: null,
    effective: { prompts: [], connectors: [], skills: [], mcpServers: [] },
    ...overrides,
  };
}

function renderTab(search = "view=scenes") {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const navigation: NavigationAdapter = {
    push: vi.fn(),
    replace: vi.fn(),
    back: vi.fn(),
    pathname: "/acme/agents/agent-1",
    searchParams: new URLSearchParams(search),
    getShareableUrl: (path) => path,
  };
  const onDirtyChange = vi.fn();
  render(
    <I18nProvider locale="en" resources={{ en: { common: enCommon, agents: enAgents } }}>
      <NavigationProvider value={navigation}>
        <QueryClientProvider client={client}>
          <ScenesTab agent={agent} canEdit onUpdate={vi.fn()} onDirtyChange={onDirtyChange} />
        </QueryClientProvider>
      </NavigationProvider>
    </I18nProvider>,
  );
  return { navigation, onDirtyChange };
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.compact = false;
  mocks.listTenants.mockResolvedValue(tenants);
  mocks.listGroups.mockResolvedValue({ scenes: [groupScene], hasMore: false });
  mocks.listPersons.mockResolvedValue([person]);
  mocks.getNode.mockImplementation(
    async (_ws: string, _agent: string, node: { scopeType: string; scopeKey: string }) =>
      node.scopeType === "org"
        ? nodeDetail()
        : nodeDetail({
            scope: { type: node.scopeType as "scene", orgId: "dingA", key: node.scopeKey, title: "" },
            scene: node.scopeType === "scene" ? groupScene : dmScene,
            prompts: [],
          }),
  );
  mocks.getMemory.mockResolvedValue({ id: "", scene_key: "" });
  mocks.conversations.mockResolvedValue({
    conversations: [
      {
        id: "conv-legacy",
        session_id: "session-legacy",
        title: "Legacy robot chat",
        updated_at: "2026-09-01T08:00:00Z",
        session_count: 1,
        conversation_title: "",
        conversation_type: "group",
        sender_name: "",
        source: "robot",
      },
    ],
    has_more: false,
    next_offset: 0,
  });
  mocks.getCapabilities.mockResolvedValue({
    enabled: true,
    library: { connectors: [], skills: [] },
    offers: { connectorIds: [], skillIds: [] },
    orgs: [],
    scenes: [],
    persons: [],
    configureUrl: "https://app.example/dingtalk/configure?agent=agent-1",
  });
  mocks.messages.mockResolvedValue({
    messages: [{ id: "m1", content: "hello from the group", role: "user", created_at: "2026-09-30T08:00:00Z" }],
    limit: 50,
    has_more: false,
    next_cursor: null,
  });
});

describe("ScenesTab tree", () => {
  it("lists tenants with their OrgId and orgs without a tenant, and opens the first tenant", async () => {
    renderTab();

    const acme = await screen.findByRole("button", { name: /^Acme/ });
    expect(within(acme).getByText("dingA")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /^Beta/ })).toBeInTheDocument();
    // An org seen without a tenant offers 创建租户 with its OrgId.
    expect(
      screen.getByRole("button", { name: copy.tenant_create_for.replace("{{orgId}}", "dingC") }),
    ).toBeInTheDocument();
    // Wide screens open the first tenant on its Context Builder.
    expect(await screen.findByRole("heading", { level: 2, name: "Acme" })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: copy.tab_config })).toHaveAttribute("aria-selected", "true");
    await waitFor(() =>
      expect(mocks.getNode).toHaveBeenCalledWith("ws-1", "agent-1", {
        orgId: "dingA",
        scopeType: "org",
        scopeKey: "dingA",
      }),
    );
  });

  it("lists a tenant's groups and people when their categories open", async () => {
    const user = userEvent.setup();
    renderTab();

    // Acme opens with the default selection; Beta stays closed.
    expect(await screen.findByRole("button", { name: copy.collapse.replace("{{name}}", "Acme") })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: copy.expand.replace("{{name}}", "Beta") })).toBeInTheDocument();
    expect(mocks.listGroups).not.toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: new RegExp(copy.category_groups) }));
    expect(await screen.findByRole("button", { name: /Release crew/ })).toBeInTheDocument();
    expect(mocks.listGroups).toHaveBeenCalledWith("ws-1", "agent-1", "dingA", { limit: 50, offset: 0 });

    await user.click(screen.getByRole("button", { name: new RegExp(copy.category_persons) }));
    expect(await screen.findByRole("button", { name: /Ada/ })).toBeInTheDocument();
    expect(mocks.listPersons).toHaveBeenCalledWith("ws-1", "agent-1", "dingA");
  });

  it("keeps the selected group and sub-tab in the address", async () => {
    const user = userEvent.setup();
    const { navigation } = renderTab("view=scenes&tenant=dingA&node=scene:cid-group");

    expect(await screen.findByText("hello from the group")).toBeInTheDocument();
    expect(mocks.messages).toHaveBeenCalledWith("agent-1", "session-1", null);
    expect(screen.getByRole("tab", { name: copy.tab_inbound })).toHaveAttribute("aria-selected", "true");

    await user.click(screen.getByRole("tab", { name: copy.tab_config }));
    expect(navigation.replace).toHaveBeenLastCalledWith(
      "/acme/agents/agent-1?view=scenes&tenant=dingA&node=scene%3Acid-group&scene_tab=config",
    );

    // The selected group's category is open; people open on demand.
    expect(screen.getByRole("button", { name: /Release crew/ })).toHaveAttribute("aria-current", "true");
    await user.click(screen.getByRole("button", { name: new RegExp(copy.category_persons) }));
    await user.click(await screen.findByRole("button", { name: /Ada/ }));
    expect(navigation.replace).toHaveBeenLastCalledWith(
      "/acme/agents/agent-1?view=scenes&tenant=dingA&node=person%3Astaff-1",
    );
  });

  it("maps an old scene link to the group under the agent's own org", async () => {
    const { navigation } = renderTab("view=scenes&scene=cid-group&scene_tab=memory&app=github");
    mocks.getMemory.mockResolvedValue({ id: "memory-1", scene_key: "cid-group" } as AgentSceneMemory);

    expect(await screen.findByText("memory-detail:memory-1")).toBeInTheDocument();
    expect(navigation.replace).toHaveBeenCalledWith(
      "/acme/agents/agent-1?view=scenes&tenant=dingA&node=scene%3Acid-group&scene_tab=memory",
    );
  });

  it("opens a person's memory through their 1:1 chat", async () => {
    mocks.getMemory.mockResolvedValue({ id: "memory-dm", scene_key: "cid-dm" } as AgentSceneMemory);
    renderTab("view=scenes&tenant=dingA&node=person:staff-1&scene_tab=memory");

    expect(await screen.findByText("memory-detail:memory-dm")).toBeInTheDocument();
    expect(mocks.getMemory).toHaveBeenCalledWith("memory-dm");
    expect(mocks.getNode).toHaveBeenCalledWith("ws-1", "agent-1", {
      orgId: "dingA",
      scopeType: "person",
      scopeKey: "staff-1",
    });
  });

  it("titles a deep-linked group beyond the loaded page from its node", async () => {
    mocks.listGroups.mockResolvedValue({ scenes: [], hasMore: true });
    renderTab("view=scenes&tenant=dingA&node=scene:cid-group");

    expect(await screen.findByRole("heading", { level: 2, name: "Release crew" })).toBeInTheDocument();
    expect(await screen.findByText("hello from the group")).toBeInTheDocument();
  });

  it("says when a person has no inbound history", async () => {
    renderTab("view=scenes&tenant=dingA&node=person:staff-1");

    expect(await screen.findByText(copy.inbound_empty)).toBeInTheDocument();
    expect(screen.getByRole("heading", { level: 2, name: "Ada" })).toBeInTheDocument();
  });

  it("opens every inbound conversation and memory row outside the tree", async () => {
    const user = userEvent.setup();
    renderTab();

    await user.click(await screen.findByRole("button", { name: copy.all_inbound }));
    expect(await screen.findByText("Legacy robot chat")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: copy.back }));
    await user.click(await screen.findByRole("button", { name: copy.all_memory }));
    expect(await screen.findByText("all-memory-list")).toBeInTheDocument();
  });

  it("explains an agent without tenants", async () => {
    mocks.listTenants.mockResolvedValue({ tenants: [], unassignedOrgs: [] });
    renderTab();

    expect(await screen.findByText(copy.empty_title)).toBeInTheDocument();
    expect(screen.getByText(copy.empty_hint)).toBeInTheDocument();
  });
});

describe("ScenesTab tenants", () => {
  it("creates a tenant for an unassigned org with its OrgId prefilled and opens it", async () => {
    mocks.createTenant.mockResolvedValue({
      orgId: "dingC",
      name: "Gamma",
      source: "created",
      groupCount: 2,
      personCount: 0,
    });
    const user = userEvent.setup();
    const { navigation } = renderTab();

    await user.click(
      await screen.findByRole("button", { name: copy.tenant_create_for.replace("{{orgId}}", "dingC") }),
    );
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByLabelText(copy.tenant_org_id)).toHaveValue("dingC");
    await user.type(within(dialog).getByLabelText(copy.tenant_name), "Gamma");
    await user.click(within(dialog).getByRole("button", { name: copy.tenant_create_submit }));

    await waitFor(() =>
      expect(mocks.createTenant).toHaveBeenCalledWith("ws-1", "agent-1", { orgId: "dingC", name: "Gamma" }),
    );
    await waitFor(() =>
      expect(navigation.replace).toHaveBeenLastCalledWith("/acme/agents/agent-1?view=scenes&tenant=dingC"),
    );
  });

  it("validates the OrgId and reports an existing tenant", async () => {
    mocks.createTenant.mockRejectedValue(new ApiError("exists", 409, { code: "tenant_exists" }));
    const user = userEvent.setup();
    renderTab();

    await user.click(await screen.findByRole("button", { name: copy.tenant_create }));
    const dialog = await screen.findByRole("dialog");
    await user.type(within(dialog).getByLabelText(copy.tenant_name), "Acme again");
    await user.type(within(dialog).getByLabelText(copy.tenant_org_id), "bad org!");
    await user.click(within(dialog).getByRole("button", { name: copy.tenant_create_submit }));
    expect(within(dialog).getByRole("alert")).toHaveTextContent(copy.tenant_org_id_invalid);
    expect(mocks.createTenant).not.toHaveBeenCalled();

    await user.clear(within(dialog).getByLabelText(copy.tenant_org_id));
    await user.type(within(dialog).getByLabelText(copy.tenant_org_id), "dingA");
    await user.click(within(dialog).getByRole("button", { name: copy.tenant_create_submit }));
    expect(await within(dialog).findByText(copy.tenant_exists)).toBeInTheDocument();
  });

  it("renames a tenant and never offers deleting the agent's own org", async () => {
    mocks.renameTenant.mockResolvedValue({ ...tenants.tenants[0], name: "Acme Group" });
    const user = userEvent.setup();
    renderTab("view=scenes&tenant=dingA&scene_tab=settings");

    const name = await screen.findByLabelText(copy.tenant_name);
    expect(name).toHaveValue("Acme");
    expect(screen.getByText(copy.tenant_identity_note)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: copy.tenant_delete })).not.toBeInTheDocument();
    await user.clear(name);
    await user.type(name, "Acme Group");
    await user.click(screen.getByRole("button", { name: builder.save }));

    await waitFor(() => expect(mocks.renameTenant).toHaveBeenCalledWith("ws-1", "agent-1", "dingA", "Acme Group"));
  });

  it("deletes a created tenant only after confirmation, then opens another tenant", async () => {
    mocks.deleteTenant.mockResolvedValue(undefined);
    const user = userEvent.setup();
    const { navigation } = renderTab("view=scenes&tenant=dingB&scene_tab=settings");

    await user.click(await screen.findByRole("button", { name: copy.tenant_delete }));
    const dialog = await screen.findByRole("alertdialog");
    expect(within(dialog).getByText(copy.tenant_delete_description)).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: copy.tenant_delete }));

    await waitFor(() => expect(mocks.deleteTenant).toHaveBeenCalledWith("ws-1", "agent-1", "dingB"));
    await waitFor(() =>
      expect(navigation.replace).toHaveBeenLastCalledWith("/acme/agents/agent-1?view=scenes&tenant=dingA"),
    );
  });
});

describe("ScenesTab Context Builder", () => {
  it("asks before unsaved prompt edits are dropped by a node or sub-tab switch", async () => {
    const user = userEvent.setup();
    const { onDirtyChange } = renderTab("view=scenes&tenant=dingA");

    await user.click(
      await screen.findByRole("button", { name: builder.prompt_delete.replace("{{name}}", "Tone") }),
    );
    expect(onDirtyChange).toHaveBeenLastCalledWith(true);

    // Another sub-tab: keeping the edits leaves the draft in place.
    await user.click(screen.getByRole("tab", { name: copy.tab_settings }));
    const dialog = await screen.findByRole("alertdialog");
    await user.click(within(dialog).getByRole("button", { name: enAgents.tabs.discard_keep }));
    expect(screen.getByRole("tab", { name: copy.tab_config })).toHaveAttribute("aria-selected", "true");
    expect(screen.queryByRole("listitem", { name: "Tone" })).not.toBeInTheDocument();

    // Another tenant: discarding switches and clears the dirty flag.
    await user.click(screen.getByRole("button", { name: /^Beta/ }));
    const second = await screen.findByRole("alertdialog");
    await user.click(within(second).getByRole("button", { name: enAgents.tabs.discard_confirm }));
    await waitFor(() => expect(onDirtyChange).toHaveBeenLastCalledWith(false));
    expect(await screen.findByRole("heading", { level: 2, name: "Beta" })).toBeInTheDocument();
  });

  it("links the configuration page under the builder", async () => {
    renderTab("view=scenes&tenant=dingA");

    expect(
      await screen.findByDisplayValue("https://app.example/dingtalk/configure?agent=agent-1"),
    ).toBeInTheDocument();
  });
});

describe("ScenesTab on a phone", () => {
  it("shows the tree first and a way back from a node", async () => {
    mocks.compact = true;
    const user = userEvent.setup();
    const { navigation } = renderTab();

    await user.click(await screen.findByRole("button", { name: /^Acme/ }));
    expect(await screen.findByRole("heading", { level: 2, name: "Acme" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: copy.back }));

    expect(navigation.replace).toHaveBeenLastCalledWith("/acme/agents/agent-1?view=scenes");
  });
});
