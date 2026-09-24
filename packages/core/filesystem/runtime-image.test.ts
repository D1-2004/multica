import { describe, expect, it } from "vitest";
import {
  SHARED_DISK_REQUIRED_RUNTIME_IMAGE,
  boundRuntimeImage,
  compareRuntimeImage,
} from "./runtime-image";

describe("compareRuntimeImage", () => {
  const required = SHARED_DISK_REQUIRED_RUNTIME_IMAGE;

  it("treats a different or older image as a mismatch", () => {
    expect(
      compareRuntimeImage(required, "multica-m7-va2eb67817f146ef4-r1-6ccf66"),
    ).toBe("mismatch");
    expect(compareRuntimeImage(required, "some-other-image")).toBe("mismatch");
  });

  it("treats a missing bound image as unknown", () => {
    expect(compareRuntimeImage(required, null)).toBe("unknown");
    expect(compareRuntimeImage(required, "  ")).toBe("unknown");
    expect(compareRuntimeImage(required, undefined)).toBe("unknown");
  });

  it("accepts the required image and a name that carries it as a whole token", () => {
    expect(compareRuntimeImage(required, required)).toBe("match");
    expect(compareRuntimeImage("r1-fdf8b8", required)).toBe("match");
    expect(compareRuntimeImage("r1-fdf8b8", `registry/${"r1-fdf8b8"}`)).toBe("match");
    expect(compareRuntimeImage(required, "8")).toBe("mismatch");
    expect(compareRuntimeImage(required, "fdf8b8")).toBe("mismatch");
    expect(compareRuntimeImage(required, "r1-fdf8b8")).toBe("mismatch");
  });

  it("does not compare when the capability names no image", () => {
    expect(compareRuntimeImage(null, "r1-6ccf66")).toBe("not_required");
    expect(compareRuntimeImage("", null)).toBe("not_required");
  });
});

describe("boundRuntimeImage", () => {
  it("reads the runtime image from template metadata", () => {
    expect(
      boundRuntimeImage({
        metadata: {
          template_name: "Team v2",
          template_alias: `  ${SHARED_DISK_REQUIRED_RUNTIME_IMAGE}  `,
          template_id: "id-1",
        },
      }),
    ).toBe(SHARED_DISK_REQUIRED_RUNTIME_IMAGE);
    expect(boundRuntimeImage({ metadata: { template_id: "id-only" } })).toBe("id-only");
    expect(boundRuntimeImage({ metadata: { template_name: "Team v2" } })).toBe("Team v2");
    expect(boundRuntimeImage({ metadata: {} })).toBeNull();
    expect(boundRuntimeImage(null)).toBeNull();
  });
});
