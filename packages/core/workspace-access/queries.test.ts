import { describe, expect, it } from "vitest";
import { workspaceAccessKeys } from "./queries";

describe("workspace access query keys", () => {
  it("keeps every token cache under the workspace id", () => {
    expect(workspaceAccessKeys.tokens("ws-b")).toEqual([
      "workspaces",
      "ws-b",
      "workspace-access",
      "tokens",
    ]);
  });
});
