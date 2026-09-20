import { describe, expect, it } from "vitest";
import { FilesystemEntriesSchema, FilesystemRootsSchema } from "./filesystem-schema";

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

describe("FilesystemEntriesSchema", () => {
  it("parses a directory page", () => {
    const parsed = FilesystemEntriesSchema.parse({
      root: "shared",
      path: ".",
      offset: 0,
      limit: 200,
      entries: [{ name: "notes.md", path: "notes.md", is_dir: false, size_bytes: 12 }],
      count: 1,
      truncated: false,
      next_offset: null,
    });
    expect(parsed.entries[0]?.name).toBe("notes.md");
  });

  it("falls through malformed entries at the parseWithFallback boundary", () => {
    expect(() => FilesystemEntriesSchema.parse({ entries: "nope" })).toThrow();
  });
});

