import { describe, expect, it } from "vitest";
import {
  WorkspaceMCPConnectionSchema,
  WorkspaceMCPLinkSchema,
} from "./workspace-mcp-schema";

describe("workspace MCP response boundaries", () => {
  it("keeps safe metadata and tolerates optional field drift without retaining secrets", () => {
    const parsed = WorkspaceMCPConnectionSchema.parse({
      id: "id",
      name: "client",
      expires_at: "date",
      scopes: 42,
      token: "secret",
    });
    expect(parsed).toEqual({
      id: "id",
      name: "client",
      expiresAt: "date",
      scopes: [],
      revokedAt: null,
      lastUsedAt: null,
    });
  });
  it("rejects a missing or malformed one-time URL", () => {
    expect(
      WorkspaceMCPLinkSchema.safeParse({ id: "id", url: 42 }).success,
    ).toBe(false);
    expect(WorkspaceMCPLinkSchema.safeParse({ id: "id" }).success).toBe(false);
  });
});
