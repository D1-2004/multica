// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider, queryOptions } from "@tanstack/react-query";
import type { Agent, AgentSceneMemory, ChatMessage } from "@multica/core/types";
import type { AgentSceneDetail, AgentSceneSummary } from "@multica/core/context-capabilities";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../../locales/en/common.json";
import enAgents from "../../../locales/en/agents.json";
import { NavigationProvider, type NavigationAdapter } from "../../../navigation";
import { ScenesTab } from "./scenes-tab";

const mocks = vi.hoisted(() => ({
  listScenes: vi.fn(),
  getScene: vi.fn(),
  setPrompt: vi.fn(),
  setBinding: vi.fn(),
  getCapabilities: vi.fn(),
  messages: vi.fn(),
  conversations: vi.fn(),
  getMemory: vi.fn(),
  compact: false,
}));

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/api", () => ({
  api: {
    listAgentScenes: mocks.listScenes,
    getAgentScene: mocks.getScene,
    setAgentScenePrompt: mocks.setPrompt,
    setAgentSceneBinding: mocks.setBinding,
    getAgentContextCapabilities: mocks.getCapabilities,
    listAgentCoordinatorConversationMessages: mocks.messages,
    listAgentCoordinatorConversations: mocks.conversations,
  },
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
vi.mock("sonner", () => ({ toast: { error: vi.fn(), success: vi.fn() } }));

const copy = enAgents.tab_body.scenes;

const agent = {
  id: "agent-1",
  workspace_id: "ws-1",
  name: "Helper",
  scene_memory_ui_enabled: true,
} as Agent;

const groupScene: AgentSceneSummary = {
  sceneKey: "cid-group",
  kind: "group",
  title: "Release crew",
  orgId: "org",
  lastActiveAt: "2026-09-30T08:00:00Z",
  inboundSessionId: "session-1",
  inboundCount: 3,
  memoryId: "memory-1",
  hasPrompt: true,
};

const dmScene: AgentSceneSummary = {
  sceneKey: "cid-dm",
  kind: "dm",
  title: "",
  orgId: "org",
  lastActiveAt: "",
  inboundSessionId: "",
  inboundCount: 0,
  memoryId: "",
  hasPrompt: false,
};

