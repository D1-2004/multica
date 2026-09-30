import { describe, expect, it } from "vitest";
import { agentScenesOptions, contextCapabilityKeys, contextConfigKeys } from "./queries";

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

  it("nests the admin scene list and scene detail under the agent", () => {
    const agentKey = contextCapabilityKeys.agent("ws-1", "agent-1");
    const scenesKey = contextCapabilityKeys.scenes("ws-1", "agent-1");
    const sceneKey = contextCapabilityKeys.scene("ws-1", "agent-1", "cid1");
    expect(scenesKey.slice(0, agentKey.length)).toEqual([...agentKey]);
    expect(sceneKey.slice(0, scenesKey.length)).toEqual([...scenesKey]);
    expect(contextCapabilityKeys.scenes("ws-2", "agent-1")).not.toEqual(scenesKey);
  });
});

describe("admin scene list paging", () => {
  const page = (count: number, hasMore: boolean) => ({
    scenes: Array.from({ length: count }, () => ({}) as never),
    hasMore,
  });

  it("continues after the loaded rows and stops without more rows", () => {
    const { getNextPageParam } = agentScenesOptions("ws-1", "agent-1");
    expect(getNextPageParam(page(50, true), [], 0, [])).toBe(50);
    expect(getNextPageParam(page(50, false), [], 50, [])).toBeUndefined();
    // A server that claims more but sends nothing must not loop.
    expect(getNextPageParam(page(0, true), [], 50, [])).toBeUndefined();
  });
});
