import { describe, expect, it } from "vitest";
import { internalConnectorKeys } from "./queries";

describe("internal connector query keys", () => {
  it("keeps the list key the prefix of the available key", () => {
    const list = internalConnectorKeys.list("ws-1");
    expect(list).toEqual(["workspaces", "ws-1", "internal-connectors"]);
    // Views read the member-visible list under this exact key.
    expect(internalConnectorKeys.available("ws-1")).toEqual([
      "workspaces",
      "ws-1",
      "internal-connectors",
      "available",
    ]);
  });

  it("scopes every key by workspace", () => {
    expect(internalConnectorKeys.available("ws-2")).not.toEqual(internalConnectorKeys.available("ws-1"));
  });
});
