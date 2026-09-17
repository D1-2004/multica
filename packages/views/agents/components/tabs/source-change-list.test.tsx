import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { I18nProvider } from "@multica/core/i18n/react";
import type { AgentSourceFileChange } from "@multica/core/types";
import enAgents from "../../../locales/en/agents.json";
import enSkills from "../../../locales/en/skills.json";
import { SourceChangesDialog } from "./source-change-list";

// Exercise our navigation and input handling independently of Shadow DOM layout.
// The real renderer is also checked in a browser.
vi.mock("@pierre/diffs/react", () => ({
  MultiFileDiff: ({ oldFile, newFile, options }: { oldFile: { contents: string }; newFile: { contents: string }; options: { diffStyle: string } }) => (
    <div data-testid="file-diff" data-layout={options.diffStyle}>
      <pre data-testid="before">{oldFile.contents}</pre>
      <pre data-testid="after">{newFile.contents}</pre>
    </div>
  ),
}));

const changes: AgentSourceFileChange[] = [
  { path: "AGENTS.md", status: "modified", before: "Old instructions", after: "New instructions" },
  { path: "skills/review/SKILL.md", status: "added", before: null, after: "Review changes" },
  { path: "skills/old/SKILL.md", status: "removed", before: "Old skill", after: null },
];

function preview(value = changes, open = true, onOpenChange = vi.fn()) {
  return <I18nProvider locale="en" resources={{ en: { agents: enAgents, skills: enSkills } }}>
    <SourceChangesDialog open={open} onOpenChange={onOpenChange} groups={[{ id: "configuration", title: "Configuration changes", changes: value }]} />
  </I18nProvider>;
}

describe("publication file diff", () => {
  it("renders deleted text using the status returned by the publication API", async () => {
    render(preview([{ path: "skills/release-check/references/legacy.md", status: "deleted", before: "Only present in v1", after: null }]));
    const diff = await screen.findByTestId("file-diff");
    expect(within(diff).getByTestId("before")).toHaveTextContent("Only present in v1");
    expect(within(diff).getByTestId("after")).toBeEmptyDOMElement();
    expect(screen.queryByText("deleted")).toBeNull();
    expect(screen.getAllByTitle(enAgents.package_diff.removed)).toHaveLength(2);
  });

  it("keeps withheld deleted text unavailable", () => {
    render(preview([{ path: "private.json", status: "deleted", before: null, after: null, before_sha: "old-object" }]));
    expect(screen.queryByTestId("file-diff")).toBeNull();
    expect(screen.getByText("Text content is unavailable for this file. Only file metadata can be compared.")).toBeInTheDocument();
  });

  it("selects files from the navigation and distinguishes additions and removals", async () => {
    render(preview());
    const navigation = screen.getByRole("tablist", { name: "Changed files" });
    const diff = await screen.findByTestId("file-diff");
    expect(diff).toHaveAttribute("data-layout", "split");
    expect(within(diff).getByTestId("before")).toHaveTextContent("Old instructions");
    fireEvent.click(within(navigation).getByRole("tab", { name: "skills/review/SKILL.md" }));
    expect(within(diff).getByTestId("before")).toBeEmptyDOMElement();
    expect(within(diff).getByTestId("after")).toHaveTextContent("Review changes");
    fireEvent.click(within(navigation).getByRole("tab", { name: "skills/old/SKILL.md" }));
    expect(within(diff).getByTestId("before")).toHaveTextContent("Old skill");
    expect(within(diff).getByTestId("after")).toBeEmptyDOMElement();
  });

  it("renders only in the global dialog and preserves selection across reopening", async () => {
    const onOpenChange = vi.fn();
    const view = render(preview(changes, false, onOpenChange));
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(screen.queryByRole("tablist")).toBeNull();
    view.rerender(preview(changes, true, onOpenChange));
    await screen.findByTestId("file-diff");
    fireEvent.click(screen.getByRole("tab", { name: "skills/review/SKILL.md" }));
    const dialog = await screen.findByRole("dialog");
    expect(view.container.contains(dialog)).toBe(false);
    expect(view.container.querySelector('[role="tabpanel"]')).toBeNull();
    expect(within(dialog).getByRole("tab", { name: "skills/review/SKILL.md" })).toHaveAttribute("aria-selected", "true");
    expect(await within(dialog).findByTestId("after")).toHaveTextContent("Review changes");
    fireEvent.click(within(dialog).getByRole("button", { name: "Close" }));
    expect(onOpenChange.mock.calls[0]?.[0]).toBe(false);
    view.rerender(preview(changes, false, onOpenChange));
    expect(screen.queryByRole("dialog")).toBeNull();
    view.rerender(preview(changes, true, onOpenChange));
    expect(screen.getByRole("tab", { name: "skills/review/SKILL.md" })).toHaveAttribute("aria-selected", "true");
  });

  it("does not turn unavailable Git text into an empty original file", () => {
    render(preview([{ path: "private.json", status: "modified", before: null, after: "new text", before_sha: "old-object", after_sha: "new-object", before_mode: "100644", after_mode: "100755" }]));
    expect(screen.getByText("Text content is unavailable for this file. Only file metadata can be compared.")).toBeInTheDocument();
    expect(screen.queryByTestId("file-diff")).toBeNull();
    expect(screen.getByText("old-object")).toBeInTheDocument();
    expect(screen.getByText("new-object")).toBeInTheDocument();
    expect(screen.getByText(/100644.*100755/)).toBeInTheDocument();
  });

  it("selects a valid file when a new preview replaces the current changes", async () => {
    const view = render(preview());
    await screen.findByTestId("file-diff");
    fireEvent.click(screen.getByRole("tab", { name: "skills/review/SKILL.md" }));
    view.rerender(preview([{ path: "agent.json", status: "modified", before: "{}", after: "{\"name\":\"New\"}" }]));
    expect(screen.getByRole("tab", { name: "agent.json" })).toHaveAttribute("aria-selected", "true");
    expect(await screen.findByTestId("after")).toHaveTextContent('{"name":"New"}');
  });

  it("shows an empty result without an unusable navigation or expand action", () => {
    render(preview([]));
    expect(screen.getByText("No changes.")).toBeInTheDocument();
    expect(screen.queryByRole("tablist")).toBeNull();
    expect(screen.queryByRole("button", { name: "Expand preview" })).toBeNull();
  });
});
