// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { Agent, DispatchPromptPreview } from "@multica/core/types";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../../locales/en/common.json";
import enAgents from "../../../locales/en/agents.json";
import { DispatchPromptDialog } from "./dispatch-prompt-dialog";

const TEST_RESOURCES = { en: { common: enCommon, agents: enAgents } };

const mockPreview = vi.hoisted(() => vi.fn());

vi.mock("@multica/core/api", () => ({
  api: {
    getAgentDispatchPromptPreview: (id: string, surface: string) =>
      mockPreview(id, surface),
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

function preview(overrides: Partial<DispatchPromptPreview> = {}): DispatchPromptPreview {
  return {
    surface: "auto",
    instruction: "MANAGED POLICY\n\nREPLY RULES",
    segments: [
      {
        id: "policy",
        source: "managed",
        delivery: "runtime_brief",
        customizable: true,
        condition: "dingtalk_dispatch",
        overridden: false,
        included: true,
        managed_text: "MANAGED POLICY",
        effective_text: "MANAGED POLICY",
      },
      {
        id: "context",
        source: "router",
        delivery: "per_turn",
        customizable: false,
        condition: "per_dispatch",
        overridden: false,
        included: false,
        excluded_reason: "not_supplied",
        managed_text: "",
        effective_text: "",
      },
      {
        id: "reply_formatting",
        source: "builtin",
        delivery: "runtime_brief",
        customizable: true,
        condition: "any_dingtalk_task",
        overridden: false,
        included: true,
        managed_text: "REPLY RULES",
        effective_text: "REPLY RULES",
      },
    ],
    runtime_sections: [
      { id: "workspace_context", source: "workspace", customizable: false, origin: "workspace.context" },
    ],
    ...overrides,
  };
}

function renderDialog(
  agent: Partial<Agent> = {},
  onSave = vi.fn().mockResolvedValue(undefined),
  readOnly = false,
) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <QueryClientProvider client={queryClient}>
        <DispatchPromptDialog
          agent={{ ...baseAgent, ...agent }}
          open
          onOpenChange={vi.fn()}
          onSave={onSave}
          readOnly={readOnly}
        />
      </QueryClientProvider>
    </I18nProvider>,
  );
  return onSave;
}

// getAllBy* is `| undefined` per element under noUncheckedIndexedAccess; a
// missing Edit button is a test failure, not a case to handle.
function firstEditButton(): HTMLElement {
  const [first] = screen.getAllByRole("button", { name: /^edit$/i });
  if (!first) throw new Error("no Edit button rendered");
  return first;
}

describe("DispatchPromptDialog", () => {
  beforeEach(() => {
    mockPreview.mockReset();
    mockPreview.mockResolvedValue(preview());
  });

  it("shows the composed structure in order", async () => {
    renderDialog();
    await screen.findByText("Managed policy");
    expect(screen.getByText("Delivery context")).toBeInTheDocument();
    expect(screen.getByText("Reply formatting")).toBeInTheDocument();
  });

  it("marks the per-dispatch delivery context as not customizable", async () => {
    renderDialog();
    await screen.findByText("Managed policy");
    expect(screen.getByText("Cannot be customized")).toBeInTheDocument();
    // Two customizable segments, so two Edit buttons — the context has none.
    expect(screen.getAllByRole("button", { name: /^edit$/i })).toHaveLength(2);
  });

  it("states each segment's injection condition, not just its bytes", async () => {
    renderDialog();
    await screen.findByText("Managed policy");
    expect(
      screen.getByText(/Injected for DingTalk messages, calendar events/i),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/Injected for every DingTalk task/i),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/Resolved by the router for each dispatch/i),
    ).toBeInTheDocument();
  });

  it("marks which segments are active in the previewed mode", async () => {
    renderDialog();
    await screen.findByText("Managed policy");
    expect(screen.getAllByText("Active in this mode")).toHaveLength(2);
    expect(
      screen.getByText(/Not active · resolved per dispatch/i),
    ).toBeInTheDocument();
  });

  it("explains why a Coordinator-created Issue task is not given the policy", async () => {
    const base = preview();
    mockPreview.mockResolvedValueOnce({
      ...base,
      segments: base.segments.map((segment) =>
        segment.id === "policy"
          ? { ...segment, included: false, excluded_reason: "coordinator_issue" }
          : segment,
      ),
    });
    renderDialog();
    await screen.findByText("Managed policy");
    expect(
      screen.getByText(/Not active · not used for Coordinator-created Issues/i),
    ).toBeInTheDocument();
  });

  it("says up front that Multica-created work never receives this", async () => {
    renderDialog();
    expect(
      await screen.findByText(/Issues and chats you create inside Multica/i),
    ).toBeInTheDocument();
  });

  it("keeps segment text collapsed until asked", async () => {
    renderDialog();
    await screen.findByText("Managed policy");
    expect(screen.queryByText("MANAGED POLICY")).not.toBeInTheDocument();

    fireEvent.click(screen.getAllByRole("button", { name: /show content/i })[0]!);
    expect(await screen.findByText("MANAGED POLICY")).toBeInTheDocument();
  });

  it("re-requests the preview when the mode changes", async () => {
    renderDialog();
    await screen.findByText("Managed policy");
    expect(mockPreview).toHaveBeenCalledWith("agent-1", "auto");

    fireEvent.click(screen.getByRole("button", { name: /^task$/i }));
    await waitFor(() =>
      expect(mockPreview).toHaveBeenCalledWith("agent-1", "issue"),
    );
  });

  it("seeds the editor with the managed text rather than a blank field", async () => {
    renderDialog();
    await screen.findByText("Managed policy");
    fireEvent.click(firstEditButton());
    expect(await screen.findByLabelText("Managed policy")).toHaveValue(
      "MANAGED POLICY",
    );
  });

  it("saves one segment without disturbing the others", async () => {
    const onSave = renderDialog({
      dispatch_prompt_overrides: { reply_formatting: "MY REPLY RULES" },
    });
    await screen.findByText("Managed policy");
    fireEvent.click(firstEditButton());
    const editor = await screen.findByLabelText("Managed policy");
    fireEvent.change(editor, { target: { value: "MY POLICY" } });
    fireEvent.click(screen.getByRole("button", { name: /^save$/i }));

    expect(onSave).toHaveBeenCalledWith({
      dispatch_prompt_overrides: {
        reply_formatting: "MY REPLY RULES",
        policy: "MY POLICY",
      },
    });
  });

  it("restores one segment by dropping only its key", async () => {
    mockPreview.mockResolvedValue(
      preview({
        segments: [
          {
            id: "policy",
            source: "managed",
            delivery: "runtime_brief",
            customizable: true,
            condition: "dingtalk_dispatch",
            overridden: true,
            included: true,
            managed_text: "MANAGED POLICY",
            effective_text: "MY POLICY",
          },
        ],
      }),
    );
    const onSave = renderDialog({
      dispatch_prompt_overrides: {
        policy: "MY POLICY",
        reply_formatting: "MY REPLY RULES",
      },
    });
    await screen.findByText("Managed policy");
    fireEvent.click(screen.getByRole("button", { name: /^edit$/i }));
    fireEvent.click(
      await screen.findByRole("button", { name: /restore managed/i }),
    );

    expect(onSave).toHaveBeenCalledWith({
      dispatch_prompt_overrides: { reply_formatting: "MY REPLY RULES" },
    });
  });

  it("lists the runtime-composed sections as not customizable here", async () => {
    renderDialog();
    await screen.findByText("Also assembled by the runtime");
    expect(screen.getByText(/workspace_context/)).toBeInTheDocument();
  });

  it("commits the new-task switch without an explicit save", async () => {
    const onSave = renderDialog();
    await screen.findByText("Managed policy");
    fireEvent.click(screen.getByRole("switch"));
    expect(onSave).toHaveBeenCalledWith({ dispatch_always_new_issue: true });
  });

  it("renders an explanatory empty state when the backend has no preview", async () => {
    mockPreview.mockResolvedValue(
      preview({ segments: [], runtime_sections: [], instruction: "" }),
    );
    renderDialog();
    expect(
      await screen.findByText(/does not expose the prompt structure/i),
    ).toBeInTheDocument();
  });

  it("offers no editing affordance when read-only", async () => {
    renderDialog({}, vi.fn(), true);
    await screen.findByText("Managed policy");
    expect(
      screen.queryByRole("button", { name: /^edit$/i }),
    ).not.toBeInTheDocument();
    expect(screen.getByRole("switch")).toHaveAttribute("aria-disabled", "true");
  });
});
