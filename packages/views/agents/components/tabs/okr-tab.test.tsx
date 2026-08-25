// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { Agent } from "@multica/core/types";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../../locales/en/common.json";
import enAgents from "../../../locales/en/agents.json";
import { OKRTab } from "./okr-tab";

const TEST_RESOURCES = { en: { common: enCommon, agents: enAgents } };

const mockList = vi.hoisted(() => vi.fn());
const mockSet = vi.hoisted(() => vi.fn());

vi.mock("@multica/core/api", () => ({
  api: {
    listAgentOKRs: (id: string) => mockList(id),
    setAgentOKRs: (id: string, okrs: unknown) => mockSet(id, okrs),
  },
}));

const baseAgent = {
  id: "agent-1",
  workspace_id: "ws-1",
  runtime_id: "runtime-1",
  name: "Agent",
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
  owner_id: "user-1",
  skills: [],
  created_at: "2026-05-28T00:00:00Z",
  updated_at: "2026-05-28T00:00:00Z",
  archived_at: null,
  archived_by: null,
} as Agent;

function renderTab(readOnly = false) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <QueryClientProvider client={queryClient}>
        <OKRTab agent={baseAgent} readOnly={readOnly} />
      </QueryClientProvider>
    </I18nProvider>,
  );
}

describe("OKRTab", () => {
  beforeEach(() => {
    mockList.mockReset();
    mockSet.mockReset();
    mockList.mockResolvedValue({ okrs: [], usage_available: true });
    mockSet.mockResolvedValue({ okrs: [], usage_available: true });
  });

  it("renders stored OKRs with their key results", async () => {
    mockList.mockResolvedValue({ okrs: [
      {
        objective: "Shorten turnaround",
        label: "O: Shorten turnaround",
        color: "#6366f1",
        key_results: [
          { text: "Median under 2h", label: "KR: Median under 2h", color: "#0ea5e9" },
        ],
      },
    ], usage_available: true });
    renderTab();
    expect(await screen.findByDisplayValue("Shorten turnaround")).toBeInTheDocument();
    expect(screen.getByDisplayValue("Median under 2h")).toBeInTheDocument();
  });

  it("shows an empty state when the agent has no OKRs", async () => {
    renderTab();
    expect(await screen.findByText("No OKRs yet.")).toBeInTheDocument();
  });

  it("keeps save disabled until something changes", async () => {
    renderTab();
    await screen.findByText("No OKRs yet.");
    expect(screen.getByRole("button", { name: /^save$/i })).toBeDisabled();

    fireEvent.click(screen.getByRole("button", { name: /add objective/i }));
    expect(screen.getByRole("button", { name: /^save$/i })).toBeEnabled();
  });

  it("submits objectives with their key results", async () => {
    renderTab();
    await screen.findByText("No OKRs yet.");
    fireEvent.click(screen.getByRole("button", { name: /add objective/i }));
    fireEvent.change(screen.getByLabelText("Objective"), {
      target: { value: "Shorten turnaround" },
    });
    fireEvent.change(screen.getByLabelText("Key result"), {
      target: { value: "Median under 2h" },
    });
    fireEvent.click(screen.getByRole("button", { name: /^save$/i }));

    await waitFor(() =>
      expect(mockSet).toHaveBeenCalledWith("agent-1", [
        { objective: "Shorten turnaround", key_results: ["Median under 2h"] },
      ]),
    );
  });

  it("drops blank rows rather than submitting editing artifacts", async () => {
    renderTab();
    await screen.findByText("No OKRs yet.");
    // Two objectives, only one filled in; the untouched one is an artifact of
    // clicking Add, not a submission.
    fireEvent.click(screen.getByRole("button", { name: /add objective/i }));
    fireEvent.click(screen.getByRole("button", { name: /add objective/i }));
    const [firstObjective] = screen.getAllByLabelText("Objective");
    if (!firstObjective) throw new Error("no Objective field rendered");
    fireEvent.change(firstObjective, { target: { value: "Real objective" } });
    fireEvent.click(screen.getByRole("button", { name: /^save$/i }));

    await waitFor(() =>
      expect(mockSet).toHaveBeenCalledWith("agent-1", [
        { objective: "Real objective", key_results: [] },
      ]),
    );
  });

  it("removes an objective", async () => {
    mockList.mockResolvedValue({ okrs: [
      { objective: "Gone", label: "O: Gone", color: "", key_results: [] },
    ], usage_available: true });
    renderTab();
    await screen.findByDisplayValue("Gone");
    fireEvent.click(screen.getByRole("button", { name: /remove objective/i }));
    expect(screen.queryByDisplayValue("Gone")).not.toBeInTheDocument();
  });

  it("shows what each objective and key result has cost", async () => {
    mockList.mockResolvedValue({ okrs: [
      {
        objective: "Shorten turnaround",
        label: "O: Shorten turnaround",
        color: "",
        spend: {
          total_tokens: 1000,
          total_cost_usd_ticks: 25_000_000_000,
          task_count: 3,
          unpriced_task_count: 0,
        },
        key_results: [
          {
            text: "Median under 2h",
            label: "KR: Median under 2h",
            color: "",
            spend: {
              total_tokens: 400,
              total_cost_usd_ticks: 12_000_000_000,
              task_count: 1,
              unpriced_task_count: 0,
            },
          },
        ],
      },
    ], usage_available: true });
    renderTab();
    await screen.findByDisplayValue("Shorten turnaround");
    expect(screen.getByText("$2.50")).toBeInTheDocument();
    expect(screen.getByText("3 tasks")).toBeInTheDocument();
    expect(screen.getByText("$1.20")).toBeInTheDocument();
    expect(screen.getByText("1 task")).toBeInTheDocument();
  });

  it("marks a cost as a floor when some tasks ran unpriced", async () => {
    mockList.mockResolvedValue({ okrs: [
      {
        objective: "Partly priced",
        label: "O: Partly priced",
        color: "",
        spend: {
          total_tokens: 900,
          total_cost_usd_ticks: 5_000_000_000,
          task_count: 4,
          unpriced_task_count: 2,
        },
        key_results: [],
      },
    ], usage_available: true });
    renderTab();
    await screen.findByDisplayValue("Partly priced");
    expect(screen.getByText("$0.50*")).toBeInTheDocument();
  });

  it("does not show a cost for an entry that has never run", async () => {
    mockList.mockResolvedValue({ okrs: [
      {
        objective: "Brand new",
        label: "O: Brand new",
        color: "",
        spend: {
          total_tokens: 0,
          total_cost_usd_ticks: 0,
          task_count: 0,
          unpriced_task_count: 0,
        },
        key_results: [],
      },
    ], usage_available: true });
    renderTab();
    await screen.findByDisplayValue("Brand new");
    // A zero-task entry has nothing to report; "$0.00" would read as a measured
    // result rather than an absence of one.
    expect(screen.queryByText("$0.00")).not.toBeInTheDocument();
  });

  it("distinguishes an unavailable usage query from measured zero", async () => {
    mockList.mockResolvedValue({
      okrs: [
        {
          objective: "Still visible",
          label: "O: Still visible",
          color: "",
          key_results: [],
        },
      ],
      usage_available: false,
    });
    renderTab();
    await screen.findByDisplayValue("Still visible");
    expect(
      screen.getByText(/cost data is temporarily unavailable/i),
    ).toBeInTheDocument();
    expect(screen.queryByText("$0.00")).not.toBeInTheDocument();
  });

  it("hides cost while the set is being edited", async () => {
    mockList.mockResolvedValue({ okrs: [
      {
        objective: "Costed",
        label: "O: Costed",
        color: "",
        spend: {
          total_tokens: 10,
          total_cost_usd_ticks: 30_000_000_000,
          task_count: 2,
          unpriced_task_count: 0,
        },
        key_results: [],
      },
    ], usage_available: true });
    renderTab();
    await screen.findByDisplayValue("Costed");
    expect(screen.getByText("$3.00")).toBeInTheDocument();

    // Costs belong to saved entries. Once the list is being reordered or
    // retyped, a positional cost could land on the wrong row.
    fireEvent.change(screen.getByLabelText("Objective"), {
      target: { value: "Costed and renamed" },
    });
    expect(screen.queryByText("$3.00")).not.toBeInTheDocument();
  });

  it("tells the owner that removing an OKR keeps its label", async () => {
    renderTab();
    await screen.findByText("No OKRs yet.");
    expect(
      screen.getByText(/issues may already carry it/i),
    ).toBeInTheDocument();
  });

  it("offers no editing affordance when read-only", async () => {
    mockList.mockResolvedValue({ okrs: [
      { objective: "Fixed", label: "O: Fixed", color: "", key_results: [] },
    ], usage_available: true });
    renderTab(true);
    await screen.findByDisplayValue("Fixed");
    expect(screen.getByDisplayValue("Fixed")).toBeDisabled();
    expect(
      screen.queryByRole("button", { name: /add objective/i }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /^save$/i }),
    ).not.toBeInTheDocument();
  });
});
