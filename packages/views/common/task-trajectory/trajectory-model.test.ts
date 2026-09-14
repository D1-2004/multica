import { describe, expect, it } from "vitest";
import {
  buildTrajectorySpans,
  parseDSHTrajectory,
  trajectoryEventSummary,
} from "./trajectory-model";

const fixture = [
  { type: "session", version: 0, id: "ses_test", createdAt: 1000, delegationDepth: 0 },
  { type: "turn/start", seq: 0, time: 1010, data: { turn: 1 } },
  { type: "user/message", seq: 1, time: 1020, data: { content: [{ type: "text", text: "hello" }] } },
  { type: "step/start", seq: 2, time: 1030, data: { turn: 1, step: 1 } },
  { type: "assistant/message", seq: 3, time: 1100, data: { turn: 1, step: 1, message: { content: [{ type: "text", text: "done" }] } } },
  { type: "step/end", seq: 4, time: 1110, data: { turn: 1, step: 1 } },
].map((row) => JSON.stringify(row)).join("\n");

describe("DSH trajectory model", () => {
  it("parses the native JSONL ledger and keeps contiguous event identity", () => {
    const doc = parseDSHTrajectory(fixture);
    expect(doc.header.id).toBe("ses_test");
    expect(doc.events).toHaveLength(5);
    expect(trajectoryEventSummary(doc.events[1]!)).toBe("hello");
    expect(trajectoryEventSummary(doc.events[3]!)).toBe("done");
  });

  it("derives a real-time step span for the overview", () => {
    const spans = buildTrajectorySpans(parseDSHTrajectory(fixture).events);
    expect(spans).toEqual(expect.arrayContaining([
      expect.objectContaining({ startSeq: 2, endSeq: 4, startTime: 1030, endTime: 1110 }),
    ]));
  });

  it("rejects a sequence gap", () => {
    expect(() => parseDSHTrajectory(fixture.replace('"seq":1', '"seq":9'))).toThrow(
      "invalid DSH trajectory event",
    );
  });

  it("rejects a non-native header version or missing delegation depth", () => {
    expect(() => parseDSHTrajectory(fixture.replace('"version":0', '"version":1'))).toThrow(
      "invalid DSH trajectory header",
    );
    expect(() =>
      parseDSHTrajectory(fixture.replace(',"delegationDepth":0', "")),
    ).toThrow("invalid DSH trajectory header");
    expect(() =>
      parseDSHTrajectory(
        fixture.replace(
          '"delegationDepth":0',
          '"parentSession":"ses_parent","origin":"subagent","delegationDepth":1',
        ),
      ),
    ).toThrow("invalid DSH trajectory header");
  });
});

describe("native v3 root trajectory", () => {
  const native = fixture.replace('"version":0', '"version":3,"isSeeded":false');
  it("retains the actual format version and all native event identities", () => {
    const result = parseDSHTrajectory(native);
    expect(result.header.version).toBe(3);
    expect(result.header.isSeeded).toBe(false);
    expect(result.events).toEqual(parseDSHTrajectory(fixture).events);
    expect(parseDSHTrajectory(native.replace(',"delegationDepth":0', '')).header.delegationDepth).toBeUndefined();
  });
  it.each([
    native.replace('"isSeeded":false,', ''),
    native.replace('"isSeeded":false', '"isSeeded":null'),
    native.replace('"isSeeded":false', '"isSeeded":true'),
    native.replace('"delegationDepth":0', '"delegationDepth":null'),
    native.replace('"version":3', '"version":4'),
    native.replace('"seq":0', '"seq":100'),
    native.replace('"time":1010', '"time":-1'),
  ])("rejects unsupported or incomplete ledgers", (invalid) => {
    expect(() => parseDSHTrajectory(invalid)).toThrow();
  });
});
