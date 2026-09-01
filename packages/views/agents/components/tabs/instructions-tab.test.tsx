// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Agent } from "@multica/core/types";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../../locales/en/common.json";
import enAgents from "../../../locales/en/agents.json";

const extractAgentVoice = vi.fn();

vi.mock("@multica/core/api", () => ({
  api: {
    extractAgentVoice: (...args: unknown[]) => extractAgentVoice(...args),
  },
}));

vi.mock("sonner", () => ({
  toast: {
    error: vi.fn(),
    success: vi.fn(),
  },
}));

import { InstructionsTab } from "./instructions-tab";

const TEST_RESOURCES = { en: { common: enCommon, agents: enAgents } };

const baseAgent: Agent = {
  id: "agent-1",
  workspace_id: "ws-1",
  runtime_id: "runtime-1",
  name: "Agent",
  description: "",
  instructions: "You are a frontend engineer.",
  persona: "",
  reply_tone: "",
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

function renderTab(
  overrides: Partial<Agent> = {},
  onSave = vi.fn().mockResolvedValue(undefined),
  extras: { readOnly?: boolean; instructionsLocked?: boolean } = {},
) {
  const result = render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <InstructionsTab
        agent={{ ...baseAgent, ...overrides }}
        onSave={onSave}
        readOnly={extras.readOnly}
        instructionsLocked={extras.instructionsLocked}
      />
    </I18nProvider>,
  );
  return { ...result, onSave };
}

describe("InstructionsTab", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("applies a persona template", async () => {
    const user = userEvent.setup();
    renderTab();

    await user.click(screen.getByRole("button", { name: "Teammate" }));
    expect(
      screen.getByDisplayValue(/You are a reliable teammate/),
    ).toBeInTheDocument();
  });

  it("saves persona and tone without rewriting github-managed instructions", async () => {
    const user = userEvent.setup();
    const { onSave } = renderTab(
      { instructions: "managed", persona: "", reply_tone: "" },
      vi.fn().mockResolvedValue(undefined),
      { instructionsLocked: true },
    );

    await user.click(screen.getByRole("button", { name: "Direct" }));
    await user.click(screen.getByRole("button", { name: "Save" }));

    expect(onSave).toHaveBeenCalledWith(
      expect.objectContaining({
        reply_tone: expect.stringContaining("Short sentences"),
      }),
    );
    expect(onSave.mock.calls[0][0].instructions).toBeUndefined();
  });

  it("extracts persona and tone from the current instructions", async () => {
    const user = userEvent.setup();
    extractAgentVoice.mockResolvedValue({
      persona: "A calm coordinator.",
      reply_tone: "Direct.",
    });
    renderTab();

    await user.click(
      screen.getByRole("button", { name: "Extract from instructions" }),
    );

    expect(extractAgentVoice).toHaveBeenCalledWith(
      "agent-1",
      "You are a frontend engineer.",
    );
    expect(screen.getByDisplayValue("A calm coordinator.")).toBeInTheDocument();
    expect(screen.getByDisplayValue("Direct.")).toBeInTheDocument();
  });
});
