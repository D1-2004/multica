// @vitest-environment jsdom

import { describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import type { Agent } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import { AgentMessageSettings, InboundCoordinatorSetting } from "./agent-message-settings";

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
    fireEvent.click(screen.getByLabelText("Show AI sender label"));

    expect(onUpdate).toHaveBeenCalledWith({ chat_session_resume: true });
    expect(onUpdate).toHaveBeenCalledWith({
      task_finished_loop_enabled: true,
    });
    expect(onUpdate).toHaveBeenCalledWith({ dingtalk_show_ai_tag: true });
  });

  it("hides the finished-work judge when inbound judging is off", () => {
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
      screen.queryByLabelText("Judge after the work finishes"),
    ).not.toBeInTheDocument();
  });

  it("defaults the AI sender label to off and respects read-only access", () => {
    const onUpdate = vi.fn(async () => {});
    renderWithI18n(
      <AgentMessageSettings agent={agent} canEdit={false} onUpdate={onUpdate} />,
    );

    const toggle = screen.getByLabelText("Show AI sender label");
    expect(toggle).not.toBeChecked();
    expect(toggle).toHaveAttribute("aria-disabled", "true");
    fireEvent.click(toggle);
    expect(onUpdate).not.toHaveBeenCalled();
  });

  it("restores the AI sender label after a failed save", async () => {
    const onUpdate = vi.fn(async () => {
      throw new Error("save failed");
    });
    renderWithI18n(
      <AgentMessageSettings
        agent={{ ...agent, dingtalk_show_ai_tag: true }}
        canEdit
        onUpdate={onUpdate}
      />,
    );

    const toggle = screen.getByLabelText("Show AI sender label");
    fireEvent.click(toggle);
    expect(onUpdate).toHaveBeenCalledWith({ dingtalk_show_ai_tag: false });
    await waitFor(() => {
      expect(toggle).toBeChecked();
      expect(toggle).not.toHaveAttribute("aria-disabled", "true");
    });
  });

  it.each([false, true])("saves unified responses independently from %j", async (enabled) => {
    const onUpdate = vi.fn(async () => {});
    renderWithI18n(
      <AgentMessageSettings
        agent={{ ...agent, inbound_coordinator: false, dingtalk_response_enabled: enabled, dingtalk_show_ai_tag: true }}
        canEdit
        onUpdate={onUpdate}
      />,
    );

    const toggle = screen.getByLabelText("Unified responses");
    fireEvent.click(toggle);
    expect(onUpdate).toHaveBeenCalledExactlyOnceWith({ dingtalk_response_enabled: !enabled });
    expect(screen.getByLabelText("Show AI sender label")).toBeChecked();
    await waitFor(() => expect(toggle).not.toHaveAttribute("aria-disabled", "true"));
    expect(toggle).toHaveAttribute("aria-checked", String(!enabled));
  });

  it("defaults unified responses to off and respects read-only access", () => {
    const onUpdate = vi.fn(async () => {});
    renderWithI18n(
      <AgentMessageSettings agent={agent} canEdit={false} onUpdate={onUpdate} />,
    );

    const toggle = screen.getByLabelText("Unified responses");
    expect(toggle).not.toBeChecked();
    expect(toggle).toHaveAttribute("aria-disabled", "true");
    fireEvent.click(toggle);
    expect(onUpdate).not.toHaveBeenCalled();
  });

  it.each([false, true])("restores unified responses to %j after a failed save", async (enabled) => {
    const onUpdate = vi.fn(async () => { throw new Error("save failed"); });
    renderWithI18n(
      <AgentMessageSettings
        agent={{ ...agent, dingtalk_response_enabled: enabled }}
        canEdit
        onUpdate={onUpdate}
      />,
    );

    const toggle = screen.getByLabelText("Unified responses");
    fireEvent.click(toggle);
    expect(onUpdate).toHaveBeenCalledExactlyOnceWith({ dingtalk_response_enabled: !enabled });
    await waitFor(() => {
      expect(toggle).toHaveAttribute("aria-checked", String(enabled));
      expect(toggle).not.toHaveAttribute("aria-disabled", "true");
    });
    expect(screen.getByLabelText("Show AI sender label")).not.toBeChecked();
  });

});


