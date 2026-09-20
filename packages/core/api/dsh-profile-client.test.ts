import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient } from "./client";

afterEach(() => vi.unstubAllGlobals());

describe("employee Profile receipts", () => {
  it("binds retry to an observed attempt and workspace without sending configuration", async () => {
    const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify({ accepted: true }), { status: 202 }));
    vi.stubGlobal("fetch", fetch);
    await new ApiClient("https://pre.example.test").retryDSHProfileBuild("workspace", "agent/id", "9", "attempt-id");
    expect(fetch).toHaveBeenCalledWith("https://pre.example.test/api/agents/agent%2Fid/dsh-profile/retry",
      expect.objectContaining({ method: "POST", body: JSON.stringify({ revision: "9", build_id: "attempt-id" }), headers: expect.objectContaining({ "X-Workspace-ID": "workspace" }) }));
  });

  it.each([
    { state: "failed", id: "5f28f5d4-40db-4f11-83be-4181f4ee0a31", can_retry: true, expected: true },
    { state: "failed", can_retry: true, expected: false },
    { state: "queued", id: "5f28f5d4-40db-4f11-83be-4181f4ee0a31", can_retry: true, expected: false },
    { state: "failed", id: "5f28f5d4-40db-4f11-83be-4181f4ee0a31", expected: false },
  ])("requires a failed attempt identity and cleanup signal for retry", async ({ expected, ...build }) => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      state: "build_failed", current: false, desired_revision: "9", applied_generation: 0,
      builds: [{ package_name: "fixture", version: "1.0.0", ...build }],
    }))));
    const result = await new ApiClient("https://pre.example.test").getDSHProfile("workspace", "agent");
    expect(result?.builds[0]?.canRetry).toBe(expected);
  });
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
    { state: "native_sync_pending", current: true, applied_generation: 1 },
    { state: "future_state", current: false, applied_generation: 0 },
    { state: "build_failed", desired_revision: "9", current: false, applied_generation: 0, builds: [{ package_name: "fixture", version: "1", state: "failed", can_retry: "true" }] },
  ])("does not turn malformed status into applied", async (body) => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify(body))));
    expect(await new ApiClient("https://pre.example.test").getDSHProfile("workspace", "agent")).toBeNull();
  });

  it("allows an unavailable native snapshot before the first saved revision", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      state: "native_sync_pending", current: false, desired_revision: "", applied_revision: "", applied_generation: 0,
    }))));
    expect(await new ApiClient("https://pre.example.test").getDSHProfile("workspace", "agent"))
      .toMatchObject({ current: false, desiredRevision: "", state: "native_sync_pending" });
  });

  it("keeps saved revisions visible when native synchronization is pending", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      state: "native_sync_pending", current: false, desired_revision: "2", applied_revision: "2", applied_generation: 1,
    }))));
    expect(await new ApiClient("https://pre.example.test").getDSHProfile("workspace", "agent"))
      .toMatchObject({ current: false, appliedRevision: "2", state: "native_sync_pending" });
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
