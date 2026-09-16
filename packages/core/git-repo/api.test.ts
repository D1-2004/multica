import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient } from "../api/client";

const connection = { id: "connection-1", provider: "github", account_login: "alice", created_at: "2026-09-15" };
afterEach(() => vi.unstubAllGlobals());

describe("Git repository API boundary", () => {
  it("lets the server infer the provider from the supplied URL", async () => {
    const fetch = vi.fn().mockResolvedValue(Response.json({ repository_url: "https://github.com/team/repo", provider: "github", connections: [connection] }));
    vi.stubGlobal("fetch", fetch);
    const result = await new ApiClient("https://multica.example").resolveGitRepository("workspace-1", "git@github.com:team/repo.git");
    expect(result.connections).toEqual([connection]);
    expect(fetch.mock.calls[0]?.[0]).toContain("repository=git%40github.com%3Ateam%2Frepo.git");
    expect(fetch.mock.calls[0]?.[0]).not.toContain("provider=");
  });
  it("only returns public connection fields", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(Response.json({ connections: [{ ...connection, token: "fixture", token_ciphertext: "fixture" }] })));
    const result = await new ApiClient("https://multica.example").listGitConnections("workspace-1");
    expect(result).toEqual({ connections: [connection] });
  });
  it.each([null, {}, { connections: "invalid" }, { connections: [{ id: 1 }] }])("rejects a malformed connection list", async (response) => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(Response.json(response)));
    await expect(new ApiClient("https://multica.example").listGitConnections("workspace-1")).rejects.toThrow("Invalid Git connections response");
  });
});
