import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient } from "./client";

afterEach(() => vi.unstubAllGlobals());

describe("FC stable provider queries", () => {
  it("keeps scoped, shared-only and unfiltered queries distinct", async () => {
    const request = vi.fn().mockImplementation(async (url: string) =>
      new Response(JSON.stringify(url.includes("stable-channel")
        ? { current: null, active_release: null, can_publish: false }
        : [])),
    );
    vi.stubGlobal("fetch", request);
    const client = new ApiClient("https://pre.example.test");
    await client.getCloudSandboxStableChannel("aliyun_fc", "dsh");
    await client.listCloudSandboxStableRuntimes("aliyun_fc", "dsh");
    await client.listCloudSandboxStableReleases("aliyun_fc", 20, "dsh");
    await client.listCloudSandboxStableReleases("aliyun_fc", 20, "");
    await client.listCloudSandboxStableReleases("aliyun_fc");
    await client.getCloudSandboxStableChannel("asb");
    const queries = request.mock.calls.map(([url]) => new URL(url).searchParams);
    expect(queries.map((query) => query.get("provider_scope")))
      .toEqual(["dsh", "dsh", "dsh", "", null, null]);
    expect(queries.map((query) => query.get("sandbox_backend")))
      .toEqual(["aliyun_fc", "aliyun_fc", "aliyun_fc", "aliyun_fc", "aliyun_fc", "asb"]);
  });
});
