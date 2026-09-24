import { describe, expect, it } from "vitest";
import {
  SHARED_DISK_CAPABILITY,
  boundRuntimeImage,
  compareRuntimeImage,
  sharedDiskSupport,
} from "./runtime-image";

describe("sharedDiskSupport", () => {
  it("treats a missing capability list as incapable and missing metadata as unknown", () => {
    expect(sharedDiskSupport(null)).toBe("unknown");
    expect(sharedDiskSupport({ metadata: { kind: "fc-e2b" } })).toBe("incapable");
    expect(sharedDiskSupport({ metadata: { capabilities: ["dws"] } })).toBe("incapable");
    expect(sharedDiskSupport({ metadata: { capabilities: [SHARED_DISK_CAPABILITY] } })).toBe("capable");
  });
});

describe("compareRuntimeImage", () => {
  const required = "multica-m7-va2eb67817f146ef4-r1-fdf8b8";

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
          template_alias: "  image-alias  ",
          template_id: "id-1",
        },
      }),
    ).toBe("image-alias");
    expect(boundRuntimeImage({ metadata: { template_id: "id-only" } })).toBe("id-only");
    expect(boundRuntimeImage({ metadata: { template_name: "Team v2" } })).toBe("Team v2");
    expect(boundRuntimeImage({ metadata: {} })).toBeNull();
    expect(boundRuntimeImage(null)).toBeNull();
  });
});
