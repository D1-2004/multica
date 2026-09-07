// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Agent } from "@multica/core/types";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/en/common.json";
import enAgents from "../../locales/en/agents.json";

const extractAgentVoice = vi.fn();

vi.mock("@multica/core/api", () => ({
  api: {
    extractAgentVoice: (...args: unknown[]) => extractAgentVoice(...args),
  },
}));

vi.mock("sonner", () => ({
  toast: { error: vi.fn() },
}));

import { AgentVoiceSettings } from "./agent-voice-settings";

const resources = { en: { common: enCommon, agents: enAgents } };
const agent = {
  id: "agent-1",
  instructions: "You are a frontend engineer.",
  persona: "",
  reply_tone: "",
} as Agent;

function renderVoice(onSave = vi.fn().mockResolvedValue(undefined)) {
  render(
    <I18nProvider locale="en" resources={resources}>
      <AgentVoiceSettings
        agent={agent}
        canEdit
        onSave={onSave}
      />
    </I18nProvider>,
  );
  return onSave;
}

describe("AgentVoiceSettings", () => {
  beforeEach(() => vi.clearAllMocks());

  it("applies and saves voice templates independently from instructions", async () => {
    const user = userEvent.setup();
    const onSave = renderVoice();

    await user.click(screen.getByRole("button", { name: "Teammate" }));
    await user.click(screen.getByRole("button", { name: "Direct" }));
    await user.click(screen.getByRole("button", { name: "Save" }));

    expect(onSave).toHaveBeenCalledWith({
      persona: expect.stringContaining("reliable teammate"),
      reply_tone: expect.stringContaining("Short sentences"),
    });
  });

  it("extracts voice from the saved System Prompt", async () => {
    const user = userEvent.setup();
    extractAgentVoice.mockResolvedValue({
      persona: "A calm coordinator.",
      reply_tone: "Direct.",
    });
    renderVoice();

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
