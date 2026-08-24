import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, screen } from "@testing-library/react";
import { renderWithI18n } from "../../test/i18n";
import { compareIssueLabels, LabelsTab } from "./labels-tab";
import type { Label } from "@multica/core/types";

const labelsRef = vi.hoisted(() => ({ current: [] as Array<Record<string, unknown>> }));

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "workspace-1",
}));

vi.mock("@tanstack/react-query", () => ({
  useQuery: () => ({ data: labelsRef.current, isLoading: false }),
}));

vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({
    labelUsage: (id: string) => `/acme/settings/labels/${id}`,
  }),
}));

vi.mock("../../navigation", () => ({
  useRowLink: () => () => ({}),
}));

vi.mock("@multica/core/labels", () => ({
  labelListOptions: (wsId: string, resourceType: string) => ({
    queryKey: ["labels", wsId, "list", resourceType],
  }),
  useCreateLabel: () => ({ mutate: vi.fn(), isPending: false }),
  useUpdateLabel: () => ({ mutate: vi.fn(), isPending: false }),
  useDeleteLabel: () => ({ mutate: vi.fn(), isPending: false }),
}));

describe("LabelsTab scopes", () => {
  afterEach(() => {
    cleanup();
    labelsRef.current = [];
  });

  // Agent labels were removed from the product (MUL-5600). The backend still
  // models the `agent` resource type, so the guard here is that the settings
  // UI never offers it as a manageable catalog again.
  it("offers only the issue and skill catalogs", () => {
    renderWithI18n(<LabelsTab />);

    expect(screen.getByRole("button", { name: /Issues/ })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Skills/ })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Agents/ })).toBeNull();
  });

  it("describes the tab without promising an agent catalog", () => {
    renderWithI18n(<LabelsTab />);

    expect(screen.getByText(/organize issues and skills/i)).toBeInTheDocument();
  });

  it("shows issue-label token and priced-cost summaries without detail requests", () => {
    labelsRef.current = [
      {
        id: "label-1",
        workspace_id: "workspace-1",
        resource_type: "issue",
        name: "Expensive",
        description: "High-cost work",
        color: "#3b82f6",
        usage_count: 1,
        usage_summary: {
          total_tokens: 12_000,
          total_cost_usd_ticks: 5_000_000_000,
          uncosted_tokens: 2_000,
          task_count: 3,
          priced_task_count: 2,
          unpriced_task_count: 1,
        },
        created_at: "2026-08-01T00:00:00Z",
        updated_at: "2026-08-24T00:00:00Z",
      },
    ];

    renderWithI18n(<LabelsTab />);

    expect(screen.getByText("12K")).toBeInTheDocument();
    expect(screen.getByText("$0.50")).toBeInTheDocument();
    expect(screen.getByTitle(/1 tasks are not fully priced/i)).toBeInTheDocument();
  });
});

describe("compareIssueLabels", () => {
  const base: Label = {
    id: "base",
    workspace_id: "workspace-1",
    resource_type: "issue",
    name: "Base",
    description: "",
    color: "#3b82f6",
    usage_count: 1,
    created_at: "2026-08-01T00:00:00Z",
    updated_at: "2026-08-24T00:00:00Z",
  };
  const complete: Label = {
    ...base,
    id: "complete",
    name: "Complete",
    usage_summary: {
      total_tokens: 100,
      total_cost_usd_ticks: 100,
      uncosted_tokens: 0,
      task_count: 1,
      priced_task_count: 1,
      unpriced_task_count: 0,
    },
  };
  const partial: Label = {
    ...base,
    id: "partial",
    name: "Partial",
    usage_summary: {
      total_tokens: 1_000,
      total_cost_usd_ticks: 1_000,
      uncosted_tokens: 500,
      task_count: 1,
      priced_task_count: 0,
      unpriced_task_count: 1,
    },
  };

  it.each(["cost_asc", "cost_desc"] as const)(
    "keeps incomplete and missing prices after complete prices for %s",
    (sort) => {
      const missing = { ...base, id: "missing", name: "Missing" };
      const ordered = [missing, partial, complete]
        .toSorted((a, b) => compareIssueLabels(a, b, sort))
        .map((label) => label.id);

      expect(ordered).toEqual(["complete", "partial", "missing"]);
    },
  );
});
