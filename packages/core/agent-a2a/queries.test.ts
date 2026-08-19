import { describe, expect, it } from "vitest";
import { agentA2AKeys } from "./queries";

describe("agent A2A query keys", () => {
  it("isolates config by both workspace and Agent", () => {
    expect(agentA2AKeys.config("ws-1", "agent-2")).toEqual([
      "workspaces",
      "ws-1",
      "agents",
      "agent-2",
      "a2a",
      "config",
    ]);
    expect(agentA2AKeys.config("ws-2", "agent-2"))
      .not.toEqual(agentA2AKeys.config("ws-1", "agent-2"));
    expect(agentA2AKeys.config("ws-1", "agent-3"))
      .not.toEqual(agentA2AKeys.config("ws-1", "agent-2"));
  });
});
