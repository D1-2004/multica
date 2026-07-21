import { describe, expect, it } from "vitest";

import {
  providerSupportsMcpConfig,
  runtimeSupportsMcpConfig,
} from "./mcp-support";

describe("providerSupportsMcpConfig", () => {
  it("matches providers whose runtime consumes mcp_config", () => {
    expect(providerSupportsMcpConfig("claude")).toBe(true);
    expect(providerSupportsMcpConfig("codebuddy")).toBe(true);
    expect(providerSupportsMcpConfig("codex")).toBe(true);
    expect(providerSupportsMcpConfig("cursor")).toBe(true);
    expect(providerSupportsMcpConfig("hermes")).toBe(true);
    expect(providerSupportsMcpConfig("kimi")).toBe(true);
    expect(providerSupportsMcpConfig("kiro")).toBe(true);
    expect(providerSupportsMcpConfig("opencode")).toBe(true);
    expect(providerSupportsMcpConfig("openclaw")).toBe(true);
    expect(providerSupportsMcpConfig("pi")).toBe(true);
    expect(providerSupportsMcpConfig("qoder")).toBe(true);
    expect(providerSupportsMcpConfig("traecli")).toBe(true);
  });

  it("rejects providers whose runtime ignores mcp_config", () => {
    expect(providerSupportsMcpConfig("antigravity")).toBe(false);
    expect(providerSupportsMcpConfig("copilot")).toBe(false);
    expect(providerSupportsMcpConfig(undefined)).toBe(false);
    expect(providerSupportsMcpConfig(null)).toBe(false);
  });
});

describe("runtimeSupportsMcpConfig", () => {
  it("requires the mcp template capability for Pi runtimes", () => {
    expect(runtimeSupportsMcpConfig("pi", { capabilities: ["pi", "mcp"] })).toBe(true);
    expect(runtimeSupportsMcpConfig("pi", { capabilities: ["pi", "dws"] })).toBe(false);
    expect(runtimeSupportsMcpConfig("pi", {})).toBe(false);
  });

  it("keeps native MCP providers independent of FC template metadata", () => {
    expect(runtimeSupportsMcpConfig("hermes", {})).toBe(true);
    expect(runtimeSupportsMcpConfig("opencode", null)).toBe(true);
    expect(runtimeSupportsMcpConfig("copilot", { capabilities: ["mcp"] })).toBe(false);
  });
});
