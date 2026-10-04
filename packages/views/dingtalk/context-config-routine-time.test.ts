import { describe, expect, it } from "vitest";
import { routineLocalDateTime, routineRunAt } from "./context-config-routine-time";

describe("one-shot routine time", () => {
  it("converts local clock input to an explicit UTC instant without shifting the clock on edit", () => {
    const local = "2027-01-15T14:30";
    const instant = routineRunAt(local);
    expect(instant).toMatch(/Z$/);
    expect(instant).toBe(new Date(2027, 0, 15, 14, 30).toISOString());
    expect(routineLocalDateTime(instant!)).toBe(`${local}:00`);
    expect(routineRunAt(`${local}:24`)).toBe(new Date(2027, 0, 15, 14, 30, 24).toISOString());
  });

  it("rejects invalid, normalized and ambiguous wire formats", () => {
    for (const local of ["", "not-a-date", "2027-02-30T12:00", "2027-01-15T25:00", "2027-01-15T14:30Z"]) {
      expect(routineRunAt(local)).toBeNull();
    }
    expect(routineLocalDateTime("malformed")).toBe("");
  });
});
