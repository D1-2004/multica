import { describe, expect, it } from "vitest";
import {
  canCreateFCE2BRuntime,
  canCreatePublicFCE2BRuntime,
} from "./runtime-access";

describe("FC/E2B runtime creation access", () => {
  it.each(["owner", "admin", "member"])(
    "lets %s create a managed runtime",
    (role) => {
      expect(canCreateFCE2BRuntime(role)).toBe(true);
    },
  );

  it.each([undefined, null, "guest"])(
    "does not let %s create a managed runtime",
    (role) => {
      expect(canCreateFCE2BRuntime(role)).toBe(false);
    },
  );

  it("reserves public managed runtimes for workspace admins", () => {
    expect(canCreatePublicFCE2BRuntime("owner")).toBe(true);
    expect(canCreatePublicFCE2BRuntime("admin")).toBe(true);
    expect(canCreatePublicFCE2BRuntime("member")).toBe(false);
  });
});
