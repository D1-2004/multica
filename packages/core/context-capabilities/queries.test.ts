import { describe, expect, it } from "vitest";
import { contextCapabilityKeys, contextConfigKeys } from "./queries";

describe("context capability query keys", () => {
  it("scopes admin keys by workspace and agent", () => {
    expect(contextCapabilityKeys.agent("ws-1", "agent-1")).toEqual([
      "workspaces",
      "ws-1",
      "context-capabilities",
      "agent-1",
    ]);
    expect(contextCapabilityKeys.agent("ws-2", "agent-1")).not.toEqual(
      contextCapabilityKeys.agent("ws-1", "agent-1"),
    );
  });

  it("nests scene detail under the agent so one invalidation covers both", () => {
    const agentKey = contextConfigKeys.agent("agent-1");
    const sceneKey = contextConfigKeys.scene("agent-1", "cid1");
    expect(sceneKey.slice(0, agentKey.length)).toEqual([...agentKey]);
    expect(contextConfigKeys.agent("agent-2")).not.toEqual(agentKey);
  });
});
