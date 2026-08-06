// @vitest-environment jsdom

import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { Agent } from "@multica/core/types";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../../locales/en/common.json";
import enAgents from "../../../locales/en/agents.json";
import { LLMTraceTab } from "./llm-trace-tab";

const TEST_RESOURCES = { en: { common: enCommon, agents: enAgents } };

const agent: Agent = {
  id: "agent-1",
  workspace_id: "ws-1",
  runtime_id: "runtime-1",
  name: "Agent",
  description: "",
  instructions: "",
  avatar_url: null,
  runtime_mode: "cloud",
  runtime_config: {
    gateway: { token: "***" },
    llm_trace: { enabled: false, sink_url: "https://old.example.test" },
  },
  custom_args: [],
  visibility: "workspace",
  permission_mode: "public_to",
  invocation_targets: [{ target_type: "workspace", target_id: null }],
  status: "idle",
  max_concurrent_tasks: 1,
  model: "",
  owner_id: "user-1",
  skills: [],
  created_at: "2026-08-07T00:00:00Z",
  updated_at: "2026-08-07T00:00:00Z",
  archived_at: null,
  archived_by: null,
};

function renderTab(onSave = vi.fn().mockResolvedValue(undefined)) {
  render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <LLMTraceTab agent={agent} onSave={onSave} />
    </I18nProvider>,
  );
  return onSave;
}

describe("LLMTraceTab", () => {
  it("shows the receiver input only while the trace switch is enabled", () => {
    renderTab();

    expect(screen.queryByLabelText("Trace receiver URL")).not.toBeInTheDocument();

    fireEvent.click(
      screen.getByRole("switch", { name: "Enable LLM request trace" }),
    );
    expect(screen.getByLabelText("Trace receiver URL")).toBeInTheDocument();

    fireEvent.click(
      screen.getByRole("switch", { name: "Enable LLM request trace" }),
    );
    expect(screen.queryByLabelText("Trace receiver URL")).not.toBeInTheDocument();
  });

  it("saves the enabled switch and receiver URL without dropping other config", async () => {
    const onSave = renderTab();

    fireEvent.click(
      screen.getByRole("switch", { name: "Enable LLM request trace" }),
    );
    fireEvent.change(screen.getByLabelText("Trace receiver URL"), {
      target: { value: "https://trace.example.test/ingest" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => {
      expect(onSave).toHaveBeenCalledWith({
        runtime_config: {
          gateway: { token: "***" },
          llm_trace: {
            enabled: true,
            sink_url: "https://trace.example.test/ingest",
          },
        },
      });
    });
  });
});
