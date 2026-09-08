import { describe, expect, it } from "vitest";
import {
  AGENT_CONFIG_GROUPS,
  groupForConfigView,
  normalizeDetailView,
  sectionForView,
} from "./agent-config-navigation";

describe("agent configuration navigation", () => {
  it("keeps every configuration concept in one stable group", () => {
    expect(AGENT_CONFIG_GROUPS.map((group) => group.id)).toEqual([
      "identity_goals",
      "capabilities",
      "connections",
      "execution",
      "management",
    ]);
    expect(groupForConfigView("digital_employee")).toBe("identity_goals");
    expect(groupForConfigView("mcp_config")).toBe("capabilities");
    expect(groupForConfigView("integrations")).toBe("connections");
    expect(groupForConfigView("general")).toBe("execution");
    expect(groupForConfigView("llm_trace")).toBe("management");
    expect(groupForConfigView("publish")).toBe("management");
    expect(groupForConfigView("export")).toBe("management");
    expect(sectionForView("publish")).toBe("configuration");
  });

  it("moves legacy identity links into Digital Employee", () => {
    expect(normalizeDetailView("identity")).toBe("digital_employee");
    expect(sectionForView("digital_employee")).toBe("configuration");
  });

  it("rejects unknown detail views", () => {
    expect(normalizeDetailView("not-a-view")).toBeNull();
  });

  it("restores inbound deep links as a primary section", () => {
    expect(normalizeDetailView("inbound")).toBe("inbound");
    expect(sectionForView("inbound")).toBe("inbound");
  });
});
