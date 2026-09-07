// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Agent } from "@multica/core/types";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../../locales/en/common.json";
import enAgents from "../../../locales/en/agents.json";

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

  it("keeps employee voice out of task instructions", () => {
    renderTab();

    expect(screen.getByLabelText(/System prompt/i)).toBeInTheDocument();
    expect(screen.queryByLabelText("Persona")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Reply tone")).not.toBeInTheDocument();
  });

  it("saves only the System Prompt", async () => {
    const user = userEvent.setup();
    const { onSave } = renderTab();

    await user.clear(screen.getByLabelText(/System prompt/i));
    await user.type(screen.getByLabelText(/System prompt/i), "New prompt");
    await user.click(screen.getByRole("button", { name: "Save" }));

    expect(onSave).toHaveBeenCalledWith({ instructions: "New prompt" });
  });

  it("keeps source-managed instructions read-only", () => {
    renderTab(
      { instructions: "managed" },
      vi.fn().mockResolvedValue(undefined),
      { instructionsLocked: true },
    );

    expect(screen.getByLabelText(/System prompt/i)).toBeDisabled();
    expect(screen.queryByRole("button", { name: "Save" })).not.toBeInTheDocument();
  });
});
