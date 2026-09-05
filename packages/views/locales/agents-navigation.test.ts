import { describe, expect, it } from "vitest";
import en from "./en/agents.json";
import ja from "./ja/agents.json";
import ko from "./ko/agents.json";
import zhHans from "./zh-Hans/agents.json";

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
});
