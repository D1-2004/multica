// @vitest-environment jsdom

import { describe, expect, it, vi } from "vitest";
import { fireEvent, screen } from "@testing-library/react";
import type { Agent } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import { AgentMessageSettings } from "./agent-message-settings";

const agent = { id: "agent-1" } as Agent;

describe("AgentMessageSettings", () => {
  it("saves each employee communication behavior independently", () => {
    const onUpdate = vi.fn(async () => {});
    renderWithI18n(
      <AgentMessageSettings
        agent={{
          ...agent,
          chat_session_resume: false,
          inbound_coordinator: true,
          task_finished_loop_enabled: false,
        }}
        canEdit
        onUpdate={onUpdate}
      />,
    );

    fireEvent.click(screen.getByLabelText("Resume last session"));
    fireEvent.click(screen.getByLabelText("Judge after the work finishes"));

    expect(onUpdate).toHaveBeenCalledWith({ chat_session_resume: true });
    expect(onUpdate).toHaveBeenCalledWith({
      task_finished_loop_enabled: true,
    });
  });

  it("disables the finished-work judge when inbound judging is off", () => {
    renderWithI18n(
      <AgentMessageSettings
        agent={{
          ...agent,
          inbound_coordinator: false,
          task_finished_loop_enabled: false,
        }}
        canEdit
        onUpdate={vi.fn(async () => {})}
      />,
    );

    expect(
      screen.getByLabelText("Judge after the work finishes"),
    ).toHaveAttribute("aria-disabled", "true");
  });
});
