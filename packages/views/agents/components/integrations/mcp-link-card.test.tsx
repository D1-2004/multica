// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Agent, AgentA2AConfig } from "@multica/core/types";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../../locales/en/common.json";
import enAgents from "../../../locales/en/agents.json";

const rawToken = "mca2a_mcp-one-time-secret";
const configRef = vi.hoisted(() => ({
  current: null as AgentA2AConfig | null,
}));
const createCredentialSpy = vi.hoisted(() => vi.fn());
const deleteCredentialSpy = vi.hoisted(() => vi.fn());
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
  useUpdateAgentA2AConfig: () => ({ mutateAsync: vi.fn(), isPending: false }),
  useCreateAgentA2AClient: () => ({ mutateAsync: vi.fn(), isPending: false }),
  useCreateAgentA2ACredential: () => ({
    mutateAsync: createCredentialSpy,
    isPending: false,
  }),
  useDeleteAgentA2ACredential: () => ({
    mutateAsync: deleteCredentialSpy,
    isPending: false,
  }),
}));

vi.mock("@multica/ui/lib/clipboard", () => ({ copyText: copyTextSpy }));
vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

import { AgentMCPLinkCard } from "./mcp-link-card";

const agent: Agent = {
  id: "agent-1",
  workspace_id: "ws-1",
  runtime_id: "runtime-1",
  name: "Coding Agent",
  description: "Builds projects",
  instructions: "",
  avatar_url: null,
  runtime_mode: "cloud",
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

function renderCard() {
  return render(
    <I18nProvider
      locale="en"
      resources={{ en: { common: enCommon, agents: enAgents } }}
    >
      <AgentMCPLinkCard agent={agent} />
    </I18nProvider>,
  );
}

describe("AgentMCPLinkCard", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    copyTextSpy.mockResolvedValue(true);
    deleteCredentialSpy.mockResolvedValue(undefined);
    configRef.current = {
      endpoint: {
        enabled: false,
        publicAgentId: "public-agent-1",
        cardName: "Coding Agent",
        cardDescription: "Builds projects",
        cardVersion: "1.0.0",
        cardSkills: [],
        cardUrl:
          "https://multica.example/api/a2a/agents/public-agent-1/.well-known/agent-card.json",
        rpcUrl: "https://multica.example/api/a2a/agents/public-agent-1/v1",
        mcpUrl: "https://multica.example/api/mcp/agents/public-agent-1",
        protocolVersion: "1.0",
      },
      agentCard: null,
      clients: [
        {
          id: "client-1",
          name: "Local Coding Agent",
          status: "active",
          scopes: ["send", "read"],
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
        return { id: "credential-1" };
      },
    );
  });

  it("generates one secret-bearing MCP URL while A2A publication is disabled", async () => {
    const user = userEvent.setup();
    renderCard();

    expect(screen.getByRole("heading", { name: "MCP" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Generate MCP link/i })).toBeEnabled();

    await user.click(screen.getByRole("button", { name: /Generate MCP link/i }));

    const url = await screen.findByRole("textbox", { name: "MCP URL" });
    expect(url).toHaveValue(
      "https://multica.example/api/mcp/connect/mca2a_mcp-one-time-secret",
    );
    expect(screen.queryByText(/codex mcp add/i)).not.toBeInTheDocument();
    expect(screen.queryByText(/claude mcp add/i)).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /Copy link/i }));
    expect(copyTextSpy).toHaveBeenLastCalledWith(
      "https://multica.example/api/mcp/connect/mca2a_mcp-one-time-secret",
    );
  });

  it("shows a masked existing link with regenerate and revoke actions", () => {
    const config = configRef.current!;
    configRef.current = {
      ...config,
      clients: [
        {
          ...config.clients[0]!,
          credentials: [
            {
              id: "credential-existing",
              keyId: "key-existing",
              tokenPrefix: "mca2a_existing",
              status: "active",
              expiresAt: null,
              lastUsedAt: null,
              revokedAt: null,
              createdAt: "2026-08-09T00:00:00Z",
              updatedAt: "2026-08-09T00:00:00Z",
            },
          ],
        },
      ],
    };

    renderCard();

    expect(screen.getByText(/mca2a_existing/i)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Regenerate/i })).toBeEnabled();
    expect(screen.getByRole("button", { name: /Revoke link/i })).toBeEnabled();
  });
});
