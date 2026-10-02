import { describe, expect, it } from "vitest";
import { agentSceneMemoryKeys, agentSceneMemoryOptions, agentSceneMemoryDetailOptions } from "./queries";

describe("scene memory loop cache isolation", () => {
  it("separates both list and detail keys by selected loop", () => {
    expect(agentSceneMemoryKeys.list("ws", "agent", "employee")).not.toEqual(agentSceneMemoryKeys.list("ws", "agent", "coordinator"));
    expect(agentSceneMemoryKeys.detail("ws", "agent", "scene", "employee")).not.toEqual(agentSceneMemoryKeys.detail("ws", "agent", "scene", "coordinator"));
    expect(agentSceneMemoryOptions("ws", "agent", true, "employee").queryKey).toEqual(agentSceneMemoryKeys.list("ws", "agent", "employee"));
    expect(agentSceneMemoryDetailOptions("ws", "agent", "scene", true, "employee").queryKey).toEqual(agentSceneMemoryKeys.detail("ws", "agent", "scene", "employee"));
  });
});
