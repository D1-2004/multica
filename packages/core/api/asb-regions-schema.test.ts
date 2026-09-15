import { describe, expect, it } from "vitest";
import { ASBRegionsSchema, EMPTY_ASB_REGIONS } from "./asb-regions-schema";
import { parseWithFallback } from "./schema";

describe("ASB region discovery boundary", () => {
  it("uses API values including new regions, with stable deduplication", () => {
    expect(
      ASBRegionsSchema.parse({
        regions: ["cn-new-region", "cn-hangzhou", "cn-hangzhou"],
      }),
    ).toEqual({ available: true, regions: ["cn-hangzhou", "cn-new-region"] });
  });
  it.each([
    {},
    { regions: null },
    { regions: "cn-hangzhou" },
    { regions: [1] },
    { regions: ["cn-hangzhou.evil.example"] },
  ])("keeps malformed discovery unavailable", (raw) => {
    expect(
      parseWithFallback(raw, ASBRegionsSchema, EMPTY_ASB_REGIONS, {
        endpoint: "asb-regions",
      }),
    ).toEqual(EMPTY_ASB_REGIONS);
  });
});
