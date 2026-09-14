import { afterEach, expect, it, vi } from "vitest";
import { ApiClient } from "./client";
import { setSchemaLogger } from "./schema";
import { noopLogger } from "../logger";

const url = "https://33124-sbx-fixture.fc.test/_multica/open#entry=dnge_" + "A".repeat(43);
const valid = { access_id: "00000000-0000-4000-8000-000000000001", entry_url: url, expires_at: new Date(Date.now() + 60000).toISOString() };
afterEach(() => { vi.unstubAllGlobals(); setSchemaLogger(noopLogger); });

it("issues only the named employee and workspace with no browser placement", async () => {
  const request = vi.fn().mockResolvedValue(new Response(JSON.stringify(valid), { status: 201 }));
  vi.stubGlobal("fetch", request);
  expect(await new ApiClient("https://pre.test").issueDSHNativeEntry("ws", "agent/id")).toEqual({ accessId: valid.access_id, entryUrl: url, expiresAt: valid.expires_at });
  expect(request).toHaveBeenCalledWith("https://pre.test/api/agents/agent%2Fid/dsh-native/access", expect.objectContaining({ method: "POST", body: "{}", headers: expect.objectContaining({ "X-Workspace-ID": "ws", "X-Workspace-Slug": "" }) }));
});

it.each([
  {}, { ...valid, access_id: "bad" }, { ...valid, expires_at: "bad" },
  ...["javascript:alert(1)", url.replace("https:", "http:"), url.replace("33124-", "other-"), url.replace("/_multica/open", "/other"), url.replace("#entry=", "?entry="), url.replace("https://", "https://user:password@"), url + "&other=value"].map((entry_url) => ({ ...valid, entry_url })),
])("rejects malformed native entries without logging credentials", async (response) => {
  const warn = vi.fn(); setSchemaLogger({ ...noopLogger, warn });
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify(response), { status: 201 })));
  expect(await new ApiClient("https://pre.test").issueDSHNativeEntry("ws", "agent")).toBeNull();
  expect(JSON.stringify(warn.mock.calls)).not.toContain("dnge_");
  expect(JSON.stringify(warn.mock.calls)).not.toContain("password");
});

it("does not resubmit an ambiguous startup", async () => {
  const request = vi.fn().mockResolvedValue(new Response("unavailable", { status: 503 }));
  vi.stubGlobal("fetch", request);
  await expect(new ApiClient("https://pre.test").issueDSHNativeEntry("ws", "agent")).rejects.toMatchObject({ status: 503 });
  expect(request).toHaveBeenCalledTimes(1);
});
