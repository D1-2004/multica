// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider, queryOptions } from "@tanstack/react-query";
import type { Agent, AgentSceneMemory } from "@multica/core/types";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../../locales/en/common.json";
import enAgents from "../../../locales/en/agents.json";
import { SceneMemoryTab, SceneMemoryDetail } from "./scene-memory-tab";

const TEST_RESOURCES = { en: { common: enCommon, agents: enAgents } };

const memoriesRef = vi.hoisted(() => ({
  current: [] as AgentSceneMemory[],
}));

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({
    issueDetail: (id: string) => `/ws/issues/${id}`,
  }),
}));

vi.mock("../../../navigation", () => ({
  AppLink: ({ href, children }: { href: string; children: unknown }) => (
    <a href={href}>{children as string}</a>
  ),
}));

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

const updateMemory = vi.fn();
const resetMemory = vi.fn();
const relationsFor = vi.hoisted(() => vi.fn());
vi.mock("@multica/core/api", () => ({
  api: {
    updateAgentSceneMemory: (...args: unknown[]) => updateMemory(...args),
    resetAgentSceneMemory: (...args: unknown[]) => resetMemory(...args),
    clearAgentSceneRelations: vi.fn(),
    listAgentSceneRelations: vi.fn(async () => []),
  },
}));

vi.mock("@multica/core/agents", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@multica/core/agents")>();
  return {
    ...actual,
    agentSceneMemoryOptions: (
      wsId: string,
      agentId: string,
      enabled = true,
      loop = "coordinator",
    ) =>
      queryOptions({
        queryKey: ["mem", wsId, agentId, enabled, loop],
        queryFn: async () => (enabled ? memoriesRef.current : []),
        enabled,
      }),
    agentSceneRelationOptions: (wsId: string, agentId: string, sceneId: string) => {
      relationsFor(sceneId);
      return queryOptions({
        queryKey: ["rel", wsId, agentId, sceneId],
        queryFn: async () => [],
      });
    },
  };
});

vi.mock("@multica/ui/hooks/use-mobile", () => ({
  useIsCompact: () => false,
}));

const agent = {
  id: "agent-1",
  workspace_id: "ws-1",
  name: "测试号",
} as Agent;

const onUpdate = vi.fn(async () => {});

// Scene memory rows are keyed by the scene_id (id and scene_key repeat it);
// the DingTalk conversation id is display-only.
const SCENE_DM = "66666666-6666-4666-8666-666666666666";
const SCENE_A = "77777777-7777-4777-8777-777777777777";
const SCENE_B = "88888888-8888-4888-8888-888888888888";

function memoryRow(sceneId: string, overrides: Partial<AgentSceneMemory> = {}): AgentSceneMemory {
  return {
    id: sceneId,
    scene_id: sceneId,
    workspace_id: "ws-1",
    agent_id: "agent-1",
    org_id: "org",
    scene_key: sceneId,
    conversation_id: "cid+abc",
    scene_kind: "dm",
    scene_title: "冬翔",
    memory_text: "GoalMate 是工具",
    memory_revision: 1,
    status: "clean",
    last_error: "",
    last_error_code: "",
    updated_at: "2026-09-01T00:00:00Z",
    ...overrides,
  };
}

function renderTab(next: Agent, canEdit = false) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <QueryClientProvider client={client}>
        <SceneMemoryTab agent={next} canEdit={canEdit} onUpdate={onUpdate} />
      </QueryClientProvider>
    </I18nProvider>,
  );
}

