import { describe, expect, it } from "vitest";
import { hostedSiteKeys } from "./queries";

describe("hosted site query keys", () => {
  it("separates lists from different workspaces", () => {
    const listKey = hostedSiteKeys.list as (
      workspaceId: string,
    ) => readonly unknown[];

    expect(listKey("workspace-a")).not.toEqual(listKey("workspace-b"));
  });
});
