import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import { useState } from "react";
import { EMPTY_AGENT_DRAFT } from "@multica/core/agents";
import { paths } from "@multica/core/paths";
import { NavigationProvider } from "../../navigation";
import enAgents from "../../locales/en/agents.json";
import { GitCreateAgentPage } from "./git-create-agent-page";

const mocked = vi.hoisted(() => ({ preview: vi.fn(), create: vi.fn(), installations: vi.fn(), push: vi.fn() }));
vi.mock("@multica/core/api", () => ({ api: {
  listGitHubInstallations: mocked.installations,
  listGitHubAgentRepositories: async () => ({ repositories: [] }),
  listGitHubAgentBranches: async () => ({ default_branch: "main", branches: [] }),
  previewGitHubAgent: mocked.preview, createAgentFromPackage: mocked.create,
} }));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "workspace-1" }));
vi.mock("@multica/core/paths", async (original) => ({ ...await original<object>(), useWorkspacePaths: () => paths.workspace("acme") }));
vi.mock("../components/runtime-picker", () => ({ RuntimePicker: () => <div>Runtime picker</div> }));
vi.mock("./use-create-agent-form", () => ({ useCreateAgentForm: () => {
  const [draft, setDraft] = useState({ ...EMPTY_AGENT_DRAFT, runtimeId: "runtime-1" });
  return { draft, setDraft, selectedRuntime: { id: "runtime-1" }, draftReady: true, runtimes: [], members: [], currentUserId: "user-1" };
} }));

function mount() {
  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })}>
    <I18nProvider locale="en" resources={{ en: { agents: enAgents } }}>
      <NavigationProvider value={{ push: mocked.push, replace: vi.fn(), back: vi.fn(), pathname: "/acme/agents/new/git", searchParams: new URLSearchParams(), getShareableUrl: (p) => p }}>
        <GitCreateAgentPage />
      </NavigationProvider>
    </I18nProvider>
  </QueryClientProvider>);
}

beforeEach(() => {
  vi.clearAllMocks();
  mocked.installations.mockResolvedValue({ can_manage: true, installations: [{ id: "install-1", account_login: "acme" }] });
  mocked.preview.mockResolvedValue({ preview_id: "reviewed-version", repository: "acme/agent", ref: "release/v2", resolved_sha: "a".repeat(40), name: "Imported", description: "", instructions: "Reviewed instructions", skills: [], blockers: [], warnings: [] });
  mocked.create.mockResolvedValue({ agent: { id: "agent-1" }, source: { synced_commit_sha: "a".repeat(40) }, warnings: [] });
});

describe("Git creation", () => {
  it("requires a new preview after changing branches and submits only the reviewed ID", async () => {
    mount();
    await waitFor(() => expect(screen.getByLabelText("GitHub connection")).toHaveValue("install-1"));
    fireEvent.change(screen.getByLabelText(enAgents.tab_body.publish.repository), { target: { value: "https://github.com/acme/agent" } });
    fireEvent.change(screen.getByLabelText("Branch, tag or commit"), { target: { value: "release/v2" } });
    expect(screen.getByRole("button", { name: enAgents.creation_studio.create_and_open })).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "Preview agent" }));
    await screen.findByText("Reviewed instructions");
    expect(mocked.preview).toHaveBeenCalledWith("workspace-1", { installation_id: "install-1", repository: "https://github.com/acme/agent", ref: "refs/heads/release/v2" });
    fireEvent.change(screen.getByLabelText("Branch, tag or commit"), { target: { value: "main" } });
    expect(screen.getByRole("button", { name: enAgents.creation_studio.create_and_open })).toBeDisabled();
    expect(screen.queryByText("Reviewed instructions")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Preview agent" }));
    await waitFor(() => expect(screen.getByRole("button", { name: enAgents.creation_studio.create_and_open })).toBeEnabled());
    fireEvent.click(screen.getByRole("button", { name: enAgents.creation_studio.create_and_open }));
    await waitFor(() => expect(mocked.push).toHaveBeenCalledWith("/acme/agents/agent-1"));
    expect(mocked.create).toHaveBeenCalledWith("workspace-1", expect.objectContaining({ preview_id: "reviewed-version", runtime_id: "runtime-1" }));
    expect(mocked.create.mock.calls[0]?.[1]).not.toHaveProperty("repository");
  });

  it("blocks creation for a member without management rights", async () => {
    mocked.installations.mockResolvedValue({ can_manage: false, installations: [] });
    mount();
    await screen.findByText(/workspace owner or admin must connect/);
    expect(screen.getByRole("button", { name: "Preview agent" })).toBeDisabled();
    expect(screen.getByRole("button", { name: enAgents.creation_studio.create_and_open })).toBeDisabled();
    expect(mocked.preview).not.toHaveBeenCalled();
  });
});

it("rejects a malformed preview without enabling creation", async () => {
  mocked.preview.mockResolvedValue({});
  mount();
  await waitFor(() => expect(screen.getByLabelText("GitHub connection")).toHaveValue("install-1"));
  fireEvent.change(screen.getByLabelText(enAgents.tab_body.publish.repository), { target: { value: "acme/agent" } });
  fireEvent.click(screen.getByRole("button", { name: "Preview agent" }));
  await screen.findByRole("alert");
  expect(screen.getByRole("button", { name: enAgents.creation_studio.create_and_open })).toBeDisabled();
  expect(mocked.create).not.toHaveBeenCalled();
});
