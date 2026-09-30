// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { Agent } from "@multica/core/types";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../../locales/en/common.json";
import enAgents from "../../../locales/en/agents.json";
import { ConnectorsTab } from "./connectors-tab";

const mocks = vi.hoisted(() => ({ role: "owner" as string, offers: vi.fn() }));

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/permissions", () => ({
  useCurrentMember: () => ({ userId: "user-1", role: mocks.role, member: null, isLoading: false }),
}));
vi.mock("./agent-connectors-section", () => ({
  AgentConnectorsSection: () => <div>agent-connectors-section</div>,
}));
vi.mock("./context-offers-section", () => ({
  ContextOffersSection: (props: { resourceType: string; isWorkspaceAdmin: boolean; showConfigureLink: boolean }) => {
    mocks.offers(props);
    return <div>{`offers:${props.resourceType}`}</div>;
  },
}));
vi.mock("./mcp-config-tab", () => ({ McpConfigTab: () => <div>mcp-config-tab</div> }));

const agent = { id: "agent-1", name: "Helper" } as Agent;

function renderTab(canEdit: boolean) {
  const client = new QueryClient();
  render(
    <I18nProvider locale="en" resources={{ en: { common: enCommon, agents: enAgents } }}>
      <QueryClientProvider client={client}>
        <ConnectorsTab agent={agent} runtime={null} canEdit={canEdit} onSave={vi.fn()} />
      </QueryClientProvider>
    </I18nProvider>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.role = "owner";
});

describe("ConnectorsTab", () => {
  it("says it applies everywhere and stacks global connectors, offers and MCP servers", () => {
    renderTab(true);

    expect(screen.getByText(enAgents.tab_body.connectors.note)).toBeInTheDocument();
    const order = ["agent-connectors-section", "offers:connector", "mcp-config-tab"].map((text) => screen.getByText(text));
    expect(order[0]!.compareDocumentPosition(order[1]!) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(order[1]!.compareDocumentPosition(order[2]!) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(mocks.offers).toHaveBeenCalledWith(
      expect.objectContaining({ isWorkspaceAdmin: true, showConfigureLink: true }),
    );
  });

  it("tells the offer section when the editor is not a workspace admin", () => {
    mocks.role = "member";
    renderTab(true);

    expect(mocks.offers).toHaveBeenCalledWith(expect.objectContaining({ isWorkspaceAdmin: false }));
  });

  it("hides the offer catalog from viewers", () => {
    renderTab(false);

    expect(screen.queryByText("offers:connector")).not.toBeInTheDocument();
    expect(screen.getByText("agent-connectors-section")).toBeInTheDocument();
    expect(screen.getByText("mcp-config-tab")).toBeInTheDocument();
  });
});
