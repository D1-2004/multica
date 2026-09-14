import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient } from "./client";

afterEach(() => vi.unstubAllGlobals());

describe("Agent package upload", () => {
  const preview = {
    preview_id: "preview-1", expires_at: "2026-09-08T12:00:00Z", package_hash: "hash",
    manifest_version: "multica.agent/v2", name: "Package inspector", instructions: "Inspect",
    skills: [{ name: "review", source_path: "skills/review", enabled: false }],
    requirements: { secrets: ["token"], deferred_bindings: ["/bindings/runner"], dsh_plugins: [{ ref: "plugin-lens", package_name: "dsh-mcp-lens", version: "1.0.0", integrity: "sha256-" + "a".repeat(64) }] },
  };

  it("uploads ZIP bytes and retains the server-owned preview and requirements", async () => {
    const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify(preview)));
    vi.stubGlobal("fetch", fetch);
    const file = new Blob(["PK example"], { type: "application/zip" });
    const result = await new ApiClient("https://api.example.test").previewAgentPackage("workspace", file);
    expect(fetch).toHaveBeenCalledWith("https://api.example.test/api/workspaces/workspace/agent-packages/preview", expect.objectContaining({ method: "POST", body: file, headers: expect.objectContaining({ "Content-Type": "application/zip" }) }));
    expect(result.preview_id).toBe("preview-1");
    expect(result.skills[0]?.enabled).toBe(false);
    expect(result.requirements.secrets).toEqual(["token"]);
    expect(result.requirements.dshPlugins).toEqual([{ ref: "plugin-lens", packageName: "dsh-mcp-lens", version: "1.0.0", integrity: "sha256-" + "a".repeat(64) }]);
  });

  it.each([{ ...preview, preview_id: "" }, { ...preview, requirements: null }, { ...preview, requirements: { ...preview.requirements, dsh_plugins: [{ ref: "" }] } }, { ...preview, requirements: { ...preview.requirements, dsh_plugins: [{ ref: "plugin", version: 123 }] } }, { ...preview, skills: [{ name: "bad", enabled: "false" }] }])("fails closed on malformed previews", async (response) => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify(response))));
    await expect(new ApiClient("https://api.example.test").previewAgentPackage("workspace", new Blob(["zip"]))).rejects.toThrow("Invalid Agent package preview response");
  });

  it("does not send an empty file", async () => {
    const fetch = vi.fn(); vi.stubGlobal("fetch", fetch);
    await expect(new ApiClient("https://api.example.test").previewAgentPackage("workspace", new Blob([]))).rejects.toThrow("40 MiB");
    expect(fetch).not.toHaveBeenCalled();
  });

  it("confirms either source through the same package endpoint", async () => {
    const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify({ agent: { id: "agent-1" }, source: { agent_id: "agent-1", source_type: "local", repository: "", ref: "", synced_commit_sha: "hash", sync_status: "ready" }, warnings: [] })));
    vi.stubGlobal("fetch", fetch);
    const request = { preview_id: "preview-1", runtime_id: "runtime-1", name: "Inspector", dsh_plugin_bindings: { "plugin-lens": "destination-plugin" }, secrets: { token: "new-fixture-token" } };
    const result = await new ApiClient("https://api.example.test").createAgentFromPackage("workspace", request);
    expect(result.agent.id).toBe("agent-1");
    expect(fetch).toHaveBeenCalledWith("https://api.example.test/api/workspaces/workspace/agent-packages", expect.objectContaining({ method: "POST", body: JSON.stringify(request) }));
  });
});
