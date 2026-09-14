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
});
