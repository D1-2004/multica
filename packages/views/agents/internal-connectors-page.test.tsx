// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import { WorkspaceSlugProvider } from "@multica/core/paths";
import enCommon from "../locales/en/common.json";
import enAgents from "../locales/en/agents.json";
import { InternalConnectorsPage } from "./internal-connectors-page";

const mocks = vi.hoisted(() => ({
  list: vi.fn(),
  update: vi.fn(),
  catalog: vi.fn(),
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
    listConnectorCatalog: mocks.catalog,
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
  credentialOptional: false,
  authMode: "bearer",
  allowedTools: ["read_knowledge"],
  agentIds: ["agent-1"],
  enabled: false,
  catalogSlug: "",
  writeEnabled: false,
  discoveredToolCount: 0,
  credentialAccount: "",
};

const githubConnector = {
  ...connector,
  id: "33333333-3333-4333-8333-333333333333",
  name: "GitHub",
  upstreamUrl: "https://api.githubcopilot.com/mcp/",
  authMode: "oauth",
  credentialSource: "workspace",
  credentialOptional: true,
  enabled: true,
  agentIds: [],
  allowedTools: ["get_me", "search_code", "get_file_contents"],
  catalogSlug: "github",
  discoveredToolCount: 48,
  credentialAccount: "@octocat",
};

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(
    <I18nProvider locale="en" resources={{ en: { common: enCommon, agents: enAgents } }}>
      <WorkspaceSlugProvider slug="acme">
        <QueryClientProvider client={client}><InternalConnectorsPage /></QueryClientProvider>
      </WorkspaceSlugProvider>
    </I18nProvider>,
  );
}

describe("InternalConnectorsPage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.list.mockResolvedValue([connector]);
    mocks.update.mockResolvedValue(undefined);
    mocks.catalog.mockResolvedValue([]);
  });

  it("keeps the workspace page focused on management and enables from the card", async () => {
    const user = userEvent.setup();
    renderPage();
    expect(await screen.findByText("Knowledge")).toBeInTheDocument();
    expect(screen.queryByText("Available connectors")).not.toBeInTheDocument();
    expect(screen.queryByText("Copy question and open chat")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Enable connector" }));
    await waitFor(() => expect(mocks.update).toHaveBeenCalledWith("workspace-1", connector.id, expect.objectContaining({ enabled: true })));
    // Custom connectors never send the catalog-only field.
    expect(mocks.update.mock.calls[0]![2]).not.toHaveProperty("write_enabled");
  });

  it("blocks enabling a connector without a workspace credential unless groups and people may bring their own", async () => {
    const user = userEvent.setup();
    mocks.list.mockResolvedValue([{ ...connector, credentialReady: false, credentialOptional: false }]);
    const { unmount } = renderPage();
    await screen.findByText("Knowledge");
    expect(screen.getByText("Credential needed")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Enable connector" })).toBeDisabled();
    unmount();

    mocks.list.mockResolvedValue([{ ...connector, credentialReady: false, credentialOptional: true }]);
    renderPage();
    await screen.findByText("Knowledge");
    expect(screen.getByText("Per-person or per-group credentials")).toBeInTheDocument();
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

describe("InternalConnectorsPage scope", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.update.mockResolvedValue(undefined);
  });

  it("lists only Aone FaaS connectors; official apps live on each agent", async () => {
    mocks.list.mockResolvedValue([connector, githubConnector]);
    renderPage();

    expect(await screen.findByText("Knowledge")).toBeInTheDocument();
    expect(screen.getByRole("heading", { level: 1, name: enAgents.internal_mcp.title })).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "GitHub" })).not.toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Official apps" })).not.toBeInTheDocument();
    // The gallery is gone, so the page never asks for the catalog.
    expect(mocks.catalog).not.toHaveBeenCalled();
  });
});
