import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { I18nProvider } from "@multica/core/i18n/react";
import { WorkspaceSlugProvider } from "@multica/core/paths";
import type { ChatCoordinatorTrace } from "@multica/core/types";
import { NavigationProvider, type NavigationAdapter } from "../../navigation";
import enChat from "../../locales/en/chat.json";
import { CoordinatorFinish } from "./coordinator-finish";

function renderFinish(trace: ChatCoordinatorTrace) {
  const adapter: NavigationAdapter = {
    push: vi.fn(), replace: vi.fn(), back: vi.fn(),
    pathname: "/acme/chat", searchParams: new URLSearchParams(), getShareableUrl: (path) => path,
  };
  render(
    <I18nProvider locale="en" resources={{ en: { chat: enChat } }}>
      <WorkspaceSlugProvider slug="acme">
        <NavigationProvider value={adapter}>
          <CoordinatorFinish trace={trace} input={{ action: "issue", purpose: "Send 123" }} />
        </NavigationProvider>
      </WorkspaceSlugProvider>
    </I18nProvider>,
  );
  return adapter;
}

describe("CoordinatorFinish", () => {
  it("shows every committed result and navigates to the issue by stable id", () => {
    const adapter = renderFinish({
      action: "issue",
      issue_results: [
        { action: "issue_created", issue_id: "first", issue_identifier: "MUL-1", issue_title: "Send 123", task_id: "run" },
        { action: "issue_commented", issue_id: "second", issue_identifier: "MUL-2" },
      ],
    });
    expect(screen.getByText("Created issue")).toBeVisible();
    expect(screen.getByText("Delivered to issue")).toBeVisible();
    expect(screen.getAllByText("Execution enqueued")).toHaveLength(1);
    const created = screen.getByRole("link", { name: "MUL-1 · Send 123" });
    expect(created).toHaveAttribute("href", "/acme/issues/first");
    fireEvent.click(created);
    expect(adapter.push).toHaveBeenCalledWith("/acme/issues/first");
    const continued = screen.getByRole("link", { name: "MUL-2" });
    expect(fireEvent.click(continued, { metaKey: true })).toBe(true);
    expect(adapter.push).toHaveBeenCalledTimes(1);
    expect(screen.getByText("Decision input").closest("details")).not.toHaveAttribute("open");
  });

  it("does not invent a successful result for an old issue verdict", () => {
    renderFinish({ action: "issue" });
    expect(screen.getByText("Execution result not recorded")).toBeVisible();
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
  });

  it("prioritizes terminal comment evidence over a reply verdict", () => {
    renderFinish({ action: "reply", issue_results: [{ action: "issue_commented", issue_id: "existing" }] });
    expect(screen.getByText("Delivered to issue")).toBeVisible();
    expect(screen.queryByText("Direct reply")).not.toBeInTheDocument();
  });

  it("handles future result actions without claiming creation", () => {
    renderFinish({ issue_results: [{ action: "future", issue_id: "existing" }] });
    expect(screen.getByText("Linked issue")).toBeVisible();
    expect(screen.queryByText("Created issue")).not.toBeInTheDocument();
  });

  it("keeps a committed issue visible when later execution fails", () => {
    renderFinish({
      action: "issue",
      issue_results: [{ action: "issue_created", issue_id: "first", issue_identifier: "MUL-1" }],
      steps: [{ seq: 1, type: "error", content: "Second item failed", error: true }],
    });
    expect(screen.getByText("Execution failed")).toBeVisible();
    expect(screen.getByText("Second item failed")).toBeVisible();
    expect(screen.getByRole("link", { name: "MUL-1" })).toBeVisible();
  });
});
