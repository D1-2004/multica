// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../locales/en/common.json";
import enAgents from "../locales/en/agents.json";
import { InternalConnectorsPage } from "./internal-connectors-page";

const mocks = vi.hoisted(() => ({
  list: vi.fn(),
  update: vi.fn(),
}));

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "workspace-1" }));
vi.mock("@multica/core/auth", () => ({
  useAuthStore: (selector: (state: { user: { id: string } }) => unknown) => selector({ user: { id: "user-1" } }),
}));
vi.mock("@multica/core/workspace/queries", () => ({
  memberListOptions: () => ({ queryKey: ["members", "workspace-1"], queryFn: async () => [{ user_id: "user-1", role: "owner" }] }),
  agentListOptions: () => ({ queryKey: ["agents", "workspace-1"], queryFn: async () => [{ id: "agent-1", name: "Probe" }] }),
}));
vi.mock("@multica/core/api", () => ({
  api: {
    listInternalConnectors: mocks.list,
    updateInternalConnector: mocks.update,
  },
}));

const connector = {
  id: "11111111-1111-4111-8111-111111111111",
  workspaceId: "workspace-1",
  name: "Knowledge",
  upstreamUrl: "https://approved.example/mcp",
  credentialRef: "MULTICA_INTERNAL_MCP_BEARER_TEST",
  credentialReady: true,
  credentialSource: "workspace",
  authMode: "bearer",
  allowedTools: ["read_knowledge"],
  agentIds: ["agent-1"],
  enabled: false,
};

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <I18nProvider locale="en" resources={{ en: { common: enCommon, agents: enAgents } }}>
      <QueryClientProvider client={client}><InternalConnectorsPage /></QueryClientProvider>
    </I18nProvider>,
  );
}

describe("InternalConnectorsPage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.list.mockResolvedValue([connector]);
    mocks.update.mockResolvedValue(undefined);
  });

  it("keeps the workspace page focused on management and enables from the card", async () => {
    const user = userEvent.setup();
    renderPage();
    expect(await screen.findByText("Knowledge")).toBeInTheDocument();
    expect(screen.queryByText("Available connectors")).not.toBeInTheDocument();
    expect(screen.queryByText("Copy question and open chat")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Enable connector" }));
    await waitFor(() => expect(mocks.update).toHaveBeenCalledWith("workspace-1", connector.id, expect.objectContaining({ enabled: true })));
  });

  it("uses explicit state actions inside management instead of an enable checkbox", async () => {
    const user = userEvent.setup();
    renderPage();
    await screen.findByText("Knowledge");
    await user.click(screen.getByRole("button", { name: "Manage" }));
    expect(screen.getByText("Availability")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Keep disabled" })).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: "Enable connector" })).toHaveLength(2);
    expect(screen.queryByRole("checkbox", { name: "Enable connector" })).not.toBeInTheDocument();
  });
});
