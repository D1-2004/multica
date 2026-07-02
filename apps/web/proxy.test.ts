import { describe, expect, it } from "vitest";
import { NextRequest } from "next/server";
import { proxy } from "./proxy";

function makeRequest(pathname: string, cookie?: string) {
  return new NextRequest(new URL(pathname, "https://app.test"), {
    headers: cookie ? { cookie } : undefined,
  });
}

describe("web proxy", () => {
  it("redirects logged-out root visitors to login before rendering the page", () => {
    const response = proxy(makeRequest("/"));

    expect(response.status).toBe(307);
    expect(response.headers.get("location")).toBe("https://app.test/login");
  });

  it("keeps the existing root shortcut for logged-in visitors with a last workspace", () => {
    const response = proxy(
      makeRequest("/", "multica_logged_in=1; last_workspace_slug=acme"),
    );

    expect(response.status).toBe(307);
    expect(response.headers.get("location")).toBe("https://app.test/acme/issues");
  });
});
