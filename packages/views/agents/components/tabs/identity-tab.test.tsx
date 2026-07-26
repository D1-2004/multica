// @vitest-environment jsdom

import { describe, it, expect, vi, beforeEach } from "vitest";
import type { ReactNode } from "react";
import { render, screen } from "@testing-library/react";
import type { Agent } from "@multica/core/types";

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
  DingTalkAccountBindingCard: ({ agentId, bindingMode }: { agentId: string; bindingMode: string }) => (
    <section
      aria-label="Enterprise digital employee"
      data-agent-id={agentId}
      data-binding-mode={bindingMode}
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

function renderTab(children: ReactNode) {
  return render(children);
}

describe("IdentityTab", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    membersRef.current = [{ user_id: "user-1", role: "owner" }];
  });

  it("renders GitHub sandbox identity before DingTalk digital employee identity", () => {
    renderTab(<IdentityTab agent={agent} />);
    const githubIdentity = screen.getByRole("region", {
      name: /GitHub sandbox identity/i,
    });
    const digitalEmployee = screen.getByRole("region", {
      name: /Enterprise digital employee/i,
    });
    expect(githubIdentity).toHaveAttribute("data-agent-id", "agent-1");
    expect(githubIdentity).toHaveAttribute("data-can-manage", "true");
    expect(digitalEmployee).toHaveAttribute("data-binding-mode", "identity");
    expect(
      githubIdentity.compareDocumentPosition(digitalEmployee) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
  });

  it("lets a non-admin agent owner manage GitHub sandbox identity", () => {
    membersRef.current = [{ user_id: "user-1", role: "member" }];
    renderTab(<IdentityTab agent={agent} />);
    expect(
      screen.getByRole("region", { name: /GitHub sandbox identity/i }),
    ).toHaveAttribute("data-can-manage", "true");
  });

  it("shows GitHub sandbox identity read-only for non-owner members", () => {
    membersRef.current = [{ user_id: "user-1", role: "member" }];
    renderTab(<IdentityTab agent={{ ...agent, owner_id: "user-2" }} />);
    expect(
      screen.getByRole("region", { name: /GitHub sandbox identity/i }),
    ).toHaveAttribute("data-can-manage", "false");
  });
});
