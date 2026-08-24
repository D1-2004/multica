import { describe, expect, it } from "vitest";
import { labelListOptions } from "./queries";

describe("labelListOptions", () => {
  it("keeps the lightweight catalog as the default query", () => {
    expect(labelListOptions("workspace-1").queryKey).toEqual([
      "labels",
      "workspace-1",
      "list",
      "issue",
    ]);
  });

  it("isolates the settings-only usage rollup in its own cache key", () => {
    expect(
      labelListOptions("workspace-1", "issue", { includeUsage: true }).queryKey,
    ).toEqual([
      "labels",
      "workspace-1",
      "usage",
      "list",
      "issue",
    ]);
  });
});