describe("SceneMemoryTab", () => {
  afterEach(() => {
    cleanup();
    onUpdate.mockClear();
    updateMemory.mockReset();
    resetMemory.mockReset();
    relationsFor.mockClear();
  });

  it("keeps the list hidden when the UI flag is off but still shows switches", async () => {
    memoriesRef.current = [memoryRow(SCENE_DM)];
    renderTab(agent, true);
    expect(
      await screen.findByText("Turn on Show list to see memory for each DM and group."),
    ).toBeInTheDocument();
    expect(screen.getByLabelText("Write")).toBeInTheDocument();
    expect(screen.queryByText("冬翔")).toBeNull();
  });

  it("hides scene memory from members who cannot edit the agent", async () => {
    memoriesRef.current = [memoryRow(SCENE_DM)];
    renderTab({ ...agent, scene_memory_ui_enabled: true }, false);
    expect(
      await screen.findByText("Turn on Show list to see memory for each DM and group."),
    ).toBeInTheDocument();
    expect(screen.queryByText("冬翔")).toBeNull();
  });

  it("lists scene memory when the UI flag is on", async () => {
    memoriesRef.current = [memoryRow(SCENE_DM, { memory_revision: 2 })];
    renderTab({ ...agent, scene_memory_ui_enabled: true }, true);
    expect(await screen.findByRole("button", { name: "Direct message 冬翔" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Edit" })).toBeInTheDocument();
    expect(screen.getByText("Issue links")).toBeInTheDocument();
    expect(screen.getByLabelText("Write")).toBeInTheDocument();
    // Relations are read by the scene_id, never by the conversation id.
    expect(relationsFor).toHaveBeenCalledWith(SCENE_DM);
    expect(relationsFor).not.toHaveBeenCalledWith("cid+abc");
  });

  it("saves a scene's memory by its scene_id", async () => {
    updateMemory.mockResolvedValue(memoryRow(SCENE_DM, { memory_text: "edited", memory_revision: 3 }));
    memoriesRef.current = [memoryRow(SCENE_DM, { memory_revision: 2 })];
    renderTab({ ...agent, scene_memory_ui_enabled: true }, true);

    fireEvent.click(await screen.findByRole("button", { name: "Edit" }));
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "edited" } });
    fireEvent.click(screen.getByRole("button", { name: enAgents.tab_body.inbound.memory_save }));

    await waitFor(() =>
      expect(updateMemory).toHaveBeenCalledWith("agent-1", SCENE_DM, {
        memory_text: "edited",
        expected_revision: 2,
        loop: "coordinator",
        scene_id: SCENE_DM,
        org_id: "org",
      }, "coordinator"),
    );
  });

  it("keeps two scenes distinct and shows the selected text", async () => {
    memoriesRef.current = [
      memoryRow(SCENE_A, {
        conversation_id: "cid-a",
        scene_kind: "group",
        scene_title: "场域隔离A",
        memory_text: "GAMMA-A-881 是报表工具",
      }),
      memoryRow(SCENE_B, {
        conversation_id: "cid-b",
        scene_kind: "group",
        scene_title: "场域隔离B",
        memory_text: "这个群还没有口径",
        updated_at: "2026-09-01T00:00:01Z",
      }),
    ];
    renderTab({ ...agent, scene_memory_ui_enabled: true }, true);
    expect(await screen.findByRole("button", { name: "Group 场域隔离A" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Group 场域隔离B" })).toBeInTheDocument();
    expect(screen.queryByText("Direct message")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: /场域隔离A/ }));
    expect(screen.getAllByText("GAMMA-A-881 是报表工具").length).toBeGreaterThan(0);
    fireEvent.click(screen.getByRole("button", { name: /场域隔离B/ }));
    expect(screen.getAllByText("这个群还没有口径").length).toBeGreaterThan(0);
    expect(relationsFor).toHaveBeenCalledWith(SCENE_B);
  });

  it("saves the write flag from the memory tab", async () => {
    memoriesRef.current = [];
    renderTab(agent, true);
    fireEvent.click(await screen.findByLabelText("Write"));
    expect(onUpdate).toHaveBeenCalledWith("agent-1", {
      scene_memory_write_enabled: true,
    });
  });
});

function memoryDetailView(next: Agent, memory: AgentSceneMemory, client: QueryClient) {
  return <I18nProvider locale="en" resources={TEST_RESOURCES}><QueryClientProvider client={client}><SceneMemoryDetail agent={next} memory={memory} canEdit /></QueryClientProvider></I18nProvider>;
}

describe("selected loop memory controls", () => {
  it("preserves Employee learning text even when it names a legacy locating section", async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const memory = memoryRow(SCENE_DM, { loop: "employee", memory_text: "scene-location (user-stated; evidence source-1):\n## 场域定位\n这是本场域明确记录的协作范围。" });
    render(memoryDetailView({ ...agent, coordination_mode: "employee" }, memory, client));
    const body = await screen.findByText(/这是本场域明确记录的协作范围/);
    expect(body.textContent).toContain("scene-location");
    expect(body.textContent).toContain("source-1");
  });
  it("does not list Coordinator summaries returned by an old server for Employee requests", async () => {
    memoriesRef.current = [memoryRow(SCENE_DM, { loop: "coordinator", scene_title: "Old Coordinator scene", memory_text: "OLD_COORDINATOR_PREVIEW" })];
    renderTab({ ...agent, coordination_mode: "employee" }, true);
    await screen.findAllByText(enAgents.tab_body.inbound.memory_empty);
    expect(screen.queryByText("Old Coordinator scene")).toBeNull();
    expect(screen.queryByText("OLD_COORDINATOR_PREVIEW")).toBeNull();
  });

  it("shows enterprise Employee memory without labelling it as a DM", async () => {
    memoriesRef.current = [memoryRow(SCENE_DM, { loop: "employee", scene_kind: "enterprise", scene_title: "Example enterprise" })];
    renderTab({ ...agent, coordination_mode: "employee" }, true);
    expect(await screen.findByRole("button", { name: `${enAgents.tab_body.context_builder.layer_org} Example enterprise` })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Direct message Example enterprise" })).toBeNull();
    expect(screen.queryByLabelText("Write")).toBeNull();
  });

  afterEach(() => { cleanup(); resetMemory.mockReset(); updateMemory.mockReset(); });
  it("shows Employee learning without Coordinator edit or relation controls", async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(memoryDetailView({ ...agent, coordination_mode: "employee" }, memoryRow(SCENE_DM, { loop: "employee", memory_text: "Employee learning" }), client));
    expect(await screen.findByText("Employee learning")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Edit" })).toBeNull();
    expect(screen.queryByText("Issue links")).toBeNull();
  });
  it("submits the selected loop, tenant, scene and displayed revision when resetting", async () => {
    resetMemory.mockResolvedValue(memoryRow(SCENE_DM, { loop: "employee", memory_revision: 8 }));
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(memoryDetailView({ ...agent, coordination_mode: "employee" }, memoryRow(SCENE_DM, { loop: "employee", memory_revision: 7 }), client));
    fireEvent.click(screen.getByRole("button", { name: enAgents.tab_body.inbound.memory_reset }));
    fireEvent.click(within(screen.getByRole("alertdialog")).getByRole("button", { name: enAgents.tab_body.inbound.memory_reset }));
    await waitFor(() => expect(resetMemory).toHaveBeenCalledWith("agent-1", SCENE_DM, { loop: "employee", scene_id: SCENE_DM, org_id: "org", expected_revision: 7 }));
  });
  it("drops an open confirmation when the selected loop changes", () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const memory = memoryRow(SCENE_DM, { loop: "coordinator" });
    const view = render(memoryDetailView({ ...agent, coordination_mode: "coordinator" }, memory, client));
    fireEvent.click(screen.getByRole("button", { name: enAgents.tab_body.inbound.memory_reset }));
    expect(screen.getByRole("alertdialog")).toBeInTheDocument();
    view.rerender(memoryDetailView({ ...agent, coordination_mode: "employee" }, memory, client));
    expect(screen.queryByRole("alertdialog")).toBeNull();
    expect(screen.queryByRole("button", { name: "Edit" })).toBeNull();
    expect(resetMemory).not.toHaveBeenCalled();
  });
});
