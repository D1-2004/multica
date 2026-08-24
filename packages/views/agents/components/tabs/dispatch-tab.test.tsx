// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { Agent } from "@multica/core/types";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../../locales/en/common.json";
import enAgents from "../../../locales/en/agents.json";
import { DispatchTab } from "./dispatch-tab";

const TEST_RESOURCES = { en: { common: enCommon, agents: enAgents } };

const MANAGED_POLICY = "MANAGED COMMON POLICY";

const mockGetDefault = vi.hoisted(() => vi.fn());

vi.mock("@multica/core/api", () => ({
  api: {
    getAgentDispatchPromptDefault: (id: string) => mockGetDefault(id),
  },
}));

const baseAgent: Agent = {
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
};

function TestShell({ children }: { children: React.ReactNode }) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return (
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    </I18nProvider>
  );
}

function renderTab(
  overrides: Partial<Agent> = {},
  onSave = vi.fn().mockResolvedValue(undefined),
  readOnly = false,
  onDirtyChange?: (dirty: boolean) => void,
) {
  render(
    <TestShell>
      <DispatchTab
        agent={{ ...baseAgent, ...overrides }}
        onSave={onSave}
        readOnly={readOnly}
        onDirtyChange={onDirtyChange}
      />
    </TestShell>,
  );
  return onSave;
}

function promptBox() {
  return screen.getByLabelText(/custom dispatch prompt/i);
}

describe("DispatchTab", () => {
  beforeEach(() => {
    mockGetDefault.mockReset();
    mockGetDefault.mockResolvedValue({ prompt: MANAGED_POLICY });
  });

  it("seeds an unset editor with the managed policy instead of a blank field", async () => {
    renderTab();
    // Seeding is the whole safety story: an author starting from an empty box
    // silently drops the managed injection defenses and truthfulness clauses.
    await waitFor(() => expect(promptBox()).toHaveValue(MANAGED_POLICY));
    expect(
      screen.getByText(/seeded with the managed policy/i),
    ).toBeInTheDocument();
  });

  it("does not report a seeded editor as dirty", async () => {
    const onDirtyChange = vi.fn();
    renderTab({}, vi.fn().mockResolvedValue(undefined), false, onDirtyChange);
    await waitFor(() => expect(promptBox()).toHaveValue(MANAGED_POLICY));
    // Merely opening the tab must not block a tab switch.
    expect(onDirtyChange).toHaveBeenLastCalledWith(false);
    expect(screen.getByRole("button", { name: /^save$/i })).toBeDisabled();
  });

  it("keeps the editor blank when the deployment configures no managed policy", async () => {
    mockGetDefault.mockResolvedValue({ prompt: "" });
    renderTab();
    await waitFor(() =>
      expect(
        screen.getByText(/uses the managed instruction/i),
      ).toBeInTheDocument(),
    );
    expect(promptBox()).toHaveValue("");
  });

  it("still renders when the default endpoint is unavailable", async () => {
    mockGetDefault.mockRejectedValue(new Error("older backend"));
    renderTab();
    await waitFor(() => expect(promptBox()).toBeInTheDocument());
    expect(promptBox()).toHaveValue("");
  });

  it("shows a stored override rather than the managed seed", async () => {
    renderTab({ dispatch_prompt: "MY POLICY" });
    await waitFor(() => expect(promptBox()).toHaveValue("MY POLICY"));
    expect(
      screen.getByText(/managed policy updates no longer reach it/i),
    ).toBeInTheDocument();
  });

  it("enables save once the seeded text is edited", async () => {
    renderTab();
    await waitFor(() => expect(promptBox()).toHaveValue(MANAGED_POLICY));
    const save = screen.getByRole("button", { name: /^save$/i });
    expect(save).toBeDisabled();

    fireEvent.change(promptBox(), {
      target: { value: MANAGED_POLICY + "\n\n额外规则" },
    });
    expect(save).toBeEnabled();
  });

  it("saves the edited seed as this agent's own prompt", async () => {
    const onSave = renderTab();
    await waitFor(() => expect(promptBox()).toHaveValue(MANAGED_POLICY));
    const authored = MANAGED_POLICY + "\n\n额外规则";
    fireEvent.change(promptBox(), { target: { value: authored } });
    fireEvent.click(screen.getByRole("button", { name: /^save$/i }));
    expect(onSave).toHaveBeenCalledWith({ dispatch_prompt: authored });
  });

  it("restores the managed policy by clearing the override", async () => {
    const onSave = renderTab({ dispatch_prompt: "MY POLICY" });
    await waitFor(() => expect(promptBox()).toHaveValue("MY POLICY"));
    fireEvent.click(screen.getByRole("button", { name: /restore managed/i }));
    // An explicit empty string, not an omitted field — the server reads that
    // as a real clear.
    expect(onSave).toHaveBeenCalledWith({ dispatch_prompt: "" });
  });

  it("offers restore only when an override is actually stored", async () => {
    renderTab();
    await waitFor(() => expect(promptBox()).toHaveValue(MANAGED_POLICY));
    expect(
      screen.queryByRole("button", { name: /restore managed/i }),
    ).not.toBeInTheDocument();
  });

  it("defaults the new-Issue switch to off when the backend omits the field", () => {
    renderTab();
    expect(screen.getByRole("switch")).not.toBeChecked();
  });

  it("commits the new-Issue switch on change without an explicit save", () => {
    const onSave = renderTab();
    fireEvent.click(screen.getByRole("switch"));
    expect(onSave).toHaveBeenCalledWith({ dispatch_always_new_issue: true });
  });

  it("turns the new-Issue switch back off", () => {
    const onSave = renderTab({ dispatch_always_new_issue: true });
    expect(screen.getByRole("switch")).toBeChecked();
    fireEvent.click(screen.getByRole("switch"));
    expect(onSave).toHaveBeenCalledWith({ dispatch_always_new_issue: false });
  });

  it("hides save and disables both controls when read-only", () => {
    renderTab({ dispatch_prompt: "MY POLICY" }, vi.fn(), true);
    expect(
      screen.queryByRole("button", { name: /^save$/i }),
    ).not.toBeInTheDocument();
    expect(promptBox()).toBeDisabled();
    // Base UI renders the switch as a span, so disabled is expressed through
    // ARIA rather than the native attribute toBeDisabled() looks for.
    expect(screen.getByRole("switch")).toHaveAttribute("aria-disabled", "true");
    // A read-only viewer must not trigger a fetch of deployment policy.
    expect(mockGetDefault).not.toHaveBeenCalled();
  });
});
