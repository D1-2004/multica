// @vitest-environment jsdom

import { fireEvent, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Agent } from "@multica/core/types";
import { renderWithI18n } from "../../../test/i18n";

const mount = vi.hoisted(() => ({ mutateAsync: vi.fn(), isPending: false }));
const revoke = vi.hoisted(() => ({ mutateAsync: vi.fn(), isPending: false }));
const queryState = vi.hoisted(() => ({
  account: {
    data: { machines: [] as unknown[] } as { machines: unknown[] } | null,
    isLoading: false,
    isError: false,
  },
  mounts: { data: { machines: [] as unknown[] }, isLoading: false, isError: false },
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
}));
vi.mock("@multica/ui/components/ui/select", () => ({
  Select: ({ children, onValueChange, value }: { children: React.ReactNode; onValueChange: (value: string) => void; value: string }) => (
    <div data-value={value}>{typeof children === "function" ? null : children}<button type="button" onClick={() => onValueChange("__none__")}>clear-runner</button></div>
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
  queryState.account = { data: { machines: [] }, isLoading: false, isError: false };
  queryState.mounts = { data: { machines: [] }, isLoading: false, isError: false };
});

describe("RunnerTab", () => {
  it("allows clearing the optional Local Runner mount", () => {
    queryState.mounts = {
      data: { machines: [{ bindingId: "binding-1", machineId: "machine-1" }] },
      isLoading: false,
      isError: false,
    };

    renderWithI18n(<RunnerTab agent={agent} canBind mode="execution" />);
    fireEvent.click(screen.getByRole("button", { name: "clear-runner" }));

    expect(revoke.mutateAsync).toHaveBeenCalledWith("binding-1");
  });

  it("keeps the mounted Runner visible as offline when account refresh fails", () => {
    queryState.account = {
      data: null,
      isLoading: false,
      isError: true,
    };
    queryState.mounts = {
      data: {
        machines: [
          {
            bindingId: "binding-1",
            machineId: "machine-1",
            name: "studio-mac",
            online: false,
          },
        ],
      },
      isLoading: false,
      isError: false,
    };

    renderWithI18n(<RunnerTab agent={agent} canBind mode="execution" />);

    const trigger = screen.getByRole("button", { name: "Local Runner" });
    expect(within(trigger).getByText("studio-mac")).toBeInTheDocument();
    expect(within(trigger).getByLabelText("Offline")).toHaveClass(
      "bg-muted-foreground/40",
    );
  });

  it("uses a green status dot for an online mounted Runner", () => {
    queryState.mounts = {
      data: {
        machines: [
          {
            bindingId: "binding-1",
            machineId: "machine-1",
            name: "studio-mac",
            online: true,
          },
        ],
      },
      isLoading: false,
      isError: false,
    };

    renderWithI18n(<RunnerTab agent={agent} canBind mode="execution" />);

    const trigger = screen.getByRole("button", { name: "Local Runner" });
    expect(within(trigger).getByLabelText("Online")).toHaveClass("bg-success");
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

  it("makes every exposed MCP visible without per-server enable switches", () => {
    queryState.mounts = {
      data: {
        machines: [
          {
            bindingId: "binding-1",
            machineId: "machine-1",
            name: "studio-mac",
            online: true,
            enabledMcpServers: {},
            mcpServers: [
              {
                name: "llm-wiki",
                title: "LLM Wiki Desktop",
                description: "Search and read the local knowledge base.",
                version: "1.2.0",
                transport: "stdio",
                availability: "available",
                detailStatus: "available",
                fingerprint: "sha256:wiki",
                tools: [
                  {
                    name: "search_wiki",
                    title: "Search wiki",
                    description: "Search pages in LLM Wiki.",
                  },
                ],
              },
              { name: "future-mcp", transport: "http", availability: "available", fingerprint: "sha256:future" },
            ],
          },
        ],
      },
      isLoading: false,
      isError: false,
    };

    renderWithI18n(<RunnerTab agent={agent} canBind mode="mcp" />);

    expect(screen.getByText("llm-wiki")).toBeInTheDocument();
    expect(screen.getByText("future-mcp")).toBeInTheDocument();
    expect(screen.queryAllByRole("switch")).toHaveLength(0);
    expect(screen.getByText("llm-wiki").closest("li")?.querySelector(".bg-success")).not.toBeNull();
  });

  it("shows protocol details for a mounted Runner MCP server", async () => {
    const user = userEvent.setup();
    queryState.mounts = {
      data: {
        machines: [{
          bindingId: "binding-1",
          machineId: "machine-1",
          name: "studio-mac",
          online: true,
          enabledMcpServers: {},
          mcpServers: [{
            name: "llm-wiki",
            title: "LLM Wiki Desktop",
            description: "Search and read the local knowledge base.",
            version: "1.2.0",
            transport: "stdio",
            availability: "available",
            detailStatus: "available",
            fingerprint: "sha256:wiki",
            tools: [{ name: "search_wiki", title: "Search wiki", description: "Search pages in LLM Wiki." }],
          }],
        }],
      },
      isLoading: false,
      isError: false,
    };

    renderWithI18n(<RunnerTab agent={agent} canBind mode="mcp" />);
    await user.click(screen.getByRole("button", { name: /llm-wiki/i }));

    expect(screen.getByText("Search and read the local knowledge base.")).toBeInTheDocument();
    expect(screen.getByText("Search pages in LLM Wiki.")).toBeInTheDocument();
    expect(screen.getByText("v1.2.0")).toBeInTheDocument();
  });
});