describe("Proactive conversation setting", () => {
  it.each([
    [false, false, "Proactively process all new conversation messages", { event_trigger_enabled: true, inbound_coordinator: true }],
    [true, true, "Proactively process all new conversation messages", { event_trigger_enabled: false }],
    [true, true, "Judge before sandbox", { inbound_coordinator: false, inbound_coordinator_user_decision_mode: "off", event_trigger_enabled: false }],
  ] as const)("preserves the inbound dependency for %j / %j", (inbound, proactive, label, expected) => {
    const onUpdate = vi.fn(async () => {});
    renderWithI18n(<InboundCoordinatorSetting agent={{ ...agent, inbound_coordinator: inbound, event_trigger_enabled: proactive }} canEdit onUpdate={onUpdate} />);
    const toggles = screen.getAllByRole("switch");
    expect(toggles[0]).toHaveAccessibleName("Judge before sandbox");
    expect(toggles[1]).toHaveAccessibleName("Proactively process all new conversation messages");
    fireEvent.click(screen.getByLabelText(label));
    expect(onUpdate).toHaveBeenCalledExactlyOnceWith(expected);
  });
});


describe("User decision audience", () => {
  it.each(["off", "all", "named"] as const)("shows the saved %s mode and its consequences", (mode) => {
    const onUpdate = vi.fn(async () => {});
    renderWithI18n(<InboundCoordinatorSetting agent={{ ...agent, inbound_coordinator: true, inbound_coordinator_user_decision_mode: mode }} canEdit onUpdate={onUpdate} />);
    expect(screen.getAllByRole("radio")).toHaveLength(3);
    expect(screen.getByRole("radio", { name: { off: "Off", all: "Everyone", named: "Named people" }[mode] })).toBeChecked();
    expect(screen.getByText("Handle all requests automatically, without choice cards.")).toBeInTheDocument();
    expect(screen.getByText("Everyone receives a choice card; handling waits for their submission.")).toBeInTheDocument();
    expect(screen.getByText("Listed people choose first; other requests are handled automatically.")).toBeInTheDocument();
    expect(!!screen.queryByRole("textbox")).toBe(mode === "named");
    expect(onUpdate).not.toHaveBeenCalled();
  });

  it.each([false, true])("initializes legacy mode %j without enabling everyone", (enabled) => {
    renderWithI18n(<InboundCoordinatorSetting agent={{ ...agent, inbound_coordinator: true, inbound_coordinator_user_decision: enabled }} canEdit onUpdate={vi.fn(async () => {})} />);
    expect(screen.getByRole("radio", { name: enabled ? "Named people" : "Off" })).toBeChecked();
  });

  it("saves a mode alone and follows the returned server state", async () => {
    const onUpdate = vi.fn(async () => {});
    const initialAgent: Agent = { ...agent, inbound_coordinator: true, inbound_coordinator_user_decision_mode: "named", inbound_coordinator_user_decision_names: ["Alice"] };
    const { rerender } = renderWithI18n(<InboundCoordinatorSetting agent={initialAgent} canEdit onUpdate={onUpdate} />);
    fireEvent.click(screen.getByRole("radio", { name: "Everyone" }));
    expect(onUpdate).toHaveBeenCalledExactlyOnceWith({ inbound_coordinator_user_decision_mode: "all" });
    await waitFor(() => expect(screen.getByRole("radio", { name: "Everyone" })).not.toHaveAttribute("aria-disabled", "true"));
    expect(screen.getByRole("radio", { name: "Named people" })).toBeChecked();
    rerender(<InboundCoordinatorSetting agent={{ ...initialAgent, inbound_coordinator_user_decision_mode: "all" }} canEdit onUpdate={onUpdate} />);
    expect(screen.getByRole("radio", { name: "Everyone" })).toBeChecked();
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
  });

  it("keeps the saved mode and names after a failed mode save", async () => {
    const onUpdate = vi.fn(async () => { throw new Error("save failed"); });
    renderWithI18n(<InboundCoordinatorSetting agent={{ ...agent, inbound_coordinator: true, inbound_coordinator_user_decision_mode: "named", inbound_coordinator_user_decision_names: ["Alice"] }} canEdit onUpdate={onUpdate} />);
    fireEvent.click(screen.getByRole("radio", { name: "Off" }));
    await waitFor(() => expect(screen.getByRole("radio", { name: "Off" })).not.toHaveAttribute("aria-disabled", "true"));
    expect(screen.getByRole("radio", { name: "Named people" })).toBeChecked();
    expect(screen.getByRole("textbox")).toHaveValue("Alice");
  });

  it("saves trimmed unique names with named mode", async () => {
    const onUpdate = vi.fn(async () => {});
    const initialAgent: Agent = { ...agent, inbound_coordinator: true, inbound_coordinator_user_decision_mode: "named" };
    const { rerender } = renderWithI18n(<InboundCoordinatorSetting agent={initialAgent} canEdit onUpdate={onUpdate} />);
    fireEvent.change(screen.getByRole("textbox"), { target: { value: " 冬翔 \nAlice\n冬翔\n" } });
    fireEvent.click(screen.getByRole("button", { name: "Save names" }));
    expect(onUpdate).toHaveBeenCalledExactlyOnceWith({ inbound_coordinator_user_decision_mode: "named", inbound_coordinator_user_decision_names: ["冬翔", "Alice"] });
    rerender(<InboundCoordinatorSetting agent={{ ...initialAgent, inbound_coordinator_user_decision_names: ["冬翔", "Alice"] }} canEdit onUpdate={onUpdate} />);
    await waitFor(() => expect(screen.getByRole("textbox")).toHaveValue("冬翔\nAlice"));
  });

  it("sends an explicit empty list without enabling everyone", async () => {
    const onUpdate = vi.fn(async () => {});
    renderWithI18n(<InboundCoordinatorSetting agent={{ ...agent, inbound_coordinator: true, inbound_coordinator_user_decision_mode: "named", inbound_coordinator_user_decision_names: ["Alice"] }} canEdit onUpdate={onUpdate} />);
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "" } });
    fireEvent.click(screen.getByRole("button", { name: "Save names" }));
    expect(onUpdate).toHaveBeenCalledExactlyOnceWith({ inbound_coordinator_user_decision_mode: "named", inbound_coordinator_user_decision_names: [] });
    await waitFor(() => expect(screen.getByRole("button", { name: "Save names" })).not.toBeDisabled());
  });

  it("preserves the draft names for retry after a failed save", async () => {
    const onUpdate = vi.fn(async () => { throw new Error("save failed"); });
    renderWithI18n(<InboundCoordinatorSetting agent={{ ...agent, inbound_coordinator: true, inbound_coordinator_user_decision_mode: "named", inbound_coordinator_user_decision_names: ["Alice"] }} canEdit onUpdate={onUpdate} />);
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "Bob" } });
    fireEvent.click(screen.getByRole("button", { name: "Save names" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Save names" })).not.toBeDisabled());
    expect(screen.getByRole("textbox")).toHaveValue("Bob");
    expect(screen.getByRole("radio", { name: "Named people" })).toBeChecked();
  });

  it.each([[false, true], [true, false]])("prevents edits with permission %j and coordinator %j", (canEdit, inbound) => {
    const onUpdate = vi.fn(async () => {});
    renderWithI18n(<InboundCoordinatorSetting agent={{ ...agent, inbound_coordinator: inbound, inbound_coordinator_user_decision_mode: "named", inbound_coordinator_user_decision_names: ["Alice"] }} canEdit={canEdit} onUpdate={onUpdate} />);
    expect(screen.getByRole("textbox")).toBeDisabled();
    expect(screen.getByRole("button", { name: "Save names" })).toBeDisabled();
    fireEvent.click(screen.getByRole("radio", { name: "Everyone" }));
    expect(onUpdate).not.toHaveBeenCalled();
    expect(screen.getByRole("radio", { name: "Named people" })).toBeChecked();
  });
});
