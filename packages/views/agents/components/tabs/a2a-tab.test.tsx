// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Agent, AgentA2AConfig } from "@multica/core/types";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../../locales/en/common.json";
import enAgents from "../../../locales/en/agents.json";

const rawToken = "mca2a_one-time-secret";
const configRef = vi.hoisted(() => ({
  current: null as AgentA2AConfig | null,
}));
const createCredentialSpy = vi.hoisted(() => vi.fn());
const deleteCredentialSpy = vi.hoisted(() => vi.fn());
const updateConfigSpy = vi.hoisted(() => vi.fn());
const updateClientSpy = vi.hoisted(() => vi.fn());
const copyTextSpy = vi.hoisted(() => vi.fn());

vi.mock("@tanstack/react-query", () => ({
  useQuery: () => ({
    data: configRef.current,
    isLoading: false,
    isError: false,
    refetch: vi.fn(),
  }),
  queryOptions: <T,>(options: T) => options,
}));

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

vi.mock("@multica/core/agent-a2a", () => ({
  agentA2AConfigOptions: () => ({ queryKey: ["agent-a2a"] }),
  useUpdateAgentA2AConfig: () => ({
    mutateAsync: updateConfigSpy,
    isPending: false,
  }),
  useCreateAgentA2AClient: () => ({ mutateAsync: vi.fn(), isPending: false }),
  useUpdateAgentA2AClient: () => ({ mutateAsync: updateClientSpy, isPending: false }),
  useCreateAgentA2ACredential: () => ({
    mutateAsync: createCredentialSpy,
    isPending: false,
  }),
  useDeleteAgentA2ACredential: () => ({
    mutateAsync: deleteCredentialSpy,
    isPending: false,
  }),
}));

vi.mock("@multica/ui/lib/clipboard", () => ({
  copyText: copyTextSpy,
}));

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

import { A2ATab } from "./a2a-tab";

const TEST_RESOURCES = { en: { common: enCommon, agents: enAgents } };

const agent: Agent = {
  id: "agent-1",
  workspace_id: "ws-1",
  runtime_id: "runtime-1",
  name: "Coding Agent",
  description: "Builds local projects",
  instructions: "",
  avatar_url: null,
  runtime_mode: "local",
  runtime_config: {},
  custom_args: [],
  visibility: "private",
  permission_mode: "private",
  invocation_targets: [],
  status: "idle",
  max_concurrent_tasks: 1,
  model: "",
  owner_id: "user-1",
  skills: [],
  created_at: "2026-08-09T00:00:00Z",
  updated_at: "2026-08-09T00:00:00Z",
  archived_at: null,
  archived_by: null,
};

function renderTab() {
  return render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <A2ATab agent={agent} />
    </I18nProvider>,
  );
}

