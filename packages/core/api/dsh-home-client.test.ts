import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient } from "./client";

afterEach(() => vi.unstubAllGlobals());

describe("DSH Home client", () => {
  it("keeps accepted provisioning pending and sends no placement", async () => {
    const request = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      provisioned: false, state: "creating", step: 2, generation: 0,
    }), { status: 202 }));
    vi.stubGlobal("fetch", request);
    const result = await new ApiClient("https://pre.example.test").ensureDSHHome("agent/id");
    expect(result).toMatchObject({ provisioned: false, state: "creating" });
    expect(request).toHaveBeenCalledWith("https://pre.example.test/api/agents/agent%2Fid/dsh-home",
      expect.objectContaining({ method: "POST", body: "{}" }));
  });

  it.each([200, 202])("does not interpret malformed %s responses as ready or unprovisioned", async (status) => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({ provisioned: true }), { status })));
    const client = new ApiClient("https://pre.example.test");
    expect(await (status === 200 ? client.getDSHHome("agent") : client.ensureDSHHome("agent"))).toBeNull();
  });

  it("preserves a gateway rejection and never automatically resubmits", async () => {
    const request = vi.fn().mockResolvedValue(new Response("<html>unavailable</html>", { status: 503 }));
    vi.stubGlobal("fetch", request);
    await expect(new ApiClient("https://pre.example.test").ensureDSHHome("agent")).rejects.toMatchObject({ status: 503 });
    expect(request).toHaveBeenCalledTimes(1);
  });
});
