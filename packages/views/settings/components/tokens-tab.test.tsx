// @vitest-environment jsdom

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/en/common.json";
import enSettings from "../../locales/en/settings.json";

vi.mock("@multica/core/api", () => ({
  api: {
    listPersonalAccessTokens: vi.fn().mockResolvedValue([]),
    createPersonalAccessToken: vi.fn(),
    revokePersonalAccessToken: vi.fn(),
  },
}));

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

import { TokensTab } from "./tokens-tab";

describe("TokensTab", () => {
  it("keeps MCP connections out of the API Tokens tab", async () => {
    render(
      <I18nProvider
        locale="en"
        resources={{ en: { common: enCommon, settings: enSettings } }}
      >
        <TokensTab />
      </I18nProvider>,
    );
    await screen.findByText(enSettings.tokens.empty);

    expect(
      screen.queryByRole("heading", { name: "Multica MCP" }),
    ).not.toBeInTheDocument();
  });
});
