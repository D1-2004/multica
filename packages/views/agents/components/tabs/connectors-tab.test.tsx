// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { Agent } from "@multica/core/types";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../../locales/en/common.json";
import enAgents from "../../../locales/en/agents.json";
import { NavigationProvider, type NavigationAdapter } from "../../../navigation";
import { ConnectorsTab } from "./connectors-tab";

const mocks = vi.hoisted(() => ({ role: "owner" as string, aone: vi.fn(), apps: vi.fn() }));

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/permissions", () => ({
  useCurrentMember: () => ({ userId: "user-1", role: mocks.role, member: null, isLoading: false }),
}));
vi.mock("./aone-connectors-section", () => ({
  AoneConnectorsSection: (props: { canEdit: boolean; isAdmin: boolean }) => {
    mocks.aone(props);
    return <div>aone-connectors</div>;
  },
}));
vi.mock("./connected-apps-section", () => ({
  ConnectedAppsSection: (props: { canEdit: boolean }) => {
    mocks.apps(props);
    return <div>connected-apps</div>;
  },
}));
vi.mock("./mcp-config-tab", () => ({ McpConfigTab: () => <div>mcp-config-tab</div> }));

const copy = enAgents.tab_body.connectors;
const agent = { id: "agent-1", name: "Helper" } as Agent;

function renderTab({
  canEdit = true,
  supportsOwnMcpConfig = true,
  search = "view=mcp_config",
}: { canEdit?: boolean; supportsOwnMcpConfig?: boolean; search?: string } = {}) {
  const client = new QueryClient();
  const navigation: NavigationAdapter = {
    push: vi.fn(),
    replace: vi.fn(),
    back: vi.fn(),
    pathname: "/acme/agents/agent-1",
    searchParams: new URLSearchParams(search),
    getShareableUrl: (path) => path,
  };
  render(
    <I18nProvider locale="en" resources={{ en: { common: enCommon, agents: enAgents } }}>
      <NavigationProvider value={navigation}>
        <QueryClientProvider client={client}>
          <ConnectorsTab
            agent={agent}
            runtime={null}
            canEdit={canEdit}
            supportsOwnMcpConfig={supportsOwnMcpConfig}
            onSave={vi.fn()}
          />
        </QueryClientProvider>
      </NavigationProvider>
    </I18nProvider>,
  );
  return { navigation };
}

function follows(first: HTMLElement, second: HTMLElement) {
  return Boolean(first.compareDocumentPosition(second) & Node.DOCUMENT_POSITION_FOLLOWING);
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.role = "owner";
});

describe("ConnectorsTab", () => {
  it("stacks the MCP block (Aone FaaS, then custom MCP servers) above the connected apps", () => {
    renderTab();

    expect(screen.getByText(copy.note)).toBeInTheDocument();
    const mcp = screen.getByRole("heading", { level: 2, name: copy.mcp_title });
    const aone = screen.getByText("aone-connectors");
    const custom = screen.getByRole("heading", { level: 3, name: copy.custom_title });
    const apps = screen.getByText("connected-apps");
    expect(follows(mcp, aone)).toBe(true);
    expect(follows(aone, custom)).toBe(true);
    expect(follows(custom, screen.getByText("mcp-config-tab"))).toBe(true);
    expect(follows(screen.getByText("mcp-config-tab"), apps)).toBe(true);
    expect(mocks.aone).toHaveBeenCalledWith(expect.objectContaining({ canEdit: true, isAdmin: true }));
    expect(mocks.apps).toHaveBeenCalledWith(expect.objectContaining({ canEdit: true }));
  });

  it("tells the sections when the editor is not a workspace admin", () => {
    mocks.role = "member";
    renderTab();

    expect(mocks.aone).toHaveBeenCalledWith(expect.objectContaining({ canEdit: true, isAdmin: false }));
  });

  it("hides the agent's own MCP servers when the runtime does not read mcp_config", () => {
    renderTab({ supportsOwnMcpConfig: false });

    expect(screen.queryByText("mcp-config-tab")).not.toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: copy.custom_title })).not.toBeInTheDocument();
    expect(screen.getByText("aone-connectors")).toBeInTheDocument();
    expect(screen.getByText("connected-apps")).toBeInTheDocument();
  });
});
