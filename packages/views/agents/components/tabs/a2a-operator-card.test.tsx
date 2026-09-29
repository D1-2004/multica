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
const updateProdForwardSpy = vi.hoisted(() => vi.fn());

vi.mock("@tanstack/react-query", () => ({
  useQuery: () => ({ data: operatorRef.current }),
  queryOptions: <T,>(options: T) => options,
}));

vi.mock("@multica/core/agent-a2a", () => ({
  agentA2AOperatorConfigOptions: () => ({ queryKey: ["agent-a2a-operator"] }),
  useUpdateAgentA2AOperatorIdentity: () => ({ mutateAsync: updateIdentitySpy, isPending: false }),
  useDeleteAgentA2AOperatorIdentity: () => ({ mutateAsync: deleteIdentitySpy, isPending: false }),
  useUpdateAgentA2AProdForward: () => ({ mutateAsync: updateProdForwardSpy, isPending: false }),
}));

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

import { A2AOperatorCard } from "./a2a-operator-card";

const TEST_RESOURCES = { en: { common: enCommon, agents: enAgents } };
const copy = enAgents.tab_body.a2a.operator;

function renderCard() {
  return render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <A2AOperatorCard wsId="ws-1" agentId="agent-1" />
    </I18nProvider>,
  );
}

const tagggIdentity = {
  uid: "7015073760",
  orgId: "439446171",
  displayName: "Tagggg",
  organizationName: "钉钉",
  deapAgentUuid: "18265b7f",
  a2aEnabled: true,
  boundAt: "2026-09-29T12:00:00Z",
};

describe("A2AOperatorCard", () => {
  beforeEach(() => {
    operatorRef.current = null;
    updateIdentitySpy.mockReset().mockResolvedValue({});
    deleteIdentitySpy.mockReset().mockResolvedValue({});
    updateProdForwardSpy.mockReset().mockResolvedValue({});
  });

  it("renders nothing for non-operators", () => {
    operatorRef.current = { operator: false, dwsIdentity: null, prodForward: null, forwardTarget: null };
    const { container } = renderCard();
    expect(container).toBeEmptyDOMElement();
  });

  it("binds a digital employee identity only with decimal ids", async () => {
    operatorRef.current = { operator: true, dwsIdentity: null, prodForward: null, forwardTarget: null };
    const user = userEvent.setup();
    renderCard();

    expect(screen.getByText(copy.identity_empty)).toBeInTheDocument();
    // Without forwarding roles neither forward section is shown.
    expect(screen.queryByText(copy.prod_forward_title)).not.toBeInTheDocument();
    expect(screen.queryByText(copy.forward_target_title)).not.toBeInTheDocument();
    const bind = screen.getByRole("button", { name: copy.save_identity });
    expect(bind).toBeDisabled();

    await user.type(screen.getByLabelText("UID"), "0123");
    await user.type(screen.getByLabelText("OrgID"), "439446171");
    expect(bind).toBeDisabled();
    await user.clear(screen.getByLabelText("UID"));
    await user.type(screen.getByLabelText("UID"), " 7015073760 ");
    await user.type(screen.getByLabelText(copy.display_name), "Tagggg");
    await user.click(bind);
    expect(updateIdentitySpy).toHaveBeenCalledWith({
      uid: "7015073760",
      orgId: "439446171",
      displayName: "Tagggg",
      organizationName: undefined,
      deapAgentUuid: undefined,
    });
  });

  it("marks an Integrations-only identity as not used by A2A and clears it", async () => {
    operatorRef.current = {
      operator: true,
      dwsIdentity: { ...tagggIdentity, a2aEnabled: false },
      prodForward: null,
      forwardTarget: null,
    };
    const user = userEvent.setup();
    renderCard();
    expect(screen.getByText(copy.identity_a2a_disabled)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: copy.clear }));
    expect(deleteIdentitySpy).toHaveBeenCalledTimes(1);
  });

  it("shows the pre-release switch on by default and turns it off", async () => {
    operatorRef.current = {
      operator: true,
      dwsIdentity: tagggIdentity,
      prodForward: {
        accept: true,
        blockedReason: "",
        registrations: [
          { registry: "https://fde-workbench.dingtalk.com", registeredAt: "2026-09-29T12:01:00Z", current: true, error: "" },
        ],
      },
      forwardTarget: null,
    };
    const user = userEvent.setup();
    renderCard();
    const toggle = screen.getByRole("switch", { name: copy.prod_forward_title });
    expect(toggle).toBeChecked();
    expect(screen.getByText("https://fde-workbench.dingtalk.com")).toBeInTheDocument();
    expect(screen.getByText(/^Registered · /)).toBeInTheDocument();
    await user.click(toggle);
    expect(updateProdForwardSpy).toHaveBeenCalledWith({ accept: false });
  });

  it("flags a registration for an earlier identity and re-registers", async () => {
    operatorRef.current = {
      operator: true,
      dwsIdentity: tagggIdentity,
      prodForward: {
        accept: true,
        blockedReason: "",
        registrations: [
          { registry: "https://fde-workbench.dingtalk.com", registeredAt: "2026-09-29T12:01:00Z", current: false, error: "" },
          { registry: "https://pre-fde-workbench.dingtalk.com", registeredAt: null, current: false, error: "register: HTTP 503" },
        ],
      },
      forwardTarget: null,
    };
    const user = userEvent.setup();
    renderCard();
    expect(screen.getByText(copy.prod_forward_stale)).toBeInTheDocument();
    expect(screen.getByText(copy.prod_forward_not_registered)).toBeInTheDocument();
    expect(screen.getByText("register: HTTP 503")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: copy.reregister }));
    expect(updateProdForwardSpy).toHaveBeenCalledWith({ accept: true });
  });

  it("explains why the Agent cannot register yet", () => {
    operatorRef.current = {
      operator: true,
      dwsIdentity: tagggIdentity,
      prodForward: {
        accept: true,
        blockedReason: "enable A2A on this Agent to accept production forwards",
        registrations: [],
      },
      forwardTarget: null,
    };
    renderCard();
    expect(screen.getByText(copy.prod_forward_pending)).toBeInTheDocument();
    expect(screen.getByText("enable A2A on this Agent to accept production forwards")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: copy.reregister })).not.toBeInTheDocument();
  });

  it("shows the switched-off state", () => {
    operatorRef.current = {
      operator: true,
      dwsIdentity: tagggIdentity,
      prodForward: { accept: false, blockedReason: "", registrations: [] },
      forwardTarget: null,
    };
    renderCard();
    expect(screen.getByRole("switch", { name: copy.prod_forward_title })).not.toBeChecked();
    expect(screen.getByText(copy.prod_forward_off)).toBeInTheDocument();
  });

  it("shows where production forwards this employee", () => {
    operatorRef.current = {
      operator: true,
      dwsIdentity: tagggIdentity,
      prodForward: null,
      forwardTarget: {
        rpcUrl: "https://pre.example.test/api/a2a/agents/agent_1234567890123/v1",
        agentName: "Pre QwenTag",
        registeredAt: "2026-09-29T12:01:00Z",
      },
    };
    renderCard();
    expect(screen.getByText(copy.forward_target_title)).toBeInTheDocument();
    expect(screen.getByText(/Pre QwenTag/)).toBeInTheDocument();
    expect(screen.queryByRole("switch")).not.toBeInTheDocument();
  });
});
