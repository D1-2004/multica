import { useEffect } from "react";
import { render, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { api } from "@multica/core/api";
import { WebProviders } from "./web-providers";

vi.mock("@/platform/navigation", () => ({ WebNavigationProvider: ({ children }: { children: React.ReactNode }) => children }));
vi.mock("@/platform/scroll-restoration", () => ({ WebScrollRestorationProvider: ({ children }: { children: React.ReactNode }) => children }));

afterEach(() => {
  vi.unstubAllGlobals();
  window.history.replaceState({}, "", "/");
  localStorage.clear();
});

it.each([200, 401])("initializes forwarded requests without touching the main session (auth status %s)", async (status) => {
  window.history.replaceState({}, "", "/forward/pre/dingtalk/configure");
  localStorage.setItem("multica_token", "production-token");
  document.cookie = "multica_logged_in=1; Path=/";
  document.cookie = "multica_csrf=production-csrf; Path=/";
  document.cookie = "mf_pre_multica_csrf=preview-csrf; Path=/forward/pre/";
  const fetch = vi.fn(async (url: string) => {
    if (url.endsWith("/auth/logout")) return new Response(null, { status: 204 });
    if (url.endsWith("/api/config")) return new Response(JSON.stringify({ allow_signup: false }));
    return new Response(JSON.stringify(status === 200 ? { id: "preview-user" } : { error: "unauthorized" }), { status });
  });
  vi.stubGlobal("fetch", fetch);
  function Child() {
    useEffect(() => { void api.logout(); }, []);
    return null;
  }
  render(<WebProviders locale="en" resources={{ en: {} }} apiBaseUrl="https://pre.example"><Child /></WebProviders>);
  await waitFor(() => expect(fetch).toHaveBeenCalledWith("/forward/pre/auth/logout", expect.anything()));
  await waitFor(() => expect(fetch).toHaveBeenCalledWith("/forward/pre/api/me", expect.anything()));
  expect(fetch.mock.calls.map(([url]) => url).sort()).toEqual([
    "/forward/pre/api/config", "/forward/pre/api/me", "/forward/pre/auth/logout",
  ]);
  for (const [, init] of fetch.mock.calls as unknown as [string, RequestInit][]) {
    expect(init.headers).not.toHaveProperty("Authorization");
    expect(init.headers).toMatchObject({ "X-CSRF-Token": "preview-csrf" });
  }
  expect(localStorage.getItem("multica_token")).toBe("production-token");
  expect(document.cookie).toContain("multica_logged_in=1");
});
