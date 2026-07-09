import { describe, expect, it } from "vitest";

import type { TaskMessagePayload } from "../types/events";
import {
  isTaskMessageTaskId,
  mergeTaskMessagesBySeq,
  pendingChatTaskOptions,
  pendingChatTasksOptions,
  taskMessagesOptions,
} from "./queries";

const msg = (seq: number): TaskMessagePayload => ({
  task_id: "task-1",
  issue_id: "issue-1",
  seq,
  type: "text",
  content: `m${seq}`,
});

describe("taskMessagesOptions", () => {
  it("fetches task messages for persisted UUID task ids", () => {
    const taskId = "4a2e8d1c-7f9b-4e2a-9c1d-123456789abc";

    expect(isTaskMessageTaskId(taskId)).toBe(true);
    expect(taskMessagesOptions(taskId).enabled).toBe(true);
  });

  it("does not fetch task messages for optimistic task ids", () => {
    const taskId = "optimistic-optimistic-1778739487737";

    expect(isTaskMessageTaskId(taskId)).toBe(false);
    expect(taskMessagesOptions(taskId).enabled).toBe(false);
  });
});

describe("pendingChatTaskOptions", () => {
  it("does not sync in-flight tasks unless the caller opts in", () => {
    const options = pendingChatTaskOptions("session-1");
    const interval = options.refetchInterval as (query: { state: { data?: { task_id?: string } } }) => number | false;

    expect(interval({ state: { data: { task_id: "task-1" } } })).toBe(false);
  });

  it("keeps syncing while an opted-in chat task is in flight", () => {
    const options = pendingChatTaskOptions("session-1", true);
    const interval = options.refetchInterval as (query: { state: { data?: { task_id?: string } } }) => number | false;

    expect(interval({ state: { data: { task_id: "task-1" } } })).toBe(2000);
  });

  it("stops syncing an opted-in query after the server reports no in-flight chat task", () => {
    const options = pendingChatTaskOptions("session-1", true);
    const interval = options.refetchInterval as (query: { state: { data?: { task_id?: string } } }) => number | false;

    expect(interval({ state: { data: {} } })).toBe(false);
    expect(interval({ state: {} })).toBe(false);
  });
});

describe("pendingChatTasksOptions", () => {
  it("does not sync aggregate in-flight tasks unless the caller opts in", () => {
    const options = pendingChatTasksOptions("ws-1");
    const interval = options.refetchInterval as (query: { state: { data?: { tasks?: unknown[] } } }) => number | false;

    expect(interval({ state: { data: { tasks: [{ task_id: "task-1" }] } } })).toBe(false);
  });

  it("keeps syncing the opted-in aggregate while any chat task is in flight", () => {
    const options = pendingChatTasksOptions("ws-1", true);
    const interval = options.refetchInterval as (query: { state: { data?: { tasks?: unknown[] } } }) => number | false;

    expect(interval({ state: { data: { tasks: [{ task_id: "task-1" }] } } })).toBe(2000);
  });

  it("stops syncing the opted-in aggregate when there are no in-flight chat tasks", () => {
    const options = pendingChatTasksOptions("ws-1", true);
    const interval = options.refetchInterval as (query: { state: { data?: { tasks?: unknown[] } } }) => number | false;

    expect(interval({ state: { data: { tasks: [] } } })).toBe(false);
    expect(interval({ state: {} })).toBe(false);
  });
});

describe("mergeTaskMessagesBySeq", () => {
  it("backfills missing seqs and keeps the list seq-ordered", () => {
    const existing = [msg(1), msg(3)];
    const merged = mergeTaskMessagesBySeq(existing, [msg(2), msg(4)]);

    expect(merged.map((m) => m.seq)).toEqual([1, 2, 3, 4]);
  });

  it("drops duplicate seqs and lets the existing entry win", () => {
    const existing = [{ ...msg(1), content: "ws" }];
    const merged = mergeTaskMessagesBySeq(existing, [
      { ...msg(1), content: "refetch" },
      msg(2),
    ]);

    expect(merged.map((m) => m.seq)).toEqual([1, 2]);
    expect(merged.find((m) => m.seq === 1)?.content).toBe("ws");
  });

  it("preserves the array reference when nothing new arrives", () => {
    const existing = [msg(1), msg(2)];

    // Empty incoming and fully-duplicate incoming must both no-op so React
    // Query observers don't re-render on replayed events.
    expect(mergeTaskMessagesBySeq(existing, [])).toBe(existing);
    expect(mergeTaskMessagesBySeq(existing, [msg(1), msg(2)])).toBe(existing);
  });
});
