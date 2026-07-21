import { act, render, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  clearLiveChatRepliesForTests,
  rememberLiveChatReply,
} from "@multica/core/chat";
import type { ChatDonePayload } from "@multica/core/types";
import { ChatReplyReceipt } from "./chat-reply-receipt";

const testState = vi.hoisted(() => ({
  appForeground: true,
  reportChatReplyReceived: vi.fn(),
}));

vi.mock("@multica/core/api", () => ({
  api: {
    reportChatReplyReceived: testState.reportChatReplyReceived,
  },
}));

vi.mock("../../common/use-app-foreground", () => ({
  useAppForeground: () => testState.appForeground,
}));

const TRACE_STARTED_AT = 1_000;
const WS_RECEIVED_AT = 2_000;
const RENDERED_AT = 2_250;

function donePayload(): ChatDonePayload {
  return {
    chat_session_id: "session-1",
    task_id: "task-1",
    trace_id: "trace-1",
    trace_started_at_unix_ms: TRACE_STARTED_AT,
    message_id: "message-1",
    content: "done",
  };
}

describe("ChatReplyReceipt", () => {
  let frames: Map<number, FrameRequestCallback>;
  let nextFrameId: number;

  beforeEach(() => {
    frames = new Map();
    nextFrameId = 1;
    testState.appForeground = true;
    testState.reportChatReplyReceived.mockReset();
    testState.reportChatReplyReceived.mockResolvedValue(undefined);
    vi.spyOn(Date, "now").mockReturnValue(RENDERED_AT);
    vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => {
      const id = nextFrameId++;
      frames.set(id, callback);
      return id;
    });
    vi.stubGlobal("cancelAnimationFrame", (id: number) => {
      frames.delete(id);
    });
  });

  afterEach(() => {
    clearLiveChatRepliesForTests();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  function flushFrames() {
    act(() => {
      const pending = [...frames.values()];
      frames.clear();
      for (const callback of pending) callback(RENDERED_AT);
    });
  }

  it("reports once on the paint frame after a live reply reaches a mounted assistant", async () => {
    const view = render(
      <ChatReplyReceipt messageId="message-1" receiptEnabled />,
    );

    act(() => {
      rememberLiveChatReply(donePayload(), WS_RECEIVED_AT);
    });

    expect(testState.reportChatReplyReceived).not.toHaveBeenCalled();
    flushFrames();

    await waitFor(() => {
      expect(testState.reportChatReplyReceived).toHaveBeenCalledWith(
        "session-1",
        "message-1",
        {
          task_id: "task-1",
          trace_id: "trace-1",
          ws_received_at_unix_ms: WS_RECEIVED_AT,
          rendered_at_unix_ms: RENDERED_AT,
          client_received_at: "1970-01-01T00:00:02.250Z",
          elapsed_ms: RENDERED_AT - TRACE_STARTED_AT,
        },
      );
    });

    view.rerender(
      <ChatReplyReceipt messageId="message-1" receiptEnabled />,
    );
    flushFrames();
    expect(testState.reportChatReplyReceived).toHaveBeenCalledTimes(1);
  });

  it("waits while the floating surface is closed", async () => {
    rememberLiveChatReply(donePayload(), WS_RECEIVED_AT);
    const view = render(
      <ChatReplyReceipt messageId="message-1" receiptEnabled={false} />,
    );

    flushFrames();
    expect(testState.reportChatReplyReceived).not.toHaveBeenCalled();

    view.rerender(
      <ChatReplyReceipt messageId="message-1" receiptEnabled />,
    );
    flushFrames();

    await waitFor(() => {
      expect(testState.reportChatReplyReceived).toHaveBeenCalledTimes(1);
    });
  });

  it("waits until the app returns to the foreground", async () => {
    testState.appForeground = false;
    rememberLiveChatReply(donePayload(), WS_RECEIVED_AT);
    const view = render(
      <ChatReplyReceipt messageId="message-1" receiptEnabled />,
    );

    flushFrames();
    expect(testState.reportChatReplyReceived).not.toHaveBeenCalled();

    testState.appForeground = true;
    view.rerender(
      <ChatReplyReceipt messageId="message-1" receiptEnabled />,
    );
    flushFrames();

    await waitFor(() => {
      expect(testState.reportChatReplyReceived).toHaveBeenCalledTimes(1);
    });
  });

  it("lets only one simultaneously mounted chat surface claim the receipt", async () => {
    rememberLiveChatReply(donePayload(), WS_RECEIVED_AT);
    render(
      <>
        <ChatReplyReceipt messageId="message-1" receiptEnabled />
        <ChatReplyReceipt messageId="message-1" receiptEnabled />
      </>,
    );

    flushFrames();

    await waitFor(() => {
      expect(testState.reportChatReplyReceived).toHaveBeenCalledTimes(1);
    });
  });
});
