import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import type { AgentSource, AgentSourceSyncPreview } from "@multica/core/types";
import enAgents from "../../../locales/en/agents.json";
import { PublishTab } from "./publish-tab";

const mocked = vi.hoisted(() => ({
  branches: vi.fn(), preview: vi.fn(), rollback: vi.fn(), history: vi.fn(), confirm: vi.fn(), error: vi.fn(), success: vi.fn(),
}));
vi.mock("@multica/core/api", () => ({ api: {
  listAgentSourceBranches: mocked.branches,
  previewAgentSourceSync: mocked.preview,
  syncAgentSource: mocked.confirm,
  listAgentPublications: mocked.history,
  previewAgentPublicationRollback: mocked.rollback,
} }));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "workspace-1" }));
vi.mock("sonner", () => ({ toast: { error: mocked.error, success: mocked.success, warning: vi.fn() } }));

const source: AgentSource = {
  agent_id: "agent-1", source_type: "git", connection_id: "installation-1",
  repository: "acme/reviewer", repository_url: "https://github.com/acme/reviewer", ref: "main", manifest_path: "agent.json",
  synced_commit_sha: "a".repeat(40), sync_status: "ready", connected: true, can_sync: true,
  last_sync_error: null, last_sync_attempt_at: null, last_synced_at: "",
};
const preview: AgentSourceSyncPreview = {
  preview_id: "preview-1", expires_at: "2099-01-01T00:00:00Z", repository_url: "https://github.com/acme/reviewer",
  ref: "release/v2", base_sha: "a".repeat(40), resolved_sha: "b".repeat(40), changed: true, warnings: [],
  git_changes: [{ path: "agent/AGENTS.md", status: "modified", before: "old instructions", after: "new instructions" }],
  configuration_changes: [{ path: "instructions", status: "modified", before: "old instructions", after: "new instructions" }],
  requirements: { secrets: [], deferred_bindings: [], runtime_provider: "", binding_declarations: [{ path: "/bindings/github_identity", declaration: { ref: "branch-maintainer" } }] },
};

function mount(canEdit = true) {
  return render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })}>
    <I18nProvider locale="en" resources={{ en: { agents: enAgents } }}>
      <PublishTab agentId="agent-1" source={source} canEdit={canEdit} />
    </I18nProvider>
  </QueryClientProvider>);
}

beforeEach(() => {
  vi.clearAllMocks();
  mocked.branches.mockResolvedValue({ repository: "acme/reviewer", repository_url: "https://github.com/acme/reviewer", default_branch: "main", branches: [] });
  mocked.preview.mockResolvedValue(preview);
  mocked.rollback.mockResolvedValue({ ...preview, ref: "main", resolved_sha: "c".repeat(40) });
  mocked.history.mockResolvedValue({ publications: [{ id: "release-1", source_type: "git", repository_url: "https://github.com/acme/reviewer", ref: "main", commit_sha: "c".repeat(40), published_at: "2026-09-14T12:00:00Z", published_by: "user-1", author_name: "Owner", changed: true, rollback_of: "", has_configuration_snapshot: true, initial_publication: true }], next_cursor: null });
  mocked.confirm.mockResolvedValue({ source: { ...source, ref: "release/v2", synced_commit_sha: "b".repeat(40) }, changed: true, warnings: [] });
});

