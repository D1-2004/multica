// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider, queryOptions } from "@tanstack/react-query";
import type { Agent, AgentSceneMemory } from "@multica/core/types";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../../locales/en/common.json";
import enAgents from "../../../locales/en/agents.json";
import { SceneMemoryTab } from "./scene-memory-tab";

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
vi.mock("@multica/core/api", () => ({
  api: {
    updateAgentSceneMemory: (...args: unknown[]) => updateMemory(...args),
    resetAgentSceneMemory: vi.fn(),
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
    ) =>
      queryOptions({
        queryKey: ["mem", wsId, agentId, enabled],
        queryFn: async () => (enabled ? memoriesRef.current : []),
        enabled,
      }),
    agentSceneRelationOptions: () =>
      queryOptions({
        queryKey: ["rel"],
        queryFn: async () => [],
      }),
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
  });

  it("keeps the list hidden when the UI flag is off but still shows switches", async () => {
    memoriesRef.current = [
      {
        id: "mem-1",
        workspace_id: "ws-1",
        agent_id: "agent-1",
        org_id: "org",
        scene_key: "cid+abc",
        scene_kind: "dm",
        scene_title: "冬翔",
        memory_text: "GoalMate 是工具",
        memory_revision: 1,
        status: "clean",
        last_error: "",
        last_error_code: "",
        updated_at: "2026-09-01T00:00:00Z",
      },
    ];
    renderTab(agent, true);
    expect(
      await screen.findByText("Turn on Show list to see memory for each DM and group."),
    ).toBeInTheDocument();
    expect(screen.getByLabelText("Write")).toBeInTheDocument();
    expect(screen.queryByText("冬翔")).toBeNull();
  });

  it("hides scene memory from members who cannot edit the agent", async () => {
    memoriesRef.current = [
      {
        id: "mem-1",
        workspace_id: "ws-1",
        agent_id: "agent-1",
        org_id: "org",
        scene_key: "cid+abc",
        scene_kind: "dm",
        scene_title: "冬翔",
        memory_text: "GoalMate 是工具",
        memory_revision: 1,
        status: "clean",
        last_error: "",
        last_error_code: "",
        updated_at: "2026-09-01T00:00:00Z",
      },
    ];
    renderTab({ ...agent, scene_memory_ui_enabled: true }, false);
    expect(
      await screen.findByText("Turn on Show list to see memory for each DM and group."),
    ).toBeInTheDocument();
    expect(screen.queryByText("冬翔")).toBeNull();
  });

  it("lists scene memory when the UI flag is on", async () => {
    memoriesRef.current = [
      {
        id: "mem-1",
        workspace_id: "ws-1",
        agent_id: "agent-1",
        org_id: "org",
        scene_key: "cid+abc",
        scene_kind: "dm",
        scene_title: "冬翔",
        memory_text: "GoalMate 是工具",
        memory_revision: 2,
        status: "clean",
        last_error: "",
        last_error_code: "",
        updated_at: "2026-09-01T00:00:00Z",
      },
    ];
    renderTab({ ...agent, scene_memory_ui_enabled: true }, true);
    expect(await screen.findByRole("button", { name: "Direct message 冬翔" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Edit" })).toBeInTheDocument();
    expect(screen.getByText("Issue links")).toBeInTheDocument();
    expect(screen.getByLabelText("Write")).toBeInTheDocument();
  });

  it("keeps two scenes distinct and shows the selected text", async () => {
    memoriesRef.current = [
      {
        id: "mem-a",
        workspace_id: "ws-1",
        agent_id: "agent-1",
        org_id: "org",
        scene_key: "cid-a",
        scene_kind: "group",
        scene_title: "场域隔离A",
        memory_text: "GAMMA-A-881 是报表工具",
        memory_revision: 1,
        status: "clean",
        last_error: "",
        last_error_code: "",
        updated_at: "2026-09-01T00:00:00Z",
      },
      {
        id: "mem-b",
        workspace_id: "ws-1",
        agent_id: "agent-1",
        org_id: "org",
        scene_key: "cid-b",
        scene_kind: "group",
        scene_title: "场域隔离B",
        memory_text: "这个群还没有口径",
        memory_revision: 1,
        status: "clean",
        last_error: "",
        last_error_code: "",
        updated_at: "2026-09-01T00:00:01Z",
      },
    ];
    renderTab({ ...agent, scene_memory_ui_enabled: true }, true);
    expect(await screen.findByRole("button", { name: "Group 场域隔离A" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Group 场域隔离B" })).toBeInTheDocument();
    expect(screen.queryByText("Direct message")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: /场域隔离A/ }));
    expect(screen.getAllByText("GAMMA-A-881 是报表工具").length).toBeGreaterThan(0);
    fireEvent.click(screen.getByRole("button", { name: /场域隔离B/ }));
    expect(screen.getAllByText("这个群还没有口径").length).toBeGreaterThan(0);
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
