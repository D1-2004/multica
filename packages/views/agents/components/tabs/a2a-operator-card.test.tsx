// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { AgentA2AOperatorConfig } from "@multica/core/types";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../../locales/en/common.json";
import enAgents from "../../../locales/en/agents.json";

const operatorRef = vi.hoisted(() => ({
  current: null as AgentA2AOperatorConfig | null,
}));
const updateIdentitySpy = vi.hoisted(() => vi.fn());
const deleteIdentitySpy = vi.hoisted(() => vi.fn());
const updateForwardSpy = vi.hoisted(() => vi.fn());

vi.mock("@tanstack/react-query", () => ({
  useQuery: () => ({ data: operatorRef.current }),
  queryOptions: <T,>(options: T) => options,
}));

vi.mock("@multica/core/agent-a2a", () => ({
  agentA2AOperatorConfigOptions: () => ({ queryKey: ["agent-a2a-operator"] }),
  useUpdateAgentA2AOperatorIdentity: () => ({ mutateAsync: updateIdentitySpy, isPending: false }),
  useDeleteAgentA2AOperatorIdentity: () => ({ mutateAsync: deleteIdentitySpy, isPending: false }),
  useUpdateAgentA2AOperatorForward: () => ({ mutateAsync: updateForwardSpy, isPending: false }),
  useDeleteAgentA2AOperatorForward: () => ({ mutateAsync: vi.fn(), isPending: false }),
}));

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

import { A2AOperatorCard } from "./a2a-operator-card";

const TEST_RESOURCES = { en: { common: enCommon, agents: enAgents } };

function renderCard() {
  return render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <A2AOperatorCard wsId="ws-1" agentId="agent-1" />
    </I18nProvider>,
  );
}

describe("A2AOperatorCard", () => {
  beforeEach(() => {
    operatorRef.current = null;
    updateIdentitySpy.mockReset().mockResolvedValue({});
    deleteIdentitySpy.mockReset().mockResolvedValue({});
    updateForwardSpy.mockReset().mockResolvedValue({});
  });

  it("renders nothing for non-operators", () => {
    operatorRef.current = {
      operator: false,
      dwsIdentity: null,
      forward: null,
      forwardAllowedOrigins: [],
    };
    const { container } = renderCard();
    expect(container).toBeEmptyDOMElement();
  });

  it("binds a digital employee identity only with decimal ids", async () => {
    operatorRef.current = {
      operator: true,
      dwsIdentity: null,
      forward: null,
      forwardAllowedOrigins: [],
    };
    const user = userEvent.setup();
    renderCard();

    expect(screen.getByText(enAgents.tab_body.a2a.operator.title)).toBeInTheDocument();
    expect(screen.getByText(enAgents.tab_body.a2a.operator.forward_disabled)).toBeInTheDocument();
    const bind = screen.getByRole("button", { name: enAgents.tab_body.a2a.operator.save_identity });
    expect(bind).toBeDisabled();

    await user.type(screen.getByLabelText("UID"), "0123");
    await user.type(screen.getByLabelText("OrgID"), "7770001");
    expect(bind).toBeDisabled();

    await user.clear(screen.getByLabelText("UID"));
    await user.type(screen.getByLabelText("UID"), " 5550001 ");
    expect(bind).toBeEnabled();
    await user.click(bind);
    expect(updateIdentitySpy).toHaveBeenCalledWith({
      uid: "5550001",
      orgId: "7770001",
      deapAgentUuid: undefined,
    });
  });

  it("shows the bound identity and forward status", async () => {
    operatorRef.current = {
      operator: true,
      dwsIdentity: {
        uid: "5550001",
        orgId: "7770001",
        deapAgentUuid: "18265b7f",
        updatedBy: "user-1",
        updatedAt: "2026-09-29T12:00:00Z",
      },
      forward: {
        rpcUrl: "https://pre.example.test/api/a2a/agents/agent_1234567890123/v1",
        sourceClientId: "client-1",
        active: true,
        updatedBy: "user-1",
        updatedAt: "2026-09-29T12:00:00Z",
      },
      forwardAllowedOrigins: ["https://pre.example.test"],
    };
    const user = userEvent.setup();
    renderCard();

    expect(screen.getByText(/uid=5550001 · orgId=7770001 · agentUuid=18265b7f/)).toBeInTheDocument();
    expect(screen.getByText(enAgents.tab_body.a2a.operator.forward_active)).toBeInTheDocument();
    expect(
      screen.getByText("https://pre.example.test/api/a2a/agents/agent_1234567890123/v1"),
    ).toBeInTheDocument();

    const clearButtons = screen.getAllByRole("button", { name: enAgents.tab_body.a2a.operator.clear });
    await user.click(clearButtons[0]!);
    expect(deleteIdentitySpy).toHaveBeenCalledTimes(1);
  });
});
