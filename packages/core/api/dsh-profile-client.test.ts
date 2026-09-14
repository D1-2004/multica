import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient } from "./client";

afterEach(() => vi.unstubAllGlobals());

describe("employee Profile receipts", () => {
  it("keeps preparation pending and sends only workspace identity", async () => {
    const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      state: "pending_host", desired_revision: "9223372036854775807", applied_generation: 0, current: false,
    }), { status: 202 }));
    vi.stubGlobal("fetch", fetch);
    const status = await new ApiClient("https://pre.example.test").prepareDSHProfile("workspace", "agent/id");
    expect(status).toMatchObject({ state: "pending_host", desiredRevision: "9223372036854775807", current: false });
    expect(fetch).toHaveBeenCalledWith("https://pre.example.test/api/agents/agent%2Fid/dsh-profile",
      expect.objectContaining({ method: "POST", body: "{}", headers: expect.objectContaining({ "X-Workspace-ID": "workspace" }) }));
  });

  it.each([
    {},
    { state: "applied", current: true },
    { state: "applied", current: true, desired_revision: "2", applied_revision: "1", applied_generation: 1, applied_sandbox_id: "sandbox" },
    { state: "applied", current: true, desired_revision: "2", applied_revision: "2", applied_generation: 0, applied_sandbox_id: "sandbox" },
    { state: "pending_host", current: true, desired_revision: "2", applied_generation: 1 },
    { state: "future_state", current: false, applied_generation: 0 },
  ])("does not turn malformed status into applied", async (body) => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify(body))));
    expect(await new ApiClient("https://pre.example.test").getDSHProfile("workspace", "agent")).toBeNull();
  });

  it("distinguishes the last applied version from edited settings", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      state: "configuration_changed", current: false, desired_revision: "2", applied_revision: "2", applied_generation: 1, applied_sandbox_id: "sandbox",
    }))));
    expect(await new ApiClient("https://pre.example.test").getDSHProfile("workspace", "agent")).toMatchObject({ current: false, appliedRevision: "2", state: "configuration_changed" });
  });
  it("reports a failed dependency without claiming Host application", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      state: "build_failed", desired_revision: "7", current: false, applied_generation: 0,
      builds: [{ package_name: "fixture", version: "1.0.0", state: "failed" }],
    }))));
    expect(await new ApiClient("https://pre.example.test").getDSHProfile("workspace", "agent"))
      .toMatchObject({ state: "build_failed", current: false, builds: [{ packageName: "fixture", version: "1.0.0", state: "failed" }] });
  });

  it("rejects an applied receipt with a failed dependency", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      state: "applied", desired_revision: "7", applied_revision: "7", current: true, applied_generation: 1, applied_sandbox_id: "sandbox",
      builds: [{ package_name: "fixture", version: "1.0.0", state: "failed" }],
    }))));
    expect(await new ApiClient("https://pre.example.test").getDSHProfile("workspace", "agent")).toBeNull();
  });

});
