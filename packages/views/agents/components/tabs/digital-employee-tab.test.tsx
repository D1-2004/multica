// @vitest-environment jsdom

import { describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import type { Agent, AgentRuntime, MemberWithUser } from "@multica/core/types";
import { renderWithI18n } from "../../../test/i18n";

vi.mock("../../../common/avatar-upload-control", () => ({
  AvatarUploadControl: () => <div aria-label="Avatar" />,
}));

vi.mock("../integrations/dingtalk-account-binding", () => ({
  DingTalkAccountBindingCard: ({
    bindingMode,
    canOperate,
  }: {
    bindingMode: string;
    canOperate: boolean;
  }) => (
    <section
      aria-label={
        bindingMode === "message"
          ? "Enterprise digital employee"
          : "Execution identity"
      }
      data-can-operate={canOperate ? "true" : "false"}
    />
  ),
}));

vi.mock("../integrations/enterprise-identity-binding", () => ({
  EnterpriseIdentityBindingCard: ({ canManage }: { canManage: boolean }) => (
    <section
      aria-label="Alibaba employee identity"
      data-can-manage={canManage ? "true" : "false"}
    />
  ),
}));

const extractAgentVoice = vi.fn();
vi.mock("@multica/core/api", () => ({
  api: {
    extractAgentVoice: (...args: unknown[]) => extractAgentVoice(...args),
  },
}));

import { DigitalEmployeeTab } from "./digital-employee-tab";

const agent = {
  id: "agent-1",
  name: "Lambda",
  description: "Coordinates the team",
  instructions: "Stay concise.",
  owner_id: "user-1",
  inbound_coordinator: true,
} as Agent;

const asbRuntime = {
  id: "runtime-1",
  workspace_id: "ws-1",
  daemon_id: "asb:ws-1:hermes",
  name: "ASB Hermes",
  runtime_mode: "cloud",
  status: "online",
  provider: "hermes",
  launch_header: "",
  device_info: "Aone Sandbox",
  owner_id: "user-1",
  visibility: "private",
  last_seen_at: null,
  created_at: "2026-09-05T00:00:00Z",
  updated_at: "2026-09-05T00:00:00Z",
  metadata: {
    kind: "cloud-sandbox",
    sandbox_backend: "asb",
    artifact_kind: "oci_image",
    artifact_ref: `registry.example/runtime@sha256:${"a".repeat(64)}`,
    artifact_digest: `sha256:${"a".repeat(64)}`,
  },
} as AgentRuntime;

describe("DigitalEmployeeTab", () => {
  it("contains employee identity and behavior without robot or sandbox identity", () => {
    renderWithI18n(
      <DigitalEmployeeTab
        agent={agent}
        runtime={asbRuntime}
        members={[]}
        currentUserId="user-1"
        canEdit
        canOperateDingTalkBinding
        dingTalkBindingPermissionLoading={false}
        onUpdate={vi.fn(async () => {})}
      />,
    );

    expect(screen.getByText("Employee profile")).toBeInTheDocument();
    expect(screen.getByLabelText("Name")).toBeInTheDocument();
    expect(
      screen.getByRole("region", { name: /Enterprise digital employee/i }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("region", { name: /Execution identity/i }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("region", { name: /Alibaba employee identity/i }),
    ).toHaveAttribute("data-can-manage", "true");
    expect(screen.getByLabelText("Persona")).toBeInTheDocument();
    expect(
      screen.getByLabelText("Judge before sandbox"),
    ).toBeInTheDocument();
    expect(screen.queryByText("Lark")).not.toBeInTheDocument();
    expect(screen.queryByText("GitHub sandbox identity")).not.toBeInTheDocument();
  });

  it("keeps employee identities read-only for viewers who cannot edit", () => {
    renderWithI18n(
      <DigitalEmployeeTab
        agent={{ ...agent, owner_id: "user-2" }}
        runtime={asbRuntime}
        members={[
          {
            id: "member-1",
            workspace_id: "ws-1",
            user_id: "user-1",
            role: "member",
            created_at: "2026-09-05T00:00:00Z",
            name: "Viewer",
            email: "viewer@example.com",
            avatar_url: null,
          } satisfies MemberWithUser,
        ]}
        currentUserId="user-1"
        canEdit={false}
        canOperateDingTalkBinding={false}
        dingTalkBindingPermissionLoading={false}
        onUpdate={vi.fn(async () => {})}
      />,
    );

    expect(
      screen.getByRole("region", { name: /Alibaba employee identity/i }),
    ).toHaveAttribute("data-can-manage", "false");
    expect(
      screen.getByRole("region", { name: /Enterprise digital employee/i }),
    ).toHaveAttribute("data-can-operate", "false");
  });
});
