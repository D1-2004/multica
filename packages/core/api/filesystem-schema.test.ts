import { describe, expect, it } from "vitest";
import { FilesystemRootsSchema } from "./filesystem-schema";

describe("FilesystemRootsSchema", () => {
  it("parses shared and agent roots", () => {
    const parsed = FilesystemRootsSchema.parse({
      roots: [
        { kind: "shared", provisioned: false, access: "read" },
        { kind: "agent", id: "11111111-1111-1111-1111-111111111111", provisioned: true, access: "write" },
      ],
    });
    expect(parsed.roots).toHaveLength(2);
  });

  it("rejects a malformed payload", () => {
    expect(() => FilesystemRootsSchema.parse({ roots: [{ kind: "issue" }] })).toThrow();
  });
});
