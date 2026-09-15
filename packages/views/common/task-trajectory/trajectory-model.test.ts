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

describe("task child trajectory bundle", () => {
  const rows = [
    { type: "multica/task-trajectory", version: 2, sessionId: "root", requestId: "task", firstSeq: 40, lastSeq: 43,
      children: [{ sessionId: "child", parentSessionId: "root", activationId: "activation", firstSeq: 20, lastSeq: 22, closed: true }] },
    { type: "session", version: 3, id: "root", createdAt: 1, isSeeded: false },
    { type: "turn/start", seq: 40, time: 2, data: { turn: 1 } },
    { type: "user/message", seq: 41, time: 3, data: { source: { kind: "user", rpcId: "task" } } },
    { type: "multica/task-child", seq: 42, time: 4, data: { requestId: "task", childSessionId: "child", activationId: "activation", firstSeq: 20 } },
    { type: "turn/end", seq: 43, time: 8, data: { turn: 1, reason: { kind: "completed" } } },
    { type: "session", version: 3, id: "child", createdAt: 2, isSeeded: true, parentSession: "root", origin: "subagent", delegationDepth: 1 },
    { type: "multica/task-child-start", seq: 20, time: 4, data: { requestId: "task", parentSessionId: "root", activationId: "activation" } },
    { type: "tool/result", seq: 21, time: 5, data: { content: "CHILD_FILE_READ_OK" } },
    { type: "multica/task-child-end", seq: 22, time: 6, data: { requestId: "task", parentSessionId: "root", activationId: "activation", firstSeq: 20 } },
  ];
  const encode = (value: unknown[]) => value.map((row) => JSON.stringify(row)).join("\n");
  it("preserves root and child session identities and native tool output", () => {
    const doc = parseDSHTrajectory(encode(rows));
    expect(doc.events.map((event) => event.seq)).toEqual([40, 41, 42, 43]);
    expect(doc.children?.[0]?.header.id).toBe("child");
    expect(doc.children?.[0]?.events.map((event) => event.seq)).toEqual([20, 21, 22]);
    expect(trajectoryEventSummary(doc.children![0]!.events[1]!)).toBe("CHILD_FILE_READ_OK");
  });
  it("rejects cross-task children, missing content, and forged lineage", () => {
    const raw = encode(rows);
    for (const invalid of [
      encode(rows.slice(0, -1)),
      raw.replace('"parentSession":"root"', '"parentSession":"other"'),
      raw.replace('"requestId":"task","parentSessionId"', '"requestId":"other","parentSessionId"'),
      raw.replace('"seq":21', '"seq":19'),
      raw.replace('"closed":true', '"closed":false'),
      raw + "\n" + JSON.stringify(rows[6]),
    ]) expect(() => parseDSHTrajectory(invalid)).toThrow();
  });
  it("shows an explicitly interrupted child without inventing completion", () => {
    const raw = encode(rows.slice(0, -1)).replace('"lastSeq":22,"closed":true', '"lastSeq":21,"closed":false');
    expect(parseDSHTrajectory(raw).children?.[0]?.closed).toBe(false);
  });
});

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

describe("task-scoped native trajectory", () => {
  const rows = [
    { type: "multica/task-trajectory", version: 1, sessionId: "session", requestId: "mine", firstSeq: 40, lastSeq: 44 },
    { type: "session", version: 3, id: "session", createdAt: 1, isSeeded: false },
    { type: "turn/start", seq: 40, time: 2, data: { turn: 9 } },
    { type: "user/message", seq: 41, time: 3, data: { source: { kind: "plugin" }, content: "current context" } },
    { type: "user/message", seq: 42, time: 4, data: { source: { kind: "user", rpcId: "mine" }, content: "current prompt" } },
    { type: "assistant/message", seq: 43, time: 5, data: { message: { content: [] } } },
    { type: "turn/end", seq: 44, time: 6, data: { turn: 9, reason: { kind: "completed" } } },
  ];
  const scoped = rows.map(row => JSON.stringify(row)).join("\n");
  it("keeps original sequence numbers and the complete task turn", () => {
    const doc = parseDSHTrajectory(scoped);
    expect(doc.scope).toEqual({ sessionId: "session", requestId: "mine", firstSeq: 40, lastSeq: 44 });
    expect(doc.events.map(event => event.seq)).toEqual([40, 41, 42, 43, 44]);
    expect(trajectoryEventSummary(doc.events[1]!)).toBe("current context");
  });
  it.each([
    scoped.replace('"rpcId":"mine"', '"rpcId":"other"'),
    scoped.replace('"kind":"plugin"', '"kind":"user","rpcId":"other"'),
    scoped.replace('"seq":41', '"seq":99'),
    scoped.replace('"firstSeq":40', '"firstSeq":null'),
    scoped.replace('"id":"session"', '"id":"other"'),
    scoped.replace('"type":"turn/end"', '"type":"assistant/message"'),
    scoped.replace('"turn":9,"reason"', '"turn":8,"reason"'),
  ])("rejects a mismatched, mixed or incomplete export", invalid => {
    expect(() => parseDSHTrajectory(invalid)).toThrow();
  });
});
