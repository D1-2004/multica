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
    mockList.mockResolvedValue([]);
    mockSet.mockResolvedValue([]);
  });

  it("renders stored OKRs with their key results", async () => {
    mockList.mockResolvedValue([
      {
        objective: "Shorten turnaround",
        label: "O: Shorten turnaround",
        color: "#6366f1",
        key_results: [
          { text: "Median under 2h", label: "KR: Median under 2h", color: "#0ea5e9" },
        ],
      },
    ]);
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
    mockList.mockResolvedValue([
      { objective: "Gone", label: "O: Gone", color: "", key_results: [] },
    ]);
    renderTab();
    await screen.findByDisplayValue("Gone");
    fireEvent.click(screen.getByRole("button", { name: /remove objective/i }));
    expect(screen.queryByDisplayValue("Gone")).not.toBeInTheDocument();
  });

  it("tells the owner that removing an OKR keeps its label", async () => {
    renderTab();
    await screen.findByText("No OKRs yet.");
    expect(
      screen.getByText(/issues may already carry it/i),
    ).toBeInTheDocument();
  });

  it("offers no editing affordance when read-only", async () => {
    mockList.mockResolvedValue([
      { objective: "Fixed", label: "O: Fixed", color: "", key_results: [] },
    ]);
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
