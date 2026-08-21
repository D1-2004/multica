// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/en/common.json";
import enSettings from "../../locales/en/settings.json";

const createTokenSpy = vi.hoisted(() => vi.fn());
const copyTextSpy = vi.hoisted(() => vi.fn());

vi.mock("@multica/core/api", () => ({
  api: {
    getBaseUrl: () => "https://api.example.com",
    createPersonalAccessToken: createTokenSpy,
  },
}));

vi.mock("@multica/ui/lib/clipboard", () => ({ copyText: copyTextSpy }));
vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

import { MCPConnectionsTab } from "./mcp-connections-tab";

function renderTab() {
  return render(
    <I18nProvider
      locale="en"
      resources={{ en: { common: enCommon, settings: enSettings } }}
    >
      <MCPConnectionsTab />
    </I18nProvider>,
  );
}

describe("MCPConnectionsTab", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    copyTextSpy.mockResolvedValue(true);
    createTokenSpy.mockResolvedValue({ token: "mul_test_secret" });
  });

  it("explains that connecting a client requires an API Key", () => {
    renderTab();

    expect(screen.getByText(/an API Key is required/i)).toBeInTheDocument();
  });

  it("surfaces the endpoint and only the four supported clients", () => {
    renderTab();

    expect(
      screen.getByDisplayValue<HTMLInputElement>(
        "https://api.example.com/api/mcp",
      ).readOnly,
    ).toBe(true);

    const clients = screen.getAllByRole("button", { name: /^Configure / });
    expect(clients).toHaveLength(4);
    expect(clients.map((button) => button.textContent)).toEqual([
      expect.stringContaining("Codex"),
      expect.stringContaining("Claude"),
      expect.stringContaining("Qoder"),
      expect.stringContaining("QoderWork"),
    ]);
  });

  it("creates a dedicated API Key and copies a ready Codex config", async () => {
    const user = userEvent.setup();
    renderTab();

    await user.click(screen.getByRole("button", { name: "Configure Codex" }));
    await user.click(
      screen.getByRole("button", { name: "Create API Key and copy" }),
    );

    await waitFor(() => {
      expect(createTokenSpy).toHaveBeenCalledWith({
        name: "Multica MCP · Codex",
        expires_in_days: 90,
      });
    });
    expect(copyTextSpy).toHaveBeenCalledWith(
      `[mcp_servers.multica]\nurl = "https://api.example.com/api/mcp"\nhttp_headers = { Authorization = "Bearer mul_test_secret" }\nenabled = true`,
    );
  });

  it("copies QoderWork's Streamable HTTP import JSON", async () => {
    const user = userEvent.setup();
    renderTab();

    await user.click(
      screen.getByRole("button", { name: "Configure QoderWork" }),
    );
    await user.click(
      screen.getByRole("button", { name: "Create API Key and copy" }),
    );

    await waitFor(() => {
      expect(copyTextSpy).toHaveBeenCalledWith(
        JSON.stringify(
          {
            mcpServers: {
              multica: {
                type: "streamable-http",
                url: "https://api.example.com/api/mcp",
                headers: {
                  Authorization: "Bearer mul_test_secret",
                },
              },
            },
          },
          null,
          2,
        ),
      );
    });
  });
});