describe("Git source import and export tab", () => {
  it("uses Git revisions without offering ZIP publication", () => {
    mount();
    expect(screen.queryByRole("button", { name: "Upload ZIP to publish" })).toBeNull();
    expect(screen.getByLabelText("Version type")).toBeDefined();
  });

  it("distinguishes tags from same-named branches and accepts a commit SHA", async () => {
    mount();
    fireEvent.change(screen.getByLabelText("Version type"), { target: { value: "tag" } });
    expect(screen.getByRole("button", { name: "Preview changes" })).toBeDisabled();
    fireEvent.change(screen.getByLabelText("Branch, tag or commit"), { target: { value: "release/v2" } });
    fireEvent.click(screen.getByRole("button", { name: "Preview changes" }));
    await screen.findByRole("dialog", { name: "Preview changes" });
    expect(mocked.preview).toHaveBeenLastCalledWith("agent-1", "refs/tags/release/v2");
    fireEvent.click(screen.getByRole("button", { name: "Close" }));
    fireEvent.change(screen.getByLabelText("Version type"), { target: { value: "commit" } });
    expect(screen.queryByText("branch-maintainer", { exact: false })).toBeNull();
    fireEvent.change(screen.getByLabelText("Branch, tag or commit"), { target: { value: "not-a-sha" } });
    expect(screen.getByRole("button", { name: "Preview changes" })).toBeDisabled();
    fireEvent.change(screen.getByLabelText("Branch, tag or commit"), { target: { value: "d".repeat(40) } });
    fireEvent.click(screen.getByRole("button", { name: "Preview changes" }));
    await screen.findByRole("dialog", { name: "Preview changes" });
    expect(mocked.preview).toHaveBeenLastCalledWith("agent-1", "d".repeat(40));
  });

  it("previews a recorded publication and waits for explicit confirmation to roll back", async () => {
    mount();
    fireEvent.click(await screen.findByRole("button", { name: "Restore this version" }));
    await screen.findByRole("dialog", { name: "Preview changes" });
    expect(mocked.rollback).toHaveBeenCalledWith("agent-1", "release-1");
    expect(mocked.confirm).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Close" }));
    fireEvent.click(screen.getByRole("button", { name: "Confirm publication" }));
    await waitFor(() => expect(mocked.confirm).toHaveBeenCalledOnce());
  });
  it("previews the selected branch and publishes only after explicit confirmation", async () => {
    const view = mount();
    fireEvent.change(screen.getByLabelText("Branch, tag or commit"), { target: { value: "release/v2" } });
    fireEvent.click(screen.getByRole("button", { name: "Preview changes" }));
    const dialog = await screen.findByRole("dialog", { name: "Preview changes" });
    expect(screen.getAllByRole("dialog")).toHaveLength(1);
    expect(view.container.querySelector('[role="tabpanel"]')).toBeNull();
    expect(screen.queryByRole("button", { name: "Expand preview" })).toBeNull();
    expect(mocked.preview).toHaveBeenCalledWith("agent-1", "refs/heads/release/v2");
    expect(mocked.confirm).not.toHaveBeenCalled();
    fireEvent.click(within(dialog).getByRole("tab", { name: "Git file changes (1)" }));
    expect(within(dialog).getByRole("tab", { name: "agent/AGENTS.md" })).toBeDefined();
    fireEvent.click(within(dialog).getByRole("tab", { name: "Configuration changes (1)" }));
    expect(within(dialog).getByRole("tab", { name: "instructions" })).toBeDefined();
    fireEvent.click(within(dialog).getByRole("button", { name: "Close" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    fireEvent.click(screen.getByRole("button", { name: "Confirm publication" }));
    await waitFor(() => expect(mocked.confirm).toHaveBeenCalledWith("agent-1", "preview-1", { secrets: {}, deferred_bindings: [] }));
  });

  it("invalidates a preview when the selected branch changes", async () => {
    mount();
    expect(screen.queryByText("branch-maintainer", { exact: false })).toBeNull();
    fireEvent.change(screen.getByLabelText("Branch, tag or commit"), { target: { value: "release/v2" } });
    fireEvent.click(screen.getByRole("button", { name: "Preview changes" }));
    await screen.findByRole("dialog", { name: "Preview changes" });
    fireEvent.click(screen.getByRole("button", { name: "Close" }));
    expect(screen.getByText("branch-maintainer", { exact: false })).toBeDefined();
    fireEvent.change(screen.getByLabelText("Branch, tag or commit"), { target: { value: "main" } });
    await waitFor(() => expect(screen.queryByRole("button", { name: "Confirm publication" })).toBeNull());
    expect(screen.queryByText("branch-maintainer", { exact: false })).toBeNull();
    expect(mocked.confirm).not.toHaveBeenCalled();
  });

  it("keeps source coordinates visible without exposing management actions to read-only users", () => {
    mount(false);
    expect(screen.getByRole("link", { name: "https://github.com/acme/reviewer" })).toBeDefined();
    expect(screen.queryByRole("button", { name: "Preview changes" })).toBeNull();
    expect(mocked.preview).not.toHaveBeenCalled();
  });
});