function detailOf(scene: AgentSceneSummary, overrides: Partial<AgentSceneDetail> = {}): AgentSceneDetail {
  return {
    scene,
    prompt: scene.hasPrompt
      ? { text: "Answer briefly.", updatedAt: "2026-09-30T09:00:00Z", updatedByName: "Ada" }
      : { text: "", updatedAt: "", updatedByName: "" },
    bindings: [
      {
        resourceType: "connector",
        resourceId: "conn-wiki",
        enabled: true,
        updatedByName: "Bob",
        updatedAt: "2026-09-30T10:00:00Z",
      },
    ],
    offers: {
      connectors: [
        {
          id: "conn-wiki",
          name: "Wiki",
          catalogSlug: "",
          authMode: "bearer",
          acceptsCredential: true,
          acceptsPat: false,
          oauthAvailable: false,
          installUrl: "",
          credential: { connected: false, account: "" },
        },
      ],
      skills: [{ id: "skill-report", name: "Weekly report", description: "Writes reports" }],
    },
    scope: { type: "scene", key: scene.sceneKey, title: scene.title },
    mcpConfig: null,
    mcpConfigSupported: true,
    mcpConfigRedacted: false,
    canConnect: true,
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
  mocks.listScenes.mockResolvedValue({ scenes: [groupScene, dmScene], hasMore: false });
  mocks.getScene.mockImplementation(async (_ws: string, _agent: string, sceneKey: string) =>
    detailOf(sceneKey === "cid-dm" ? dmScene : groupScene),
  );
  mocks.getCapabilities.mockResolvedValue({
    enabled: true,
    library: { connectors: [], skills: [] },
    offers: { connectorIds: [], skillIds: [] },
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

describe("ScenesTab list", () => {
  it("lists group chats and 1:1 chats as compact one-line rows", async () => {
    renderTab();

    const group = await screen.findByRole("button", { name: `${copy.kind_group} Release crew` });
    // Counts live in the scene detail; the row only flags memory and a prompt.
    expect(within(group).queryByText(/connector|skill/)).not.toBeInTheDocument();
    expect(within(group).getByRole("img", { name: copy.has_memory })).toBeInTheDocument();
    expect(within(group).getByRole("img", { name: copy.has_prompt })).toBeInTheDocument();

    const dm = screen.getByRole("button", { name: `${copy.kind_dm} ${copy.untitled_dm}` });
    expect(within(dm).queryByRole("img")).not.toBeInTheDocument();
    expect(mocks.listScenes).toHaveBeenCalledWith("ws-1", "agent-1", { limit: 50, offset: 0 });
  });

  it("explains that scenes appear after the agent talks in DingTalk", async () => {
    mocks.listScenes.mockResolvedValue({ scenes: [], hasMore: false });
    renderTab();

    expect(await screen.findByText(copy.empty_title)).toBeInTheDocument();
    expect(screen.getByText(copy.empty_hint)).toBeInTheDocument();
  });
});

describe("ScenesTab scene detail", () => {
  it("opens the latest scene on its inbound history", async () => {
    renderTab();

    expect(await screen.findByText("hello from the group")).toBeInTheDocument();
    expect(mocks.messages).toHaveBeenCalledWith("agent-1", "session-1", null);
    expect(screen.getByRole("tab", { name: copy.tab_inbound })).toHaveAttribute("aria-selected", "true");
  });

  it("keeps the selected scene and sub-tab in the address", async () => {
    const user = userEvent.setup();
    const { navigation } = renderTab();

    await user.click(await screen.findByRole("button", { name: `${copy.kind_dm} ${copy.untitled_dm}` }));
    expect(navigation.replace).toHaveBeenLastCalledWith("/acme/agents/agent-1?view=scenes&scene=cid-dm");
    expect(await screen.findByText(copy.inbound_empty)).toBeInTheDocument();

    await user.click(screen.getByRole("tab", { name: copy.tab_config }));
    expect(navigation.replace).toHaveBeenLastCalledWith(
      "/acme/agents/agent-1?view=scenes&scene=cid-dm&scene_tab=config",
    );
  });

  it("closes an app dialog of the previous scene when another scene opens", async () => {
    const user = userEvent.setup();
    const { navigation } = renderTab("view=scenes&scene=cid-group&scene_tab=inbound&app=github");

    await user.click(await screen.findByRole("button", { name: `${copy.kind_dm} ${copy.untitled_dm}` }));

    expect(navigation.replace).toHaveBeenLastCalledWith("/acme/agents/agent-1?view=scenes&scene=cid-dm");
  });

  it("loads the memory of the scene by its id", async () => {
    // The agent-wide list is capped, so the scene row is fetched directly.
    mocks.getMemory.mockResolvedValue({ id: "memory-1", scene_key: "cid-group" } as AgentSceneMemory);
    renderTab("view=scenes&scene=cid-group&scene_tab=memory");

    expect(await screen.findByText("memory-detail:memory-1")).toBeInTheDocument();
    expect(mocks.getMemory).toHaveBeenCalledWith("memory-1");
  });

  it("says when a scene has no memory yet", async () => {
    renderTab("view=scenes&scene=cid-dm&scene_tab=memory");

    expect(await screen.findByText(copy.memory_empty)).toBeInTheDocument();
    expect(mocks.getMemory).not.toHaveBeenCalled();
  });

  it("opens every inbound conversation and memory row outside the scene list", async () => {
    const user = userEvent.setup();
    renderTab();

    await user.click(await screen.findByRole("button", { name: copy.all_inbound }));
    expect(await screen.findByText("Legacy robot chat")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: copy.all_inbound })).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: copy.back }));
    await user.click(await screen.findByRole("button", { name: copy.all_memory }));
    expect(await screen.findByText("all-memory-list")).toBeInTheDocument();
  });
});

