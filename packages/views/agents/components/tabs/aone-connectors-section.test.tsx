// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { Agent } from "@multica/core/types";
import type { AgentContextCapabilities } from "@multica/core/context-capabilities";
import { I18nProvider } from "@multica/core/i18n/react";
import { WorkspaceSlugProvider } from "@multica/core/paths";
import enCommon from "../../../locales/en/common.json";
import enAgents from "../../../locales/en/agents.json";
import { NavigationProvider, type NavigationAdapter } from "../../../navigation";
import { AoneConnectorsSection } from "./aone-connectors-section";

const mocks = vi.hoisted(() => ({
  list: vi.fn(),
  available: vi.fn(),
  update: vi.fn(),
  getCaps: vi.fn(),
  setOffers: vi.fn(),
}));

vi.mock("@multica/core/api", () => ({
  api: {
    listInternalConnectors: mocks.list,
    listAvailableInternalConnectors: mocks.available,
    updateInternalConnector: mocks.update,
    getAgentContextCapabilities: mocks.getCaps,
    setAgentContextCapabilityOffers: mocks.setOffers,
  },
}));
vi.mock("sonner", () => ({ toast: { error: vi.fn(), success: vi.fn() } }));

const copy = enAgents.tab_body.connectors;
const agent = { id: "agent-1", name: "Helper" } as Agent;

const knowledge = {
  id: "11111111-1111-4111-8111-111111111111",
  workspaceId: "ws-1",
  name: "Knowledge",
  upstreamUrl: "https://faas.example/mcp",
  credentialRef: "REF",
  credentialReady: true,
  credentialSource: "workspace",
  credentialOptional: false,
  authMode: "bearer",
  allowedTools: ["read_knowledge", "search"],
  agentIds: ["agent-1"],
  enabled: true,
  catalogSlug: "",
  writeEnabled: false,
  discoveredToolCount: 0,
  credentialAccount: "",
};
// Granted, but its workspace switch is off.
const tickets = { ...knowledge, id: "22222222-2222-4222-8222-222222222222", name: "Tickets", enabled: false };
// Granted, no workspace credential.
const wiki = { ...knowledge, id: "33333333-3333-4333-8333-333333333333", name: "Wiki", credentialReady: false };
// Offered to groups and people only.
const search = { ...knowledge, id: "44444444-4444-4444-8444-444444444444", name: "Search", agentIds: [], authMode: "none" };
// In the library, not used by this agent.
const billing = { ...knowledge, id: "55555555-5555-4555-8555-555555555555", name: "Billing", agentIds: ["agent-2"] };
// An official app: never listed here.
const github = {
  ...knowledge,
  id: "66666666-6666-4666-8666-666666666666",
  name: "GitHub",
  authMode: "oauth",
  catalogSlug: "github",
  agentIds: ["agent-1"],
};

function caps(connectorIds: string[]): AgentContextCapabilities {
  return {
    enabled: true,
    library: {
      connectors: [
        { id: search.id, name: "Search", enabled: true, authMode: "none", catalogSlug: "" },
        { id: knowledge.id, name: "Knowledge", enabled: true, authMode: "bearer", catalogSlug: "" },
      ],
      skills: [],
    },
    offers: { connectorIds, skillIds: ["skill-1"] },
    scenes: [
      {
        scopeKey: "cid-1",
        scopeTitle: "Sales",
        bindings: [{ resourceType: "connector", resourceId: knowledge.id, enabled: true, shareInGroups: false }],
        credentialCount: 0,
      },
    ],
    orgs: [],
    persons: [],
    configureUrl: "",
  };
}

function renderSection({ canEdit = true, isAdmin = true }: { canEdit?: boolean; isAdmin?: boolean } = {}) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const navigation: NavigationAdapter = {
    push: vi.fn(),
    replace: vi.fn(),
    back: vi.fn(),
    pathname: "/acme/agents/agent-1",
    searchParams: new URLSearchParams("view=mcp_config"),
    getShareableUrl: (path) => path,
  };
  render(
    <I18nProvider locale="en" resources={{ en: { common: enCommon, agents: enAgents } }}>
      <WorkspaceSlugProvider slug="acme">
        <NavigationProvider value={navigation}>
          <QueryClientProvider client={client}>
            <AoneConnectorsSection agent={agent} wsId="ws-1" canEdit={canEdit} isAdmin={isAdmin} />
          </QueryClientProvider>
        </NavigationProvider>
      </WorkspaceSlugProvider>
    </I18nProvider>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.list.mockResolvedValue([knowledge, tickets, wiki, search, billing, github]);
  mocks.getCaps.mockResolvedValue(caps([knowledge.id, search.id]));
  mocks.setOffers.mockImplementation(async (_ws: string, _agent: string, input: { connectorIds: string[] }) =>
    caps(input.connectorIds),
  );
  mocks.update.mockResolvedValue(undefined);
  mocks.available.mockResolvedValue([]);
});

