import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient } from "./client";
import { GitHubAgentPreviewSchema } from "./schemas";

afterEach(() => vi.unstubAllGlobals());

describe("Agent source export boundary", () => {
  it("downloads a source archive", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("PK source archive", { headers: { "Content-Type": "application/zip" } })));
    const result = await new ApiClient("https://api.example.test").exportAgent("agent-1");
    expect(result.size).toBeGreaterThan(0);
  });
  it.each(["application/json", "text/html", ""]) ("rejects a malformed export response with type %s", async (contentType) => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("{}", { headers: { "Content-Type": contentType } })));
    await expect(new ApiClient("https://api.example.test").exportAgent("agent-1")).rejects.toThrow("Invalid agent export response");
  });
  it("preserves disabled skills and rejects malformed enabled fields in previews", () => {
    const preview = { installation_id: "i", repository: "acme/agent", ref: "main", resolved_sha: "sha", name: "agent", description: "", instructions: "", skills: [{ source_path: "skills/one", name: "one", enabled: false }] };
    expect(GitHubAgentPreviewSchema.parse(preview).skills[0]?.enabled).toBe(false);
    expect(GitHubAgentPreviewSchema.safeParse({ ...preview, skills: [{ ...preview.skills[0], enabled: "false" }] }).success).toBe(false);
  });
});
