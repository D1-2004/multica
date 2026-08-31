import { describe, expect, it } from "vitest";
import type { HostedSite } from "./types";
import { hostedSiteDisplayTitle } from "./display";

const baseSite: HostedSite = {
  siteId: "site-1",
  publicSiteId: "public-1",
  title: "",
  status: "active",
  activeRevisionId: null,
  latestRevisionId: "revision-1",
  latestStatus: "active",
  latestError: "",
  createdAt: "2026-08-31T00:00:00Z",
  updatedAt: "2026-08-31T00:00:00Z",
  siteUrl: "https://sites.example.test/sites/public-1/",
};

describe("hostedSiteDisplayTitle", () => {
  it("prefers the entrypoint HTML title", () => {
    expect(hostedSiteDisplayTitle({ ...baseSite, title: " Weekly Review " })).toBe(
      "Weekly Review",
    );
  });

  it("falls back to the public ID for legacy sites", () => {
    expect(hostedSiteDisplayTitle(baseSite)).toBe("public-1");
  });
});
