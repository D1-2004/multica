import { afterEach, expect, it, vi } from "vitest";
import { ApiClient } from "./client";

afterEach(() => vi.unstubAllGlobals());

it("uses the configured CSRF cookie without falling back to the main session", async () => {
  const fetch = vi.fn().mockResolvedValue(new Response(null, { status: 204 }));
  vi.stubGlobal("fetch", fetch);
  vi.stubGlobal("document", { cookie: "multica_csrf=production; mf_pre_multica_csrf=preview" });
  const api = new ApiClient("/forward/pre", { csrfCookieName: "mf_pre_multica_csrf" });
  await api.logout();
  expect(fetch).toHaveBeenLastCalledWith("/forward/pre/auth/logout", expect.objectContaining({
    headers: expect.objectContaining({ "X-CSRF-Token": "preview" }),
  }));
  vi.stubGlobal("document", { cookie: "multica_csrf=production" });
  await api.logout();
  expect(fetch.mock.lastCall?.[1].headers).not.toHaveProperty("X-CSRF-Token");
});

it("keeps the default CSRF cookie for existing clients", async () => {
  const fetch = vi.fn().mockResolvedValue(new Response(null, { status: 204 }));
  vi.stubGlobal("fetch", fetch);
  vi.stubGlobal("document", { cookie: "multica_csrf=production; mf_pre_multica_csrf=preview" });
  await new ApiClient("").logout();
  expect(fetch.mock.lastCall?.[1].headers["X-CSRF-Token"]).toBe("production");
});
