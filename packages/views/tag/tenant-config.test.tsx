import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Agent } from "@multica/core/types";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../locales/en/common.json";
import enAgents from "../locales/en/agents.json";
import { TagTenantConfig } from "./tenant-config";

const copy = enAgents.tag_tenant;

const { data, mocks } = vi.hoisted(() => ({
  data: {
    bindings: [] as unknown[],
    operator: { operator: true, dwsIdentity: null } as unknown,
    a2a: { endpoint: null, agentCard: null, clients: [] } as unknown,
  },
  mocks: {
    saveIdentity: vi.fn(),
    saveSupervisor: vi.fn(),
    setNative: vi.fn(),
    removeBinding: vi.fn(),
    begin: vi.fn(),
    updateA2A: vi.fn(),
  },
}));

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/dingtalk-account-bindings", () => ({
  dingtalkAccountBindingsOptions: () => ({
    queryKey: ["bindings"],
    queryFn: () => ({ bindings: data.bindings, configured: true, manualBindingAllowed: true }),
  }),
  dingtalkNativeSubscriptionStatusOptions: () => ({
    queryKey: ["native"],
    queryFn: () => ({ nativeSubscription: true, stream: { state: "connected" } }),
  }),
  useBeginDingTalkAccountBinding: () => ({ mutate: mocks.begin, isPending: false }),
  useDeleteDingTalkAccountBinding: () => ({ mutate: mocks.removeBinding, isPending: false }),
  useSetDingTalkNativeSubscription: () => ({ mutate: mocks.setNative, isPending: false }),
}));
vi.mock("@multica/core/agent-a2a", () => ({
  agentA2AConfigOptions: () => ({ queryKey: ["a2a"], queryFn: () => data.a2a }),
  agentA2AOperatorConfigOptions: () => ({ queryKey: ["operator"], queryFn: () => data.operator }),
  useUpdateAgentA2AConfig: () => ({ mutate: mocks.updateA2A, isPending: false }),
  useUpdateAgentA2AOperatorIdentity: () => ({ mutateAsync: mocks.saveIdentity, isPending: false }),
}));
vi.mock("@multica/core/tag", () => ({
  useSetTagEmployeeSupervisorLink: () => ({ mutateAsync: mocks.saveSupervisor, isPending: false }),
}));
vi.mock("../agents/components/agent-message-settings", () => ({
  InboundCoordinatorSetting: () => <div>inbound-coordinator</div>,
}));

const agent = { id: "employee-1", name: "Tag · 钉钉验证" } as Agent;

function binding(overrides: { identity?: string; route?: string; native?: boolean }) {
  return {
    id: "b-1",
    workspaceId: "ws-1",
    agentId: "employee-1",
    dwsIdentity: {
      status: overrides.identity ?? "unbound",
      organizationName: "钉钉",
      accountDisplayName: "Taggg",
      nativeSubscription: overrides.native ?? false,
    },
    messageRoute: { status: overrides.route ?? "unbound", organizationName: "钉钉" },
  };
}

function renderConfig() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <I18nProvider locale="en" resources={{ en: { common: enCommon, agents: enAgents } }}>
      <QueryClientProvider client={client}>
        <TagTenantConfig agent={agent} canEdit onUpdate={vi.fn()} />
      </QueryClientProvider>
    </I18nProvider>,
  );
}

beforeEach(() => {
  for (const mock of Object.values(mocks)) mock.mockReset();
  data.bindings = [];
  data.operator = { operator: true, dwsIdentity: null };
  data.a2a = { endpoint: null, agentCard: null, clients: [] };
});

