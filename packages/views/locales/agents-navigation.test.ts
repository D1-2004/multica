import { describe, expect, it } from "vitest";
import en from "./en/agents.json";
import ja from "./ja/agents.json";
import ko from "./ko/agents.json";
import zhHans from "./zh-Hans/agents.json";
import enLayout from "./en/layout.json";
import enSettings from "./en/settings.json";
import zhHansLayout from "./zh-Hans/layout.json";
import zhHansSettings from "./zh-Hans/settings.json";

const locales = { en, ja, ko, zhHans };
const tabKeys = [
  "configuration",
  "identity_goals",
  "capabilities",
  "connections",
  "execution",
  "management",
  "digital_employee",
  "integrations",
  "mcp_access",
] as const;

describe("agent navigation translations", () => {
  it.each(Object.entries(locales))("provides every navigation label in %s", (_locale, resources) => {
    for (const key of tabKeys) {
      expect(resources.tabs[key].trim()).not.toBe("");
    }
    expect(resources.tab_body.digital_employee.identity_title.trim()).not.toBe("");
    expect(resources.tab_body.digital_employee.voice_title.trim()).not.toBe("");
    expect(resources.tab_body.digital_employee.message_title.trim()).not.toBe("");
  });

  it("uses plain-language computer and application-integration labels", () => {
    expect(enLayout.nav.runners).toBe("My Computer");
    expect(en.tabs.runner).toBe("My Computer");
    expect(en.tab_body.runner.execution_title).toBe("Use My Computer");
    expect(enSettings.page.tabs.local_runner).toBe("My Computer");
    expect(enSettings.page.tabs.integrations).toBe("App Integrations");

    expect(zhHansLayout.nav.runners).toBe("我的电脑");
    expect(zhHans.tabs.runner).toBe("我的电脑");
    expect(zhHans.tab_body.runner.execution_title).toBe("使用我的电脑");
    expect(zhHansSettings.page.tabs.local_runner).toBe("我的电脑");
    expect(zhHansSettings.page.tabs.integrations).toBe("应用集成");
    expect(zhHansSettings.local_runner.title).toBe("我的电脑");
    expect(zhHansSettings.local_runner.empty_title).not.toContain("Runner");

    expect(en.tab_body.composio_mcp.empty_link_to_settings).toContain(
      "App Integrations",
    );
    expect(en.tab_body.integrations.members_note).toContain(
      "App Integrations",
    );
    expect(zhHans.tab_body.composio_mcp.empty_link_to_settings).toContain(
      "应用集成",
    );
    expect(zhHans.tab_body.integrations.members_note).toContain("应用集成");
  });
});
