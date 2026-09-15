import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient } from "../api/client";

const connection = { id: "connection-1", provider: "alibaba_code", account_login: "alice", created_at: "2026-09-15" };
afterEach(() => vi.unstubAllGlobals());

describe("Git repository API boundary", () => {
  it("lets the server infer the provider from the supplied URL", async () => {
    const fetch = vi.fn().mockResolvedValue(Response.json({ repository_url: "https://code.alibaba-inc.com/team/repo", provider: "alibaba_code", connections: [connection] }));
    vi.stubGlobal("fetch", fetch);
    const result = await new ApiClient("https://multica.example").resolveGitRepository("workspace-1", "git@code.alibaba-inc.com:team/repo.git");
    expect(result.connections).toEqual([connection]);
    expect(fetch.mock.calls[0]?.[0]).toContain("repository=git%40code.alibaba-inc.com%3Ateam%2Frepo.git");
    expect(fetch.mock.calls[0]?.[0]).not.toContain("provider=");
  });
  it("only returns public connection fields", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(Response.json({ ...connection, token: "fixture", token_ciphertext: "fixture" })));
    const result = await new ApiClient("https://multica.example").connectGitRepository("workspace-1", { repository_url: "https://code.alibaba-inc.com/team/repo", token: "code_pat_fixture" });
    expect(result).toEqual(connection);
  });
  it.each([null, {}, { connections: "invalid" }, { connections: [{ id: 1 }] }])("rejects a malformed connection list", async (response) => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(Response.json(response)));
    await expect(new ApiClient("https://multica.example").listGitConnections("workspace-1")).rejects.toThrow("Invalid Git connections response");
  });
});