describe("ScenesTab scene configuration", () => {
  it("saves the scene prompt and reports unsaved edits", async () => {
    mocks.setPrompt.mockResolvedValue({ text: "Be brief.", updatedAt: "2026-09-30T11:00:00Z", updatedByName: "Ada" });
    const user = userEvent.setup();
    const { onDirtyChange } = renderTab("view=scenes&scene=cid-group&scene_tab=config");

    const input = await screen.findByRole("textbox", { name: copy.prompt_title });
    expect(input).toHaveValue("Answer briefly.");
    // Under the prompt and under the scene's custom MCP servers: both are
    // stored only.
    expect(screen.getAllByText(copy.prompt_hint)).toHaveLength(2);
    await user.clear(input);
    await user.type(input, "Be brief.");
    expect(onDirtyChange).toHaveBeenLastCalledWith(true);
    await user.click(screen.getByRole("button", { name: copy.prompt_save }));

    await waitFor(() =>
      expect(mocks.setPrompt).toHaveBeenCalledWith("ws-1", "agent-1", "cid-group", "Be brief."),
    );
  });

  it("asks before an unsaved prompt is dropped by a scene or sub-tab switch", async () => {
    const user = userEvent.setup();
    const { onDirtyChange } = renderTab("view=scenes&scene=cid-group&scene_tab=config");

    const input = await screen.findByRole("textbox", { name: copy.prompt_title });
    await user.type(input, " Draft");
    expect(onDirtyChange).toHaveBeenLastCalledWith(true);

    // Another sub-tab: keep editing leaves the draft in place.
    await user.click(screen.getByRole("tab", { name: copy.tab_inbound }));
    const dialog = await screen.findByRole("alertdialog");
    await user.click(within(dialog).getByRole("button", { name: enAgents.tabs.discard_keep }));
    expect(screen.getByRole("textbox", { name: copy.prompt_title })).toHaveValue("Answer briefly. Draft");
    expect(screen.getByRole("tab", { name: copy.tab_config })).toHaveAttribute("aria-selected", "true");

    // Another scene: discarding switches and clears the dirty flag.
    await user.click(screen.getByRole("button", { name: `${copy.kind_dm} ${copy.untitled_dm}` }));
    const second = await screen.findByRole("alertdialog");
    await user.click(within(second).getByRole("button", { name: enAgents.tabs.discard_confirm }));
    await waitFor(() => expect(onDirtyChange).toHaveBeenLastCalledWith(false));
    expect(await screen.findByRole("textbox", { name: copy.prompt_title })).toHaveValue("");
  });

  it("keeps the default scene open when a refetch reorders the list", async () => {
    const user = userEvent.setup();
    renderTab("view=scenes&scene_tab=config");

    const input = await screen.findByRole("textbox", { name: copy.prompt_title });
    await user.type(input, " Draft");
    // New activity moves the 1:1 chat to the top of the list.
    mocks.listScenes.mockResolvedValue({
      scenes: [{ ...dmScene, lastActiveAt: "2026-09-30T12:00:00Z" }, groupScene],
      hasMore: false,
    });
    await user.click(screen.getByRole("button", { name: copy.refresh }));

    await waitFor(() => expect(mocks.listScenes).toHaveBeenCalledTimes(2));
    expect(screen.getByRole("heading", { level: 2, name: "Release crew" })).toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: copy.prompt_title })).toHaveValue("Answer briefly. Draft");
  });

  it("keeps an unsaved draft when the stored prompt changes underneath", async () => {
    const user = userEvent.setup();
    renderTab("view=scenes&scene=cid-group&scene_tab=config");

    const input = await screen.findByRole("textbox", { name: copy.prompt_title });
    await user.type(input, " Draft");
    // Another admin saved meanwhile; a refetch brings the new stored prompt.
    mocks.getScene.mockResolvedValue(
      detailOf(groupScene, {
        prompt: { text: "Reply in English.", updatedAt: "2026-09-30T12:00:00Z", updatedByName: "Eve" },
      }),
    );
    await user.click(screen.getByRole("button", { name: copy.refresh }));

    expect(await screen.findByText(/Eve/)).toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: copy.prompt_title })).toHaveValue("Answer briefly. Draft");
  });

  it("refuses a prompt over the server limit", async () => {
    const user = userEvent.setup();
    renderTab("view=scenes&scene=cid-dm&scene_tab=config");

    const input = await screen.findByRole("textbox", { name: copy.prompt_title });
    await user.click(input);
    await user.paste("x".repeat(8001));

    expect(screen.getByText("Keep the prompt within 8000 characters.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: copy.prompt_save })).toBeDisabled();
  });

  it("toggles offered connectors and skills for the scene", async () => {
    mocks.setBinding.mockResolvedValue({
      resourceType: "skill",
      resourceId: "skill-report",
      enabled: true,
      updatedByName: "Ada",
      updatedAt: "",
    });
    const user = userEvent.setup();
    renderTab("view=scenes&scene=cid-group&scene_tab=config");

    const wiki = await screen.findByRole("switch", { name: "Turn on Wiki in this scene" });
    expect(wiki).toBeChecked();
    await user.click(screen.getByRole("switch", { name: "Turn on Weekly report in this scene" }));

    await waitFor(() =>
      expect(mocks.setBinding).toHaveBeenCalledWith("ws-1", "agent-1", "cid-group", {
        resourceType: "skill",
        resourceId: "skill-report",
        enabled: true,
      }),
    );
    // Members turn items on themselves from the configuration page.
    expect(await screen.findByDisplayValue("https://app.example/dingtalk/configure?agent=agent-1")).toBeInTheDocument();
  });
});

describe("ScenesTab on a phone", () => {
  it("shows the list first and a way back from a scene", async () => {
    mocks.compact = true;
    const user = userEvent.setup();
    const { navigation } = renderTab();

    await user.click(await screen.findByRole("button", { name: `${copy.kind_group} Release crew` }));
    await user.click(await screen.findByRole("button", { name: copy.back }));

    expect(navigation.replace).toHaveBeenLastCalledWith("/acme/agents/agent-1?view=scenes");
  });
});