describe("A2ATab", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    updateConfigSpy.mockResolvedValue(undefined);
    updateClientSpy.mockResolvedValue(undefined);
    copyTextSpy.mockResolvedValue(true);
    configRef.current = {
      endpoint: {
        enabled: true,
        publicAgentId: "public-agent-1",
        cardName: "Coding Agent",
        cardDescription: "Builds local projects",
        cardVersion: "1.0.0",
        cardSkills: [],
        cardUrl: "https://multica.example/api/a2a/agents/public-agent-1/.well-known/agent-card.json",
        rpcUrl: "https://multica.example/api/a2a/agents/public-agent-1/v1",
        mcpUrl: "https://multica.example/api/mcp/agents/public-agent-1",
        protocolVersion: "1.0",
      },
      agentCard: {
        name: "Coding Agent",
        description: "Builds local projects",
        supportedInterfaces: [
          {
            url: "https://multica.example/api/a2a/agents/public-agent-1/v1",
            protocolBinding: "JSONRPC",
            protocolVersion: "1.0",
          },
        ],
        version: "1.0.0",
        capabilities: {
          streaming: false,
          pushNotifications: false,
          extendedAgentCard: false,
        },
        defaultInputModes: ["text/plain"],
        defaultOutputModes: ["text/plain"],
        skills: [],
      },
      clients: [
        {
          id: "client-1",
          name: "A2A Client",
          status: "active",
          scopes: ["send", "read", "list", "cancel"],
          rateLimitPerMinute: null,
          maxConcurrentTasks: null,
          credentials: [],
          createdAt: "2026-08-09T00:00:00Z",
          updatedAt: "2026-08-09T00:00:00Z",
          revokedAt: null,
        },
      ],
    };
    createCredentialSpy.mockImplementation(
      async ({ onToken }: { onToken: (token: string) => void }) => {
        onToken(rawToken);
        return {
          id: "credential-1",
          keyId: "key-1",
          tokenPrefix: "mca2a_key-1",
          status: "active",
          expiresAt: null,
          lastUsedAt: null,
          revokedAt: null,
          createdAt: "2026-08-09T00:00:00Z",
          updatedAt: "2026-08-09T00:00:00Z",
        };
      },
    );
    deleteCredentialSpy.mockResolvedValue(undefined);
  });

  it("shows only the A2A endpoint, Agent Card, and API key controls", () => {
    renderTab();

    expect(screen.getByText(/standard A2A protocol/i)).toBeInTheDocument();
    expect(screen.getByRole("switch", { name: /Accept A2A calls/i })).toBeChecked();
    expect(screen.getByDisplayValue(/public-agent-1\/v1$/)).toBeInTheDocument();
    expect(
      screen.getByDisplayValue(/public-agent-1\/\.well-known\/agent-card\.json$/),
    ).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: /A2A API key/i })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Generate API key/i })).toBeEnabled();
    expect(screen.queryByRole("heading", { name: /MCP link/i })).not.toBeInTheDocument();
  });

  it("copies the A2A endpoint URLs", async () => {
    const user = userEvent.setup();
    renderTab();

    await user.click(screen.getByRole("button", { name: /Copy A2A RPC URL/i }));
    await user.click(screen.getByRole("button", { name: /Copy Agent Card URL/i }));

    expect(copyTextSpy).toHaveBeenNthCalledWith(
      1,
      "https://multica.example/api/a2a/agents/public-agent-1/v1",
    );
    expect(copyTextSpy).toHaveBeenNthCalledWith(
      2,
      "https://multica.example/api/a2a/agents/public-agent-1/.well-known/agent-card.json",
    );
  });

  it("generates and copies a one-time A2A API token", async () => {
    const user = userEvent.setup();
    renderTab();

    await user.click(screen.getByRole("button", { name: /Generate API key/i }));

    expect(await screen.findByDisplayValue(rawToken)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /Copy credential/i }));
    expect(copyTextSpy).toHaveBeenLastCalledWith(rawToken);
    expect(createCredentialSpy).toHaveBeenCalledTimes(1);
  });

  it("persists the A2A enable switch immediately", async () => {
    const user = userEvent.setup();
    configRef.current = {
      endpoint: {
        enabled: false,
        publicAgentId: "public-agent-1",
        cardName: "Coding Agent",
        cardDescription: "Builds local projects",
        cardVersion: "1.0.0",
        cardSkills: [],
        cardUrl: "",
        rpcUrl: "",
        protocolVersion: "1.0",
      },
      agentCard: null,
      clients: [],
    };
    renderTab();

    const enabledSwitch = screen.getByRole("switch", {
      name: /Accept A2A calls/i,
    });
    expect(enabledSwitch).not.toBeChecked();
    expect(screen.getByText(/^Disabled$/i)).toBeInTheDocument();

    await user.click(enabledSwitch);
    await waitFor(() => expect(updateConfigSpy).toHaveBeenCalledTimes(1));
    expect(updateConfigSpy).toHaveBeenCalledWith(
      expect.objectContaining({ enabled: true }),
    );
  });
});
