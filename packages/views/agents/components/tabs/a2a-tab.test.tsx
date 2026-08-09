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
  useUpdateAgentA2AClient: () => ({ mutateAsync: vi.fn(), isPending: false }),
  useCreateAgentA2ACredential: () => ({
    mutateAsync: createCredentialSpy,
    isPending: false,
  }),
  useDeleteAgentA2ACredential: () => ({
    mutateAsync: vi.fn(),
    isPending: false,
  }),
}));

vi.mock("@multica/ui/lib/clipboard", () => ({
  copyText: vi.fn().mockResolvedValue(true),
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
          name: "Local coding agent",
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
  });

  it("renders public Card and non-standard exports without a raw credential", () => {
    const { container } = renderTab();

    expect(
      screen.getByText(/configured instructions, skills, tools/i),
    ).toBeInTheDocument();
    expect(screen.getByDisplayValue(/public-agent-1\/\.well-known\/agent-card\.json$/)).toBeInTheDocument();
    expect(screen.getByText(/not a standard A2A file/i)).toBeInTheDocument();
    expect(container.textContent).toContain('"tokenEnv": "MULTICA_A2A_TOKEN"');
    expect(container.textContent).toContain(
      "Authorization: Bearer $MULTICA_A2A_TOKEN",
    );
    expect(container.textContent).not.toContain(rawToken);
  });

  it("does not offer empty exports when the endpoint has no public URLs", () => {
    configRef.current = {
      endpoint: {
        enabled: true,
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

    const { container } = renderTab();

    expect(
      screen.getByText(/Public A2A URLs are not available yet/i),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /Copy Agent Card URL/i }),
    ).not.toBeInTheDocument();
    expect(screen.queryByText("multica-a2a.json")).not.toBeInTheDocument();
    expect(screen.queryByText(/cURL example/i)).not.toBeInTheDocument();
    expect(container.textContent).not.toContain("MULTICA_A2A_TOKEN");
  });

  it("keeps a created credential only in the one-time dialog and clears it on close", async () => {
    const user = userEvent.setup();
    renderTab();

    await user.click(
      screen.getByRole("button", { name: /^Create credential$/i }),
    );
    await user.click(screen.getByRole("button", { name: /^Create$/i }));

    expect(await screen.findByDisplayValue(rawToken)).toBeInTheDocument();
    await user.click(
      screen.getByRole("checkbox", {
        name: /I stored this credential securely/i,
      }),
    );
    await user.click(screen.getByRole("button", { name: /^Done$/i }));

    await waitFor(() => {
      expect(screen.queryByDisplayValue(rawToken)).not.toBeInTheDocument();
    });
    expect(createCredentialSpy).toHaveBeenCalledTimes(1);
  });
});
