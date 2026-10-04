import { describe, expect, it } from "vitest";
import { parseWithFallback } from "./schema";
import type { ContextRoutine, ContextRoutineRun, ContextRoutineWriteResult } from "../types/context-capability";
import {
  ContextConfigSceneDetailSchema,
  ContextRoutineEnvelopeSchema,
  ContextRoutineRunEnvelopeSchema,
  ContextRoutineRunsListSchema,
  ContextRoutineWriteSchema,
  ContextRoutinesListSchema,
} from "./context-capability-schema";

const opts = { endpoint: "test", includeReceived: false };
const sceneId = "c7c7c7c7-0000-4000-8000-000000000001";

const wireRoutine = {
  id: "11111111-1111-4111-8111-111111111111",
  scene_id: sceneId,
  scene_kind: "group",
  autopilot_id: "22222222-2222-4222-8222-222222222222",
  title: "Daily standup",
  instructions: "Remind the group.",
  enabled: true,
  trigger: {
    id: "33333333-3333-4333-8333-333333333333",
    kind: "schedule",
    cron: "0 9 * * 1-5",
    timezone: "Asia/Shanghai",
    next_run_at: "2026-10-02T01:00:00Z",
    next_runs: ["2026-10-02T01:00:00Z", "2026-10-05T01:00:00Z"],
  },
  last_run: {
    id: "44444444-4444-4444-8444-444444444444",
    status: "completed",
    source: "schedule",
    created_at: "2026-10-01T01:00:00Z",
    completed_at: "2026-10-01T01:02:00Z",
  },
  created_by_type: "member",
  created_at: "2026-10-01T00:00:00Z",
  updated_at: "2026-10-01T00:00:00Z",
};

describe("scene routine schemas", () => {
  it("maps a routine and fills omitted strings and lists", () => {
    const [routine] = parseWithFallback<ContextRoutine[]>(
      { routines: [wireRoutine] },
      ContextRoutinesListSchema,
      [],
      opts,
    );
    expect(routine).toMatchObject({
      sceneId,
      sceneKind: "group",
      title: "Daily standup",
      enabled: true,
      pauseReason: "",
      trigger: { kind: "schedule", timezone: "Asia/Shanghai", webhookUrl: "", webhookUrlMasked: "", nextRuns: [expect.any(String), expect.any(String)] },
      lastRun: { status: "completed", failureReason: "", completedAt: "2026-10-01T01:02:00Z" },
    });
  });

  it("retains a one-shot instant, scene identity and consumption state without a cron", () => {
    const [routine] = ContextRoutinesListSchema.parse({
      routines: [{ ...wireRoutine, trigger: { kind: "once", run_at: "2026-10-04T14:33:00+08:00", consumed: true } }],
    });
    expect(routine).toMatchObject({
      sceneId,
      trigger: { kind: "once", runAt: "2026-10-04T14:33:00+08:00", consumed: true, cron: "", nextRunAt: null, nextRuns: [] },
    });
  });

  it("tolerates omitted or malformed one-shot fields on older responses", () => {
    const parse = (trigger: object) => ContextRoutineEnvelopeSchema.parse({ routine: { ...wireRoutine, trigger } });
    expect(parse(wireRoutine.trigger)?.trigger).toMatchObject({ runAt: null, consumed: false });
    expect(parse({ kind: "once", run_at: 7, consumed: "true" })?.trigger).toMatchObject({ runAt: null, consumed: false });
    expect(parse({ kind: "once", run_at: "not-a-date" })?.trigger.runAt).toBeNull();
  });

  it("is enabled only for a literal true and tolerates a malformed last run", () => {
    const [routine] = ContextRoutinesListSchema.parse({
      routines: [{ ...wireRoutine, enabled: "true", last_run: { status: 3 } }],
    });
    expect(routine?.enabled).toBe(false);
    expect(routine?.lastRun).toBeNull();
  });

  it("drops a malformed entry instead of the whole list, and falls back on a malformed body", () => {
    const list = ContextRoutinesListSchema.parse({ routines: [wireRoutine, { id: 7 }, null] });
    expect(list.map((r) => r.id)).toEqual([wireRoutine.id]);
    expect(ContextRoutinesListSchema.parse({ routines: null })).toEqual([]);
    expect(parseWithFallback<ContextRoutine[]>("oops", ContextRoutinesListSchema, [], opts)).toEqual([]);
  });

  it("reads a write result, the full webhook URL once, and null for a malformed echo", () => {
    const webhook = {
      ...wireRoutine,
      trigger: { id: "t", kind: "webhook", webhook_url: "https://x/api/webhooks/autopilots/awt_secret", webhook_url_masked: "https://x/api/webhooks/autopilots/awt_…cret" },
    };
    const result = parseWithFallback<ContextRoutineWriteResult | null>(
      { routine: webhook, updated: true, tell_the_human: "…" },
      ContextRoutineWriteSchema,
      null,
      opts,
    );
    expect(result?.updated).toBe(true);
    expect(result?.routine.trigger.webhookUrl).toContain("awt_secret");
    expect(ContextRoutineWriteSchema.parse({ routine: { id: 1 } })).toBeNull();
    expect(ContextRoutineEnvelopeSchema.parse({ routine: webhook })?.trigger.kind).toBe("webhook");
    expect(ContextRoutineEnvelopeSchema.parse({})).toBeNull();
    expect(ContextRoutineRunEnvelopeSchema.parse({ run: null })).toBeNull();
    expect(ContextRoutineRunEnvelopeSchema.parse({ run: wireRoutine.last_run })?.status).toBe("completed");
  });

  it("reads a run history, dropping malformed rows, and an empty list for a malformed body", () => {
    const runs = parseWithFallback<ContextRoutineRun[]>(
      {
        runs: [
          wireRoutine.last_run,
          { id: "55555555-5555-4555-8555-555555555555", status: "failed", source: "manual", failure_reason: "boom", created_at: "2026-10-02T01:00:00Z", completed_at: null },
          { status: "completed" },
          "x",
        ],
      },
      ContextRoutineRunsListSchema,
      [],
      opts,
    );
    expect(runs.map((run) => [run.status, run.source, run.failureReason, run.completedAt])).toEqual([
      ["completed", "schedule", "", "2026-10-01T01:02:00Z"],
      ["failed", "manual", "boom", null],
    ]);
    expect(parseWithFallback<ContextRoutineRun[]>({ runs: "nope" }, ContextRoutineRunsListSchema, [], opts)).toEqual([]);
    expect(parseWithFallback<ContextRoutineRun[]>(null, ContextRoutineRunsListSchema, [], opts)).toEqual([]);
  });

  it("grants routine editing only for a literal true", () => {
    const rights = (value: unknown) =>
      ContextConfigSceneDetailSchema.parse({ scene: { scope_key: sceneId, kind: "group" }, rights: value }).rights;
    expect(rights({ toggle: true, connect: true, edit_prompts: true, edit_mcp: true, edit_routines: true })?.editRoutines).toBe(true);
    expect(rights({ toggle: true, connect: true, edit_prompts: true, edit_mcp: true, edit_routines: "yes" })?.editRoutines).toBe(false);
    expect(rights({ toggle: true, connect: true, edit_prompts: true, edit_mcp: true })?.editRoutines).toBe(false);
  });
});
