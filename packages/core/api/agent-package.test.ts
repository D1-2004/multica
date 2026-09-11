import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient } from "./client";

afterEach(() => vi.unstubAllGlobals());

describe("Agent package upload", () => {
  const preview = {
    preview_id: "preview-1", expires_at: "2026-09-08T12:00:00Z", package_hash: "hash",
    manifest_version: "multica.agent/v2", name: "Package inspector", instructions: "Inspect",
    skills: [{ name: "review", source_path: "skills/review", enabled: false }],
    requirements: { secrets: ["token"], deferred_bindings: ["/bindings/runner"] },
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
  });

  it.each([{ ...preview, preview_id: "" }, { ...preview, requirements: null }, { ...preview, skills: [{ name: "bad", enabled: "false" }] }])("fails closed on malformed previews", async (response) => {
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
    const request = { preview_id: "preview-1", runtime_id: "runtime-1", name: "Inspector" };
    const result = await new ApiClient("https://api.example.test").createAgentFromPackage("workspace", request);
    expect(result.agent.id).toBe("agent-1");
    expect(fetch).toHaveBeenCalledWith("https://api.example.test/api/workspaces/workspace/agent-packages", expect.objectContaining({ method: "POST", body: JSON.stringify(request) }));
  });

  it("prepares a Builder package without discarding manifest or file fields", async () => {
    const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify(preview)));
    vi.stubGlobal("fetch", fetch);
    const content = '{"manifest":{"version":"multica.agent/v2","configuration":{"persona":"Review"}},"files":{"AGENTS.md":"All instructions"}}';
    await new ApiClient("https://api.example.test").prepareAgentPackage("workspace", content);
    expect(fetch).toHaveBeenCalledWith("https://api.example.test/api/workspaces/workspace/agent-packages/prepare", expect.objectContaining({ method: "POST", body: content }));
  });

  it("requires a usable preview for ZIP publication", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({ preview_id: "", resolved_sha: "hash" }))));
    await expect(new ApiClient("https://api.example.test").previewAgentPackagePublication("agent", new Blob(["zip"]))).rejects.toThrow("Invalid Agent package publication preview");
  });

  it("rejects malformed Builder previews and non-ZIP downloads", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({ ...preview, requirements: null }))));
    await expect(new ApiClient("https://api.example.test").prepareAgentPackage("workspace", "{}")).rejects.toThrow("Invalid Agent package preview response");
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("<html>Sign in</html>", { headers: { "Content-Type": "text/html" } })));
    await expect(new ApiClient("https://api.example.test").downloadPreparedAgentPackage("workspace", "preview-1")).rejects.toThrow("Invalid Agent package download response");
  });
});

describe("Agent package binding confirmation", () => {
  const report = { revision: "revision-1", bindings: [{ path: "/bindings/github_identity", status: "pending", declaration: { ref: "author" }, current: { ref: "github-current" }, current_fingerprint: "fingerprint", config_tab: "general", message: "" }], resources: [{ ref: "github-current", kind: "github-identity", label: "Author" }] };

  it("retains current resource evidence and posts an explicit mapping", async () => {
    const fetch = vi.fn().mockImplementation(() => Promise.resolve(new Response(JSON.stringify(report))));
    vi.stubGlobal("fetch", fetch);
    const api = new ApiClient("https://api.example.test");
    expect(await api.getAgentPackageBindings("agent-1")).toEqual(report);
    const request = { path: "/bindings/github_identity", revision: report.revision, current_fingerprint: "fingerprint", mappings: { author: "github-current" } };
    await api.confirmAgentPackageBinding("agent-1", request);
    expect(fetch).toHaveBeenLastCalledWith("https://api.example.test/api/agents/agent-1/package-bindings/confirm", expect.objectContaining({ method: "POST", body: JSON.stringify(request) }));
  });

  it.each([{}, { ...report, revision: "" }, { ...report, bindings: [{ ...report.bindings[0], current_fingerprint: 7 }] }, { ...report, bindings: [{ ...report.bindings[0], declaration: undefined }] }])("rejects malformed binding evidence for both operations", async (response) => {
    vi.stubGlobal("fetch", vi.fn().mockImplementation(() => Promise.resolve(new Response(JSON.stringify(response)))));
    const api = new ApiClient("https://api.example.test");
    await expect(api.getAgentPackageBindings("agent-1")).rejects.toThrow("Invalid Agent package binding response");
    await expect(api.confirmAgentPackageBinding("agent-1", { path: "", revision: "", current_fingerprint: "", mappings: {} })).rejects.toThrow("Invalid Agent package binding response");
  });
});
