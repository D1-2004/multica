import { describe, expect, it } from "vitest";
import { parseWithFallback } from "./schema";
import { ASBNetworkPolicySchema, EMPTY_ASB_NETWORK_POLICY } from "./asb-network-policy-schema";

describe("ASB network policy boundary", () => {
  it("normalizes wire fields", () => {
    expect(ASBNetworkPolicySchema.parse({ default_action: "deny", default_targets: ["mcp.dingtalk.com"], custom_targets: ["custom.example"], effective_targets: ["mcp.dingtalk.com", "custom.example"] })).toMatchObject({ available: true, customTargets: ["custom.example"], defaultAction: "deny" });
  });
  it.each([{}, { default_action: "allow" }, { default_action: "deny", default_targets: "*", custom_targets: null }])("disables editing on malformed responses", (raw) => {
    expect(parseWithFallback(raw, ASBNetworkPolicySchema, EMPTY_ASB_NETWORK_POLICY, { endpoint: "asb-network-policy" }).available).toBe(false);
  });
});
