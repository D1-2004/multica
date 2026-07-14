import { describe, expect, it } from "vitest";
import {
  dingtalkAccountBindingKeys,
  dingtalkAccountBindingsOptions,
} from "./queries";

describe("DingTalk account binding query options", () => {
  it("uses a workspace-scoped key and refetches on focus without polling", () => {
    const options = dingtalkAccountBindingsOptions("workspace-1");

    expect(dingtalkAccountBindingKeys.list("workspace-1")).toEqual([
      "dingtalk-account-bindings",
      "workspace-1",
      "list",
    ]);
    expect(options.queryKey).toEqual(
      dingtalkAccountBindingKeys.list("workspace-1"),
    );
    expect(options.enabled).toBe(true);
    expect(options.refetchOnWindowFocus).toBe("always");
    expect(options).not.toHaveProperty("refetchInterval");
  });
});
