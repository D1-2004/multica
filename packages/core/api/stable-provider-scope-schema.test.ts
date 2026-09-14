import { describe, expect, it } from "vitest";
import { FCE2BStableReleaseSchema } from "./schemas";

describe("stable release provider scope", () => {
  const scope = FCE2BStableReleaseSchema.pick({ provider_scope: true });
  it("preserves an explicit scope and accepts historical responses", () => {
    expect(scope.parse({ provider_scope: "dsh" })).toEqual({ provider_scope: "dsh" });
    expect(scope.parse({})).toEqual({ provider_scope: "" });
  });
  it("rejects malformed scope instead of interpreting it as a shared release", () => {
    for (const provider_scope of [null, ["dsh"], { provider: "dsh" }, 1]) {
      expect(scope.safeParse({ provider_scope }).success).toBe(false);
    }
  });
});
