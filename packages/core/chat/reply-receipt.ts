import type { ChatDonePayload } from "../types/events";

const LIVE_REPLY_TTL_MS = 5 * 60 * 1000;
const MAX_LIVE_REPLIES = 500;

export interface LiveChatReply {
  messageId: string;
  chatSessionId: string;
  taskId: string;
  traceId: string;
  traceStartedAtUnixMs: number;
  wsReceivedAtUnixMs: number;
}

interface LiveChatReplyEntry {
  reply: LiveChatReply;
  state: "pending" | "in_flight" | "acked";
  reportAttempts: number;
}

export interface ClaimedLiveChatReply extends LiveChatReply {
  reportAttempt: number;
}

const liveReplies = new Map<string, LiveChatReplyEntry>();
const listeners = new Set<() => void>();
let revision = 0;

function publishChange() {
  revision += 1;
  for (const listener of listeners) listener();
}

function pruneExpired(nowUnixMs: number) {
  let changed = false;
  for (const [messageId, entry] of liveReplies) {
    if (nowUnixMs - entry.reply.wsReceivedAtUnixMs <= LIVE_REPLY_TTL_MS) continue;
    liveReplies.delete(messageId);
    changed = true;
  }
  if (changed) publishChange();
}

/**
 * Registers the first websocket delivery for a traceable assistant message.
 * Reconnect replay keeps the original receive time instead of extending it.
 */
export function rememberLiveChatReply(
  payload: ChatDonePayload,
  wsReceivedAtUnixMs = Date.now(),
): LiveChatReply | null {
  pruneExpired(wsReceivedAtUnixMs);

  const traceStartedAtUnixMs = payload.trace_started_at_unix_ms;

  if (
    !payload.message_id ||
    !payload.trace_id ||
    typeof traceStartedAtUnixMs !== "number" ||
    !Number.isFinite(traceStartedAtUnixMs) ||
    traceStartedAtUnixMs <= 0
  ) {
    return null;
  }

  const existing = liveReplies.get(payload.message_id);
  if (existing) return existing.reply;

  const reply: LiveChatReply = {
    messageId: payload.message_id,
    chatSessionId: payload.chat_session_id,
    taskId: payload.task_id,
    traceId: payload.trace_id,
    traceStartedAtUnixMs,
    wsReceivedAtUnixMs,
  };
  liveReplies.set(reply.messageId, {
    reply,
    state: "pending",
    reportAttempts: 0,
  });

  while (liveReplies.size > MAX_LIVE_REPLIES) {
    const oldestMessageId = liveReplies.keys().next().value as string | undefined;
    if (!oldestMessageId) break;
    liveReplies.delete(oldestMessageId);
  }

  publishChange();
  return reply;
}

/** Atomically claims a pending reply so only one mounted chat surface reports it. */
export function claimLiveChatReply(
  messageId: string,
  nowUnixMs = Date.now(),
): ClaimedLiveChatReply | null {
  pruneExpired(nowUnixMs);
  const entry = liveReplies.get(messageId);
  if (!entry || entry.state !== "pending") return null;
  entry.state = "in_flight";
  entry.reportAttempts += 1;
  publishChange();
  return { ...entry.reply, reportAttempt: entry.reportAttempts };
}

/** Marks an in-flight receipt durable after the server acknowledges it. */
export function acknowledgeLiveChatReply(messageId: string, traceId: string): boolean {
  const entry = liveReplies.get(messageId);
  if (!entry || entry.state !== "in_flight" || entry.reply.traceId !== traceId) {
    return false;
  }
  entry.state = "acked";
  publishChange();
  return true;
}

/** Returns a failed in-flight report to pending after the caller's retry delay. */
export function releaseLiveChatReply(
  messageId: string,
  traceId: string,
  nowUnixMs = Date.now(),
): boolean {
  const entry = liveReplies.get(messageId);
  if (!entry || entry.state !== "in_flight" || entry.reply.traceId !== traceId) {
    return false;
  }
  if (nowUnixMs - entry.reply.wsReceivedAtUnixMs > LIVE_REPLY_TTL_MS) {
    liveReplies.delete(messageId);
  } else {
    entry.state = "pending";
  }
  publishChange();
  return true;
}

export function peekLiveChatReply(
  messageId: string,
  nowUnixMs = Date.now(),
): LiveChatReply | null {
  const entry = liveReplies.get(messageId);
  if (
    !entry ||
    entry.state !== "pending" ||
    nowUnixMs - entry.reply.wsReceivedAtUnixMs > LIVE_REPLY_TTL_MS
  ) {
    return null;
  }
  return entry.reply;
}

export function subscribeLiveChatReplies(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

export function getLiveChatRepliesRevision(): number {
  return revision;
}

export function clearLiveChatRepliesForTests() {
  if (liveReplies.size === 0) return;
  liveReplies.clear();
  publishChange();
}
