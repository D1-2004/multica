import { describe, expect, it } from "vitest";
import { DSHHomeSchema } from "./dsh-home-schema";

describe("DSH Home response", () => {
  it("parses a prepared Home whose sandbox has not started", () => {
    expect(DSHHomeSchema.parse({ provisioned: true, state: "offline", step: 6, generation: 0 })).toEqual({
      provisioned: true, state: "offline", step: 6, generation: 0, sandboxId: "",
    });
  });

  it.each([
    {},
    { provisioned: "true", state: "running", step: 6, generation: 1 },
    { provisioned: true, state: "planned", step: 2, generation: 0 },
    { provisioned: true, state: "future-state", step: 6, generation: 1 },
    { provisioned: false, state: "creating", step: 7, generation: 0 },
  ])("rejects malformed or misleading readiness: %o", (response) => {
    expect(DSHHomeSchema.safeParse(response).success).toBe(false);
  });
});