describe("AoneConnectorsSection for workspace admins", () => {
  it("lists the agent's Aone FaaS connectors with their workspace status, never official apps", async () => {
    renderSection();

    const row = await screen.findByRole("listitem", { name: "Knowledge" });
    expect(within(row).getByText(copy.status_enabled)).toBeInTheDocument();
    expect(within(row).getByText(`faas.example · 2 tools · ${copy.auth_bearer}`)).toBeInTheDocument();
    expect(within(screen.getByRole("listitem", { name: "Tickets" })).getByText(copy.disabled_in_workspace)).toBeInTheDocument();
    expect(within(screen.getByRole("listitem", { name: "Wiki" })).getByText(copy.status_missing_credential)).toBeInTheDocument();
    // An offered-only connector stays visible, marked as such.
    const offeredOnly = screen.getByRole("listitem", { name: "Search" });
    expect(within(offeredOnly).getByText(copy.status_offer_only)).toBeInTheDocument();
    expect(within(offeredOnly).getByText(`faas.example · 2 tools · ${copy.auth_none}`)).toBeInTheDocument();
    expect(screen.queryByRole("listitem", { name: "GitHub" })).not.toBeInTheDocument();
    expect(screen.queryByRole("listitem", { name: "Billing" })).not.toBeInTheDocument();

    await waitFor(() =>
      expect(within(row).getByRole("switch", { name: "Let groups and people turn on Knowledge" })).toBeChecked(),
    );
    expect(within(screen.getByRole("listitem", { name: "Wiki" })).getByRole("switch")).not.toBeChecked();
    // One row per connector: the short switch label and an icon button for
    // removal, no hint paragraph or extra strip.
    expect(within(row).getByText(copy.offer_switch)).toHaveAttribute("aria-hidden", "true");
    expect(within(row).getByRole("button", { name: "Remove Knowledge" })).not.toHaveTextContent(copy.remove);
    expect(screen.getByRole("link", { name: copy.aone_manage })).toHaveAttribute("href", "/acme/internal-connectors");
  });

  it("does not report a missing credential for an offered-only connector", async () => {
    // Groups and people bring their own token for a connector that is only
    // offered, so no workspace credential is missing there.
    const docs = { ...knowledge, id: "77777777-7777-4777-8777-777777777777", name: "Docs", agentIds: [], credentialReady: false };
    mocks.list.mockResolvedValue([knowledge, wiki, docs]);
    mocks.getCaps.mockResolvedValue(caps([knowledge.id, docs.id]));
    renderSection();

    const row = await screen.findByRole("listitem", { name: "Docs" });
    expect(within(row).getByText(copy.status_offer_only)).toBeInTheDocument();
    expect(within(row).queryByText(copy.status_missing_credential)).not.toBeInTheDocument();
    // A granted connector without a workspace credential still warns.
    expect(within(screen.getByRole("listitem", { name: "Wiki" })).getByText(copy.status_missing_credential)).toBeInTheDocument();
  });

  it("offers a connector to groups and people at once", async () => {
    const user = userEvent.setup();
    renderSection();

    const row = await screen.findByRole("listitem", { name: "Wiki" });
    const toggle = within(row).getByRole("switch");
    // Base UI marks a disabled switch with aria-disabled.
    await waitFor(() => expect(toggle).not.toHaveAttribute("aria-disabled", "true"));
    await user.click(toggle);

    await waitFor(() =>
      expect(mocks.setOffers).toHaveBeenCalledWith("ws-1", "agent-1", {
        connectorIds: [knowledge.id, search.id, wiki.id],
        skillIds: ["skill-1"],
      }),
    );
  });

  it("asks before taking away an offer that scenes use", async () => {
    const user = userEvent.setup();
    renderSection();

    const row = await screen.findByRole("listitem", { name: "Knowledge" });
    const toggle = within(row).getByRole("switch");
    // Base UI marks a disabled switch with aria-disabled.
    await waitFor(() => expect(toggle).not.toHaveAttribute("aria-disabled", "true"));
    await user.click(toggle);
    expect(mocks.setOffers).not.toHaveBeenCalled();
    const dialog = await screen.findByRole("alertdialog");
    expect(within(dialog).getByText("It turns off right away in 1 scenes and for 0 people.")).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: copy.offer_off_confirm }));

    await waitFor(() =>
      expect(mocks.setOffers).toHaveBeenCalledWith("ws-1", "agent-1", {
        connectorIds: [search.id],
        skillIds: ["skill-1"],
      }),
    );
  });

  it("removes a connector from the agent (grant and offer) only after confirmation", async () => {
    const user = userEvent.setup();
    renderSection();

    await screen.findByRole("listitem", { name: "Knowledge" });
    await user.click(screen.getByRole("button", { name: "Remove Knowledge" }));
    expect(mocks.update).not.toHaveBeenCalled();
    const dialog = await screen.findByRole("alertdialog");
    await user.click(within(dialog).getByRole("button", { name: copy.remove }));

    await waitFor(() =>
      expect(mocks.update).toHaveBeenCalledWith("ws-1", knowledge.id, expect.objectContaining({ agent_ids: [] })),
    );
    await waitFor(() =>
      expect(mocks.setOffers).toHaveBeenCalledWith("ws-1", "agent-1", {
        connectorIds: [search.id],
        skillIds: ["skill-1"],
      }),
    );
  });

  it("adds an Aone FaaS connector from the library, official apps excluded", async () => {
    const user = userEvent.setup();
    renderSection();

    await screen.findByRole("listitem", { name: "Knowledge" });
    await user.click(screen.getByRole("button", { name: copy.aone_add }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).queryByText("GitHub")).not.toBeInTheDocument();
    expect(within(dialog).queryByText("Knowledge")).not.toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "Add Billing" }));

    await waitFor(() =>
      expect(mocks.update).toHaveBeenCalledWith(
        "ws-1",
        billing.id,
        expect.objectContaining({ agent_ids: ["agent-2", "agent-1"] }),
      ),
    );
  });
});

