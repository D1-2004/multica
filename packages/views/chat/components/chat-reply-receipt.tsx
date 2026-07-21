"use client";

import { useEffect, useSyncExternalStore } from "react";
import { api } from "@multica/core/api";
import {
  acknowledgeLiveChatReply,
  claimLiveChatReply,
  getLiveChatRepliesRevision,
  peekLiveChatReply,
  releaseLiveChatReply,
  subscribeLiveChatReplies,
} from "@multica/core/chat";
import { createLogger } from "@multica/core/logger";
import { useAppForeground } from "../../common/use-app-foreground";

const receiptLogger = createLogger("chat.reply-receipt");

interface ChatReplyReceiptProps {
  messageId: string;
  receiptEnabled: boolean;
}

/** Reports a live assistant reply after its bubble has reached a paint frame. */
export function ChatReplyReceipt({
  messageId,
  receiptEnabled,
}: ChatReplyReceiptProps) {
  const appForeground = useAppForeground();
  const revision = useSyncExternalStore(
    subscribeLiveChatReplies,
    getLiveChatRepliesRevision,
    getLiveChatRepliesRevision,
  );
  const hasLiveReply = peekLiveChatReply(messageId) !== null;

  useEffect(() => {
    if (!receiptEnabled || !appForeground || !hasLiveReply) return;

    const frameId = requestAnimationFrame(() => {
      const reply = claimLiveChatReply(messageId);
      if (!reply) return;

      const clientReceivedAtUnixMs = Date.now();
      void api.reportChatReplyReceived(
        reply.chatSessionId,
        reply.messageId,
        {
          task_id: reply.taskId,
          trace_id: reply.traceId,
          ws_received_at_unix_ms: reply.wsReceivedAtUnixMs,
          rendered_at_unix_ms: clientReceivedAtUnixMs,
          client_received_at: new Date(clientReceivedAtUnixMs).toISOString(),
          elapsed_ms: clientReceivedAtUnixMs - reply.traceStartedAtUnixMs,
        },
      ).then(
        () => {
          acknowledgeLiveChatReply(reply.messageId, reply.traceId);
        },
        (error: unknown) => {
          const retryDelayMs = Math.min(
            1_000 * 2 ** Math.min(reply.reportAttempt - 1, 5),
            30_000,
          );
          receiptLogger.warn("failed to report rendered assistant reply", {
            message_id: reply.messageId,
            task_id: reply.taskId,
            retry_delay_ms: retryDelayMs,
            error,
          });
          globalThis.setTimeout(() => {
            releaseLiveChatReply(reply.messageId, reply.traceId);
          }, retryDelayMs);
        },
      );
    });

    return () => cancelAnimationFrame(frameId);
  }, [appForeground, hasLiveReply, messageId, receiptEnabled, revision]);

  return null;
}