describe("TagTenantConfig", () => {
  it("shows identity as two layers, the three ways to perceive events and the inbound coordinator", async () => {
    renderConfig();
    expect(screen.getByRole("heading", { name: copy.issue_title })).toBeTruthy();
    expect(screen.getByRole("heading", { name: copy.perceive_title })).toBeTruthy();
    expect(screen.getByText(copy.mode_native)).toBeTruthy();
    expect(screen.getByText(copy.mode_backend)).toBeTruthy();
    expect(screen.getByText(copy.mode_a2a)).toBeTruthy();
    expect(screen.getByText("inbound-coordinator")).toBeTruthy();
    // Without an identity, native perception cannot start.
    expect(await screen.findByText(copy.native_needs_identity)).toBeTruthy();
    expect(screen.getByRole("switch", { name: copy.mode_native })).toHaveAttribute("aria-disabled", "true");
    expect(screen.getByText(copy.a2a_unavailable)).toBeTruthy();
  });

  it("marks native perception active and blocks the backend subscription while it is on", async () => {
    data.bindings = [binding({ identity: "active", native: true })];
    renderConfig();
    await waitFor(() =>
      expect(screen.getByRole("switch", { name: copy.mode_native })).toHaveAttribute("aria-checked", "true"),
    );
    expect(await screen.findByText(copy.native_stream.replace("{{state}}", "connected"))).toBeTruthy();
    expect(screen.getByText(copy.route_blocked_by_native)).toBeTruthy();
    expect(screen.getByRole("button", { name: copy.route_scan })).toBeDisabled();
  });

  it("issues an identity by filling in the employee and supervisor IDs", async () => {
    const user = userEvent.setup();
    mocks.saveIdentity.mockResolvedValue(undefined);
    mocks.saveSupervisor.mockResolvedValue(undefined);
    renderConfig();

    await user.click(screen.getByRole("radio", { name: new RegExp(copy.method_fill) }));
    await user.type(await screen.findByLabelText(copy.fill_org_id), "439446171");
    await user.type(screen.getByLabelText(copy.fill_uid), "7015073760");
    await user.type(screen.getByLabelText(copy.fill_supervisor_uid), "6753994909");
    await user.type(screen.getByLabelText(copy.fill_deap_agent), "18265b7f-ed66-42f7-b4be-c99dd20b2b62");
    await user.click(screen.getByRole("button", { name: copy.fill_submit }));

    expect(mocks.saveIdentity).toHaveBeenCalledWith({
      uid: "7015073760",
      orgId: "439446171",
      displayName: undefined,
      organizationName: undefined,
      deapAgentUuid: "18265b7f-ed66-42f7-b4be-c99dd20b2b62",
    });
    expect(mocks.saveSupervisor).toHaveBeenCalledWith({
      agentId: "employee-1",
      deapAgentUuid: "18265b7f-ed66-42f7-b4be-c99dd20b2b62",
      supervisorUid: "6753994909",
    });
    expect(await screen.findByText(copy.fill_saved_supervisor)).toBeTruthy();
  });

  it("says so when the deployment has no supervisor exchange", async () => {
    const user = userEvent.setup();
    const { ApiError } = await import("@multica/core/api");
    mocks.saveIdentity.mockResolvedValue(undefined);
    mocks.saveSupervisor.mockRejectedValue(new ApiError("not found", 404, "Not Found"));
    renderConfig();

    await user.click(screen.getByRole("radio", { name: new RegExp(copy.method_fill) }));
    await user.type(await screen.findByLabelText(copy.fill_org_id), "439446171");
    await user.type(screen.getByLabelText(copy.fill_uid), "7015073760");
    await user.type(screen.getByLabelText(copy.fill_supervisor_uid), "6753994909");
    await user.type(screen.getByLabelText(copy.fill_deap_agent), "deap-1");
    await user.click(screen.getByRole("button", { name: copy.fill_submit }));

    expect(await screen.findByText(copy.supervisor_unsupported)).toBeTruthy();
  });

  it("turns on managed replies before native perception for an employee without them", async () => {
    const user = userEvent.setup();
    data.bindings = [binding({ identity: "active" })];
    const onUpdate = vi.fn().mockResolvedValue(undefined);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <I18nProvider locale="en" resources={{ en: { common: enCommon, agents: enAgents } }}>
        <QueryClientProvider client={client}>
          <TagTenantConfig
            agent={{ ...agent, inbound_coordinator: false, dingtalk_response_enabled: false }}
            canEdit
            onUpdate={onUpdate}
          />
        </QueryClientProvider>
      </I18nProvider>,
    );
    await waitFor(() =>
      expect(screen.getByRole("switch", { name: copy.mode_native })).not.toHaveAttribute("aria-disabled", "true"),
    );
    await user.click(screen.getByRole("switch", { name: copy.mode_native }));
    await waitFor(() => expect(mocks.setNative).toHaveBeenCalled());
    expect(onUpdate).toHaveBeenCalledWith({ inbound_coordinator: true, dingtalk_response_enabled: true });
    expect(onUpdate.mock.invocationCallOrder[0]).toBeLessThan(mocks.setNative.mock.invocationCallOrder[0] ?? 0);
    expect(mocks.setNative.mock.calls[0]?.[0]).toEqual({ agentId: "employee-1", enabled: true });
  });

  it("keeps direct filling to platform operators", async () => {
    const user = userEvent.setup();
    data.operator = { operator: false, dwsIdentity: null };
    renderConfig();
    await user.click(screen.getByRole("radio", { name: new RegExp(copy.method_fill) }));
    expect(await screen.findByText(copy.fill_operator_only)).toBeTruthy();
    expect(screen.queryByLabelText(copy.fill_org_id)).toBeNull();
    const issue = screen.getByRole("radiogroup", { name: copy.issue_title });
    expect(within(issue).getAllByRole("radio")).toHaveLength(2);
  });
});