describe("AoneConnectorsSection for agent owners who are not admins", () => {
  it("shows the Aone FaaS rows read only, without the admin library", async () => {
    mocks.available.mockResolvedValue([
      { id: knowledge.id, name: "Knowledge", serverName: "c1", agentId: "agent-1", agentName: "Helper", tools: ["a"], catalogSlug: "" },
      { id: github.id, name: "GitHub", serverName: "c6", agentId: "agent-1", agentName: "Helper", tools: [], catalogSlug: "github" },
      { id: billing.id, name: "Billing", serverName: "c5", agentId: "agent-2", agentName: "Other", tools: [], catalogSlug: "" },
    ]);
    renderSection({ isAdmin: false });

    const row = await screen.findByRole("listitem", { name: "Knowledge" });
    expect(within(row).getByRole("switch")).toHaveAttribute("aria-disabled", "true");
    expect(within(row).getByRole("switch")).toBeChecked();
    // Offered-only Aone connectors come from the offer catalog.
    const search = screen.getByRole("listitem", { name: "Search" });
    expect(within(search).getByText(copy.status_offer_only)).toBeInTheDocument();
    expect(screen.queryByRole("listitem", { name: "GitHub" })).not.toBeInTheDocument();
    expect(screen.queryByRole("listitem", { name: "Billing" })).not.toBeInTheDocument();
    expect(screen.getByText(copy.aone_admin_only)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: copy.aone_add })).not.toBeInTheDocument();
    expect(mocks.list).not.toHaveBeenCalled();
  });
});

describe("AoneConnectorsSection for viewers", () => {
  it("lists the Aone FaaS connectors granted to the agent without reading the offer catalog", async () => {
    mocks.available.mockResolvedValue([
      { id: knowledge.id, name: "Knowledge", serverName: "c1", agentId: "agent-1", agentName: "Helper", tools: ["a"], catalogSlug: "" },
      { id: github.id, name: "GitHub", serverName: "c6", agentId: "agent-1", agentName: "Helper", tools: [], catalogSlug: "github" },
    ]);
    renderSection({ canEdit: false, isAdmin: false });

    expect(await screen.findByRole("listitem", { name: "Knowledge" })).toBeInTheDocument();
    expect(screen.queryByRole("listitem", { name: "GitHub" })).not.toBeInTheDocument();
    expect(screen.getByRole("heading", { name: copy.aone_title })).toBeInTheDocument();
    expect(mocks.getCaps).not.toHaveBeenCalled();
  });
});
