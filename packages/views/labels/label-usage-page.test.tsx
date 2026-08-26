import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, screen } from "@testing-library/react";
import type { LabelUsageResponse } from "@multica/core/types";
import { renderWithI18n } from "../test/i18n";
import { NavigationProvider, type NavigationAdapter } from "../navigation";
import { LabelUsagePage } from "./label-usage-page";

const queryRef = vi.hoisted(() => ({
  data: null as LabelUsageResponse | null,
  queryKey: [] as unknown[],
}));

vi.mock("@tanstack/react-query", async () => {
  const actual = await vi.importActual<typeof import("@tanstack/react-query")>(
    "@tanstack/react-query",
  );
  return {
    ...actual,
    useQuery: (options: { queryKey: unknown[] }) => {
      queryRef.queryKey = options.queryKey;
      return {
        data: queryRef.data,
        isLoading: false,
        isError: false,
        refetch: vi.fn(),
      };
    },
  };
});

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "workspace-1",
}));

vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({
    settingsLabels: () => "/acme/settings?tab=labels",
    issueDetail: (id: string) => `/acme/issues/${id}`,
  }),
}));

vi.mock("../common/use-viewing-timezone", () => ({
  useViewingTimezone: () => "Asia/Shanghai",
}));

function navigation(search = ""): NavigationAdapter {
  return {
    push: vi.fn(),
    replace: vi.fn(),
    back: vi.fn(),
    pathname: "/acme/settings/labels/label-1",
    searchParams: new URLSearchParams(search),
    getShareableUrl: (path) => `https://example.test${path}`,
  };
}

const DATA: LabelUsageResponse = {
  label: {
    id: "label-1",
    workspace_id: "workspace-1",
    resource_type: "issue",
    name: "Expensive",
    description: "High-cost work",
    color: "#3b82f6",
    usage_count: 1,
    created_at: "2026-08-01T00:00:00Z",
    updated_at: "2026-08-24T00:00:00Z",
  },
  summary: {
    total_tokens: 15_000,
    total_cost_usd_ticks: 10_000_000_000,
    uncosted_tokens: 5_000,
    task_count: 2,
    priced_task_count: 1,
    unpriced_task_count: 1,
  },
  daily: [
    {
      date: "2026-08-24",
      total_tokens: 15_000,
      total_cost_usd_ticks: 10_000_000_000,
      uncosted_tokens: 5_000,
      task_count: 2,
      priced_task_count: 1,
      unpriced_task_count: 1,
    },
  ],
  breakdown: [
    {
      provider: "opencode",
      model: "qwen3.8-max",
      total_tokens: 15_000,
      total_cost_usd_ticks: 10_000_000_000,
      uncosted_tokens: 5_000,
      task_count: 2,
      unpriced_task_count: 1,
    },
  ],
  tasks: [
    {
      task_id: "task-1",
      agent_id: "agent-1",
      agent_name: "Pricing Agent",
      issue_id: "issue-1",
      issue_identifier: "MUL-1",
      issue_title: "Investigate spend",
      status: "completed",
      provider: "",
      model: "",
      has_usage: true,
      is_priced: false,
      total_tokens: 15_000,
      total_cost_usd_ticks: 10_000_000_000,
      uncosted_tokens: 5_000,
      usage_breakdown: [
        {
          provider: "opencode",
          model: "qwen3.8-max",
          total_tokens: 10_000,
          total_cost_usd_ticks: 10_000_000_000,
          uncosted_tokens: 0,
          is_priced: true,
        },
        {
          provider: "deap",
          model: "qwen3.7-plus",
          total_tokens: 5_000,
          total_cost_usd_ticks: 0,
          uncosted_tokens: 5_000,
          is_priced: false,
        },
      ],
      created_at: "2026-08-24T01:00:00Z",
      completed_at: "2026-08-24T01:01:00Z",
      activity_at: "2026-08-24T01:01:00Z",
    },
  ],
  pagination: { page: 2, page_size: 25, total: 27, total_pages: 2 },
};

describe("LabelUsagePage", () => {
  afterEach(() => {
    cleanup();
    queryRef.data = null;
    queryRef.queryKey = [];
  });

  it("renders honest partial cost, charts, breakdown, and per-task usage", () => {
    queryRef.data = DATA;
    renderWithI18n(
      <NavigationProvider value={navigation("period=90d&sort=tokens&direction=asc&page=2")}>
        <LabelUsagePage labelId="label-1" />
      </NavigationProvider>,
    );

    expect(screen.getByRole("heading", { name: "Expensive" })).toBeInTheDocument();
    expect(screen.getByText("Partial subtotal")).toBeInTheDocument();
    expect(screen.getByText(/5K tokens are excluded/i)).toBeInTheDocument();
    expect(screen.getByText("Daily usage trend")).toBeInTheDocument();
    expect(screen.getAllByText("qwen3.8-max").length).toBeGreaterThan(0);
    expect(screen.getByText("qwen3.8-max, qwen3.7-plus")).toBeInTheDocument();
    expect(screen.getByText("MUL-1")).toBeInTheDocument();
    expect(screen.getByText("Page 2 of 2")).toBeInTheDocument();

    expect(queryRef.queryKey).toContainEqual({
      period: "90d",
      sort: "tokens",
      direction: "asc",
      tz: "Asia/Shanghai",
      page: 2,
      page_size: 25,
    });
  });

  it("does not present a zero price when every recorded token is unpriced", () => {
    queryRef.data = {
      ...DATA,
      summary: {
        ...DATA.summary,
        total_cost_usd_ticks: 0,
        uncosted_tokens: DATA.summary.total_tokens,
        priced_task_count: 0,
      },
      breakdown: [],
      tasks: [],
      pagination: { page: 1, page_size: 25, total: 0, total_pages: 0 },
    };
    renderWithI18n(
      <NavigationProvider value={navigation()}>
        <LabelUsagePage labelId="label-1" />
      </NavigationProvider>,
    );

    expect(screen.queryByText("$0.00")).toBeNull();
    expect(screen.getAllByText("—").length).toBeGreaterThan(0);
  });
});
