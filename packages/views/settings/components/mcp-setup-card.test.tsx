// @vitest-environment jsdom
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/en/common.json";
import enSettings from "../../locales/en/settings.json";
const mocks = vi.hoisted(() => ({
  create: vi.fn(),
  list: vi.fn(),
  revoke: vi.fn(),
  copy: vi.fn(),
  role: "owner",
}));
vi.mock("@multica/core/api", () => ({
  api: {
    createWorkspaceMCPConnection: mocks.create,
    listWorkspaceMCPConnections: mocks.list,
    revokeWorkspaceMCPConnection: mocks.revoke,
  },
}));
vi.mock("@multica/core/config", () => ({ useFeatureEnabled: () => true }));
vi.mock("@multica/core/paths", () => ({
  useCurrentWorkspace: () => ({ id: "ws-1", name: "My team" }),
}));
vi.mock("@multica/core/permissions", () => ({
  useCurrentMember: () => ({ role: mocks.role }),
}));
vi.mock("@multica/ui/lib/clipboard", () => ({ copyText: mocks.copy }));
vi.mock("sonner", () => ({ toast: { error: vi.fn() } }));
import { MCPConnectionsTab } from "./mcp-connections-tab";
function renderTab() {
  return render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <I18nProvider
        locale="en"
        resources={{ en: { common: enCommon, settings: enSettings } }}
      >
        <MCPConnectionsTab />
      </I18nProvider>
    </QueryClientProvider>,
  );
}
describe("Workspace MCP connections", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.role = "owner";
    mocks.list.mockResolvedValue([]);
    mocks.create.mockResolvedValue({
      id: "conn",
      url: "https://example.test/api/mcp/workspaces/ws-1/connect/wmcp_secret",
    });
    mocks.copy.mockResolvedValue(true);
  });
  it("creates and copies a workspace-only link with read-only default", async () => {
    const user = userEvent.setup();
    renderTab();
    expect(
      screen.getByText("Only this workspace: My team"),
    ).toBeInTheDocument();
    await user.type(
      screen.getByRole("textbox", { name: "Connection name" }),
      "My OpenCode",
    );
    await user.click(screen.getByRole("button", { name: "Create MCP link" }));
    await screen.findByText("Your MCP link is ready");
    expect(mocks.create).toHaveBeenCalledWith(
      "ws-1",
      expect.objectContaining({ name: "My OpenCode", scopes: ["read"] }),
    );
    await user.click(screen.getByRole("button", { name: "Copy link" }));
    expect(mocks.copy).toHaveBeenCalledWith(
      "https://example.test/api/mcp/workspaces/ws-1/connect/wmcp_secret",
    );
  });
  it("does not fetch or issue admin-managed connections for members", () => {
    mocks.role = "member";
    renderTab();
    expect(screen.getByText(/Ask a workspace owner/)).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Create MCP link" }),
    ).not.toBeInTheDocument();
    expect(mocks.list).not.toHaveBeenCalled();
  });
  it("revokes only the selected connection after confirmation", async () => {
    mocks.list.mockResolvedValue([
      {
        id: "conn",
        name: "My OpenCode",
        scopes: ["read"],
        expiresAt: "2099-01-01T00:00:00Z",
        revokedAt: null,
      },
    ]);
    mocks.revoke.mockResolvedValue(undefined);
    const user = userEvent.setup();
    renderTab();
    await screen.findByText("My OpenCode");
    await user.click(screen.getByRole("button", { name: "Revoke" }));
    expect(mocks.revoke).not.toHaveBeenCalled();
    const buttons = screen.getAllByRole("button", { name: "Revoke" });
    await user.click(buttons[buttons.length - 1]!);
    await waitFor(() =>
      expect(mocks.revoke).toHaveBeenCalledWith("ws-1", "conn"),
    );
  });
});
