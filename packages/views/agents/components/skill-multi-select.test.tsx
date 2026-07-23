// @vitest-environment jsdom

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/en/common.json";
import enAgents from "../../locales/en/agents.json";
import { SkillMultiSelect } from "./skill-multi-select";

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

vi.mock("@multica/core/workspace/queries", () => ({
  skillListOptions: () => ({
    queryKey: ["workspaces", "ws-1", "skills"],
    queryFn: async () => [],
  }),
}));

const TEST_RESOURCES = { en: { common: enCommon, agents: enAgents } };

describe("SkillMultiSelect GitHub-managed skills", () => {
  it("shows repository skills immediately as selected and read-only", () => {
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });

    render(
      <I18nProvider locale="en" resources={TEST_RESOURCES}>
        <QueryClientProvider client={queryClient}>
          <SkillMultiSelect
            selectedIds={new Set()}
            onChange={vi.fn()}
            managedSkills={[
              {
                source_path: "agent/skills/repository-audit",
                name: "repository-audit",
                description: "Inspect repository state before delivery.",
                file_count: 1,
              },
            ]}
          />
        </QueryClientProvider>
      </I18nProvider>,
    );

    const managedRow = screen.getByRole("button", {
      name: /repository-audit/i,
    });
    expect(managedRow).toBeDisabled();
    expect(managedRow).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByText("Inspect repository state before delivery.")).toBeInTheDocument();
  });
});
