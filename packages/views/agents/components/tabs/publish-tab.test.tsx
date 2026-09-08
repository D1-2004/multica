import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import type { AgentSource, AgentSourceSyncPreview } from "@multica/core/types";
import enAgents from "../../../locales/en/agents.json";
import { PublishTab } from "./publish-tab";

const mocked = vi.hoisted(() => ({
  branches: vi.fn(), preview: vi.fn(), confirm: vi.fn(), error: vi.fn(), success: vi.fn(),
}));
vi.mock("@multica/core/api", () => ({ api: {
  listAgentSourceBranches: mocked.branches,
  previewAgentSourceSync: mocked.preview,
  syncAgentSource: mocked.confirm,
} }));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "workspace-1" }));
vi.mock("sonner", () => ({ toast: { error: mocked.error, success: mocked.success, warning: vi.fn() } }));

const source: AgentSource = {
  agent_id: "agent-1", source_type: "github", installation_id: "installation-1",
  repository: "acme/reviewer", ref: "main", manifest_path: "agent.json",
  synced_commit_sha: "a".repeat(40), sync_status: "ready", github_connected: true, can_sync: true,
  last_sync_error: null, last_sync_attempt_at: null, last_synced_at: "",
};
const preview: AgentSourceSyncPreview = {
  preview_id: "preview-1", expires_at: "2099-01-01T00:00:00Z", repository_url: "https://github.com/acme/reviewer",
  ref: "release/v2", base_sha: "a".repeat(40), resolved_sha: "b".repeat(40), changed: true, warnings: [],
  git_changes: [{ path: "agent/AGENTS.md", status: "modified", before: "old instructions", after: "new instructions" }],
  configuration_changes: [{ path: "instructions", status: "modified", before: "old instructions", after: "new instructions" }],
};

function mount(canEdit = true) {
  return render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })}>
    <I18nProvider locale="en" resources={{ en: { agents: enAgents } }}>
      <PublishTab source={source} canEdit={canEdit} />
    </I18nProvider>
  </QueryClientProvider>);
}

beforeEach(() => {
  vi.clearAllMocks();
  mocked.branches.mockResolvedValue({ repository: "acme/reviewer", repository_url: "https://github.com/acme/reviewer", default_branch: "main", branches: [] });
  mocked.preview.mockResolvedValue(preview);
  mocked.confirm.mockResolvedValue({ source: { ...source, ref: "release/v2", synced_commit_sha: "b".repeat(40) }, changed: true, warnings: [] });
});

describe("Git source import and export tab", () => {
  it("previews the selected branch and publishes only after explicit confirmation", async () => {
    mount();
    fireEvent.change(screen.getByLabelText("Branch or commit"), { target: { value: "release/v2" } });
    fireEvent.click(screen.getByRole("button", { name: "Preview changes" }));
    await screen.findByRole("button", { name: "Confirm publication" });
    expect(mocked.preview).toHaveBeenCalledWith("agent-1", "release/v2");
    expect(mocked.confirm).not.toHaveBeenCalled();
    expect(screen.getByText("Git file changes (1)")).toBeDefined();
    fireEvent.click(screen.getByRole("button", { name: "Confirm publication" }));
    await waitFor(() => expect(mocked.confirm).toHaveBeenCalledWith("agent-1", "preview-1", { secrets: {}, deferred_bindings: [] }));
  });

  it("invalidates a preview when the selected branch changes", async () => {
    mount();
    fireEvent.change(screen.getByLabelText("Branch or commit"), { target: { value: "release/v2" } });
    fireEvent.click(screen.getByRole("button", { name: "Preview changes" }));
    await screen.findByRole("button", { name: "Confirm publication" });
    fireEvent.change(screen.getByLabelText("Branch or commit"), { target: { value: "main" } });
    await waitFor(() => expect(screen.queryByRole("button", { name: "Confirm publication" })).toBeNull());
    expect(mocked.confirm).not.toHaveBeenCalled();
  });

  it("keeps source coordinates visible without exposing management actions to read-only users", () => {
    mount(false);
    expect(screen.getByRole("link", { name: "https://github.com/acme/reviewer" })).toBeDefined();
    expect(screen.queryByRole("button", { name: "Preview changes" })).toBeNull();
    expect(mocked.preview).not.toHaveBeenCalled();
  });
});
