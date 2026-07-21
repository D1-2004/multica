import { afterEach, describe, expect, it, vi } from "vitest";
import type { ChatDonePayload } from "../types/events";
import {
  acknowledgeLiveChatReply,
  claimLiveChatReply,
  clearLiveChatRepliesForTests,
  peekLiveChatReply,
  rememberLiveChatReply,
  releaseLiveChatReply,
  subscribeLiveChatReplies,
} from "./reply-receipt";

function donePayload(
  overrides: Partial<ChatDonePayload> = {},
): ChatDonePayload {
  return {
    chat_session_id: "session-1",
    task_id: "task-1",
    trace_id: "trace-1",
    trace_started_at_unix_ms: 1_000,
    message_id: "message-1",
    content: "done",
    ...overrides,
  };
}

afterEach(() => {
  clearLiveChatRepliesForTests();
  vi.restoreAllMocks();
});

describe("live chat reply receipt registry", () => {
  it("keeps the first websocket receive time across replay", () => {
    rememberLiveChatReply(donePayload(), 2_000);
    rememberLiveChatReply(donePayload(), 3_000);

    expect(peekLiveChatReply("message-1", 3_000)).toEqual({
      messageId: "message-1",
      chatSessionId: "session-1",
      taskId: "task-1",
      traceId: "trace-1",
      traceStartedAtUnixMs: 1_000,
      wsReceivedAtUnixMs: 2_000,
    });
  });

  it("atomically hands a live reply to only one mounted surface", () => {
    rememberLiveChatReply(donePayload(), 2_000);

    expect(claimLiveChatReply("message-1", 2_001)?.traceId).toBe("trace-1");
    expect(claimLiveChatReply("message-1", 2_002)).toBeNull();
  });

  it("releases a failed report for retry and suppresses websocket replay after ack", () => {
    rememberLiveChatReply(donePayload(), 2_000);

    const first = claimLiveChatReply("message-1", 2_001);
    expect(first?.reportAttempt).toBe(1);
    expect(releaseLiveChatReply("message-1", "trace-1", 2_002)).toBe(true);
    expect(claimLiveChatReply("message-1", 2_002)?.reportAttempt).toBe(2);
    expect(acknowledgeLiveChatReply("message-1", "trace-1")).toBe(true);

    rememberLiveChatReply(donePayload(), 2_100);
    expect(peekLiveChatReply("message-1", 2_100)).toBeNull();
    expect(claimLiveChatReply("message-1", 2_100)).toBeNull();
  });

  it("does not register a chat done event without complete trace data", () => {
    const listener = vi.fn();
    const unsubscribe = subscribeLiveChatReplies(listener);

    expect(
      rememberLiveChatReply(donePayload({ trace_id: undefined }), 2_000),
    ).toBeNull();
    expect(
      rememberLiveChatReply(
        donePayload({ trace_started_at_unix_ms: undefined }),
        2_000,
      ),
    ).toBeNull();
    expect(listener).not.toHaveBeenCalled();
    unsubscribe();
  });

  it("expires an unrendered live reply instead of acknowledging history", () => {
    rememberLiveChatReply(donePayload(), 2_000);

    expect(peekLiveChatReply("message-1", 302_001)).toBeNull();
    expect(claimLiveChatReply("message-1", 302_001)).toBeNull();
  });
});
