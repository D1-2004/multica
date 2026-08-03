// @vitest-environment jsdom

import { describe, it, expect, vi, beforeEach } from "vitest";
import type { ReactNode } from "react";
import { render, screen } from "@testing-library/react";
import type { Agent, AgentRuntime } from "@multica/core/types";

type MemberRole = "owner" | "admin" | "member" | "guest";

const membersRef = vi.hoisted(() => ({
  current: [{ user_id: "user-1", role: "owner" as MemberRole }],
}));

vi.mock("@tanstack/react-query", () => ({
  useQuery: (opts: { enabled?: boolean }) =>
    opts.enabled === false ? { data: undefined } : { data: membersRef.current },
  queryOptions: <T,>(opts: T) => opts,
}));

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

vi.mock("@multica/core/workspace/queries", () => ({
  memberListOptions: () => ({ queryKey: ["members"], queryFn: vi.fn() }),
}));

vi.mock("@multica/core/auth", () => {
  const useAuthStore = Object.assign(
    (sel?: (s: { user: { id: string } }) => unknown) =>
      sel ? sel({ user: { id: "user-1" } }) : { user: { id: "user-1" } },
    { getState: () => ({ user: { id: "user-1" } }) },
  );
  return { useAuthStore };
});

vi.mock("../integrations/dingtalk-account-binding", () => ({
  DingTalkAccountBindingCard: ({
    agentId,
    bindingMode,
    canOperate,
  }: {
    agentId: string;
    bindingMode: string;
    canOperate: boolean;
  }) => (
    <section
      aria-label="Enterprise digital employee"
      data-agent-id={agentId}
      data-binding-mode={bindingMode}
      data-can-operate={canOperate ? "true" : "false"}
    />
  ),
}));

vi.mock("../integrations/github-identity-binding", () => ({
  GitHubIdentityBindingCard: ({
    agentId,
    canManage,
  }: {
    agentId: string;
    canManage: boolean;
  }) => (
    <section
      aria-label="GitHub sandbox identity"
      data-agent-id={agentId}
      data-can-manage={canManage ? "true" : "false"}
    />
  ),
}));

vi.mock("../integrations/enterprise-identity-binding", () => ({
  EnterpriseIdentityBindingCard: ({
    agentId,
    canManage,
  }: {
    agentId: string;
    canManage: boolean;
  }) => (
    <section
      aria-label="Alibaba employee identity"
      data-agent-id={agentId}
      data-can-manage={canManage ? "true" : "false"}
    />
  ),
}));

import { IdentityTab } from "./identity-tab";

const agent: Agent = {
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
  created_at: "2026-04-16T00:00:00Z",
  updated_at: "2026-04-16T00:00:00Z",
  archived_at: null,
  archived_by: null,
};

const asbRuntime: AgentRuntime = {
  id: "runtime-asb",
  workspace_id: "ws-1",
  daemon_id: "asb:ws-1:hermes",
  name: "ASB-Hermes",
  runtime_mode: "cloud",
  provider: "hermes",
  launch_header: "",
  status: "online",
  device_info: "Aone Sandbox",
  metadata: {
    kind: "cloud-sandbox",
    sandbox_backend: "asb",
    artifact_kind: "oci_image",
  },
  owner_id: "user-1",
  visibility: "private",
  last_seen_at: null,
  created_at: "2026-08-03T00:00:00Z",
  updated_at: "2026-08-03T00:00:00Z",
};

const fcRuntime: AgentRuntime = {
  ...asbRuntime,
  id: "runtime-fc",
  daemon_id: "fc-e2b:ws-1:hermes",
  name: "FC-Hermes",
  device_info: "FC/E2B one-shot sandbox",
  metadata: { kind: "fc-e2b" },
};

function renderTab(children: ReactNode) {
  return render(children);
}

describe("IdentityTab", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    membersRef.current = [{ user_id: "user-1", role: "owner" }];
  });

  it("renders sandbox identities before DingTalk digital employee identity", () => {
    renderTab(
      <IdentityTab
        agent={agent}
        runtime={asbRuntime}
        canOperateDingTalkBinding
        dingTalkBindingPermissionLoading={false}
      />,
    );
    const githubIdentity = screen.getByRole("region", {
      name: /GitHub sandbox identity/i,
    });
    const enterpriseIdentity = screen.getByRole("region", {
      name: /Alibaba employee identity/i,
    });
    const digitalEmployee = screen.getByRole("region", {
      name: /Enterprise digital employee/i,
    });
    expect(githubIdentity).toHaveAttribute("data-agent-id", "agent-1");
    expect(githubIdentity).toHaveAttribute("data-can-manage", "true");
    expect(enterpriseIdentity).toHaveAttribute("data-agent-id", "agent-1");
    expect(enterpriseIdentity).toHaveAttribute("data-can-manage", "true");
    expect(digitalEmployee).toHaveAttribute("data-binding-mode", "identity");
    expect(digitalEmployee).toHaveAttribute("data-can-operate", "true");
    expect(
      githubIdentity.compareDocumentPosition(enterpriseIdentity) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
    expect(
      enterpriseIdentity.compareDocumentPosition(digitalEmployee) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
  });

  it("hides Alibaba employee identity for FC runtimes", () => {
    renderTab(
      <IdentityTab
        agent={agent}
        runtime={fcRuntime}
        canOperateDingTalkBinding
        dingTalkBindingPermissionLoading={false}
      />,
    );
    expect(
      screen.queryByRole("region", { name: /Alibaba employee identity/i }),
    ).not.toBeInTheDocument();
    expect(
      screen.getByRole("region", { name: /GitHub sandbox identity/i }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("region", { name: /Enterprise digital employee/i }),
    ).toHaveAttribute("data-binding-mode", "identity");
  });

  it("lets a non-admin agent owner manage GitHub sandbox identity", () => {
    membersRef.current = [{ user_id: "user-1", role: "member" }];
    renderTab(
      <IdentityTab
        agent={agent}
        canOperateDingTalkBinding
        dingTalkBindingPermissionLoading={false}
      />,
    );
    expect(
      screen.getByRole("region", { name: /GitHub sandbox identity/i }),
    ).toHaveAttribute("data-can-manage", "true");
  });

  it("shows GitHub sandbox identity read-only for non-owner members", () => {
    membersRef.current = [{ user_id: "user-1", role: "member" }];
    renderTab(
      <IdentityTab
        agent={{ ...agent, owner_id: "user-2" }}
        canOperateDingTalkBinding={false}
        dingTalkBindingPermissionLoading={false}
      />,
    );
    expect(
      screen.getByRole("region", { name: /GitHub sandbox identity/i }),
    ).toHaveAttribute("data-can-manage", "false");
    expect(
      screen.getByRole("region", { name: /Enterprise digital employee/i }),
    ).toHaveAttribute("data-can-operate", "false");
  });
});
