// @vitest-environment jsdom

import { fireEvent, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Agent } from "@multica/core/types";
import { renderWithI18n } from "../../../test/i18n";

const mount = vi.hoisted(() => ({ mutateAsync: vi.fn(), isPending: false }));
const revoke = vi.hoisted(() => ({ mutateAsync: vi.fn(), isPending: false }));
const setServer = vi.hoisted(() => ({ mutateAsync: vi.fn(), isPending: false }));
const queryState = vi.hoisted(() => ({
  account: { data: { machines: [] as unknown[] }, isLoading: false },
  mounts: { data: { machines: [] as unknown[] }, isLoading: false },
}));

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/auth", () => ({
  useAuthStore: (selector: (state: { user: { id: string } }) => unknown) =>
    selector({ user: { id: "user-1" } }),
}));
vi.mock("@tanstack/react-query", () => ({
  useQuery: (options: { queryKey: string[] }) =>
    options.queryKey.includes("agent") ? queryState.mounts : queryState.account,
}));
vi.mock("@multica/core/runner", () => ({
  accountRunnerBindingsOptions: () => ({ queryKey: ["runner", "account"] }),
  agentRunnerBindingsOptions: () => ({ queryKey: ["runner", "agent"] }),
  useMountAgentRunnerMachine: () => mount,
  useRevokeAgentRunnerBinding: () => revoke,
  useSetAgentRunnerMcpServerEnabled: () => setServer,
}));
vi.mock("@multica/ui/components/ui/select", () => ({
  Select: ({ children, onValueChange }: { children: React.ReactNode; onValueChange: (value: string) => void }) => (
    <div>{typeof children === "function" ? null : children}<button type="button" onClick={() => onValueChange("__none__")}>clear-runner</button></div>
  ),
  SelectContent: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  SelectItem: ({ children }: { children: React.ReactNode }) => <span>{children}</span>,
  SelectTrigger: ({ children, ...props }: React.HTMLAttributes<HTMLButtonElement>) => <button type="button" {...props}>{children}</button>,
  SelectValue: () => <span>selected-runner</span>,
}));

import { RunnerTab } from "./runner-tab";

const agent = { id: "agent-1" } as Agent;

beforeEach(() => {
  vi.clearAllMocks();
  queryState.account = { data: { machines: [] }, isLoading: false };
  queryState.mounts = { data: { machines: [] }, isLoading: false };
});

describe("RunnerTab", () => {
  it("allows clearing the optional Local Runner mount", () => {
    queryState.mounts = {
      data: { machines: [{ bindingId: "binding-1", machineId: "machine-1" }] },
      isLoading: false,
    };

    renderWithI18n(<RunnerTab agent={agent} canBind mode="execution" />);
    fireEvent.click(screen.getByRole("button", { name: "clear-runner" }));

    expect(revoke.mutateAsync).toHaveBeenCalledWith("binding-1");
  });

  it("uses the requested heading and managed-style empty state", () => {
    renderWithI18n(<RunnerTab agent={agent} canBind mode="mcp" />, {
      locale: "zh-Hans",
    });

    expect(screen.getByText("从本机 Runner 连接")).toBeInTheDocument();
    expect(
      screen.getByText("请先在执行配置中选择本机 Runner，之后可查看其 MCP 服务。")
        .parentElement,
    ).toHaveClass("border-dashed");
  });
});
