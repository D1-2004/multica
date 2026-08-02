import { describe, expect, it } from "vitest";
import { workspaceAccessKeys } from "./queries";

describe("workspace access query keys", () => {
  it("keeps every grant and token cache under the workspace id", () => {
    expect(workspaceAccessKeys.grants("ws-a")).toEqual([
      "workspaces",
      "ws-a",
      "workspace-access",
      "grants",
    ]);
    expect(workspaceAccessKeys.tokens("ws-b", "grant-1")).toEqual([
      "workspaces",
      "ws-b",
      "workspace-access",
      "grants",
      "grant-1",
      "tokens",
    ]);
  });
});
