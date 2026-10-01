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
    expect(groupForConfigView("dsh")).toBe("execution");
    expect(normalizeDetailView("dsh_plugins")).toBe("dsh");
    expect(normalizeDetailView("dsh_home")).toBe("dsh");
    expect(groupForConfigView("llm_trace")).toBe("management");
    expect(groupForConfigView("publish")).toBe("management");
    expect(groupForConfigView("export")).toBe("management");
    expect(sectionForView("publish")).toBe("configuration");
  });

  it("lists skills, connectors and MCP apps as the capabilities", () => {
    const capabilities = AGENT_CONFIG_GROUPS.find(
      (group) => group.id === "capabilities",
    );
    expect(capabilities?.items.map((item) => item.id)).toEqual([
      "skills",
      "mcp_config",
      "composio_mcp",
    ]);
  });

  it("moves legacy identity links into Digital Employee", () => {
    expect(normalizeDetailView("identity")).toBe("digital_employee");
    expect(sectionForView("digital_employee")).toBe("configuration");
  });

  it("rejects unknown detail views", () => {
    expect(normalizeDetailView("not-a-view")).toBeNull();
  });

  it("opens scenes as a primary section", () => {
    expect(normalizeDetailView("scenes")).toBe("scenes");
    expect(sectionForView("scenes")).toBe("scenes");
  });

  it("sends old inbound and memory links to scenes", () => {
    expect(normalizeDetailView("inbound")).toBe("scenes");
    expect(normalizeDetailView("memory")).toBe("scenes");
  });

  it("sends old context capability links to the connectors tab", () => {
    expect(normalizeDetailView("context_capabilities")).toBe("mcp_config");
    expect(sectionForView("mcp_config")).toBe("configuration");
  });
});
