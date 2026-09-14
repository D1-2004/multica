export interface DSHTrajectoryHeader {
  type: "session";
  version: number;
  id: string;
  createdAt: number;
  cwd?: string;
  parentSession?: string;
  seedLength?: number;
  isSeeded?: boolean;
  origin?: "subagent";
  delegationDepth?: number;
  agentPreset?: string;
}

export type TrajectoryCategory = "user" | "assistant" | "tool" | "system";

export interface DSHTrajectoryEvent {
  type: string;
  seq: number;
  time: number;
  data: Record<string, unknown>;
  ignorable?: true;
  surfaceOp?: unknown;
  sourceEventSeqs?: number[];
}

export interface DSHTrajectoryDocument {
  header: DSHTrajectoryHeader;
  events: DSHTrajectoryEvent[];
  scope?: { sessionId: string; requestId: string; firstSeq: number; lastSeq: number };
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

export function parseDSHTrajectory(jsonl: string): DSHTrajectoryDocument {
  const lines = jsonl.split(/\r?\n/).filter((line) => line.trim() !== "");
  if (lines.length === 0) throw new Error("empty DSH trajectory");
  const first: unknown = JSON.parse(lines[0]!);
  let scope: DSHTrajectoryDocument["scope"];
  let headerIndex = 0;
  if (isRecord(first) && first.type === "multica/task-trajectory") {
    if (first.version !== 1 || typeof first.sessionId !== "string" || !first.sessionId ||
      typeof first.requestId !== "string" || !first.requestId ||
      !Number.isSafeInteger(first.firstSeq) || Number(first.firstSeq) < 0 ||
      !Number.isSafeInteger(first.lastSeq) || Number(first.lastSeq) < Number(first.firstSeq) ||
      Number(first.lastSeq) - Number(first.firstSeq) + 1 !== lines.length - 2) {
      throw new Error("invalid DSH trajectory scope");
    }
    scope = { sessionId: first.sessionId, requestId: first.requestId, firstSeq: Number(first.firstSeq), lastSeq: Number(first.lastSeq) };
    headerIndex = 1;
  }
  const rawHeader: unknown = JSON.parse(lines[headerIndex]!);
  if (
    !isRecord(rawHeader) ||
    rawHeader.type !== "session" ||
    typeof rawHeader.id !== "string" ||
    !(rawHeader.version === 0 ||
      (rawHeader.version === 3 && rawHeader.isSeeded === false)) ||
    !Number.isSafeInteger(rawHeader.createdAt) ||
    Number(rawHeader.createdAt) < 0 ||
    !(rawHeader.delegationDepth === 0 ||
      (rawHeader.version === 3 && rawHeader.delegationDepth === undefined)) ||
    rawHeader.parentSession !== undefined ||
    rawHeader.origin !== undefined
  ) {
    throw new Error("invalid DSH trajectory header");
  }
  if (scope && (rawHeader.version !== 3 || rawHeader.id !== scope.sessionId)) {
    throw new Error("DSH trajectory scope does not match its native header");
  }

  const events: DSHTrajectoryEvent[] = [];
  for (let index = headerIndex + 1; index < lines.length; index += 1) {
    const raw: unknown = JSON.parse(lines[index]!);
    if (
      !isRecord(raw) ||
      typeof raw.type !== "string" ||
      raw.type === "" || raw.type === "session" ||
      !Number.isSafeInteger(raw.seq) ||
      raw.seq !== (scope?.firstSeq ?? 0) + index - headerIndex - 1 ||
      !Number.isSafeInteger(raw.time) ||
      Number(raw.time) < 0 ||
      !isRecord(raw.data)
    ) {
      throw new Error(`invalid DSH trajectory event ${index - 1}`);
    }
    events.push(raw as unknown as DSHTrajectoryEvent);
  }
  if (scope) {
    const firstEvent = events[0];
    const lastEvent = events.at(-1);
    const turn = firstEvent?.data.turn;
    const reason = lastEvent?.data.reason;
    if (firstEvent?.type !== "turn/start" || !Number.isSafeInteger(turn) || Number(turn) < 1 ||
      lastEvent?.type !== "turn/end" || lastEvent.data.turn !== turn || !isRecord(reason) ||
      !["completed", "aborted", "error", "interrupted"].includes(String(reason.kind))) {
      throw new Error("incomplete DSH task turn");
    }
    let requests = 0;
    for (const [index, event] of events.entries()) {
      if ((event.type === "turn/start" && index !== 0) || (event.type === "turn/end" && index !== events.length - 1)) {
        throw new Error("DSH trajectory contains another turn");
      }
      if (event.type === "user/message") {
        const source = event.data.source;
        if (!isRecord(source) || typeof source.kind !== "string" || !source.kind) throw new Error("invalid DSH message source");
        if (source.kind === "user" && (++requests !== 1 || source.rpcId !== scope.requestId)) {
          throw new Error("DSH trajectory contains another request");
        }
      }
    }
    if (requests !== 1) throw new Error("DSH trajectory has no task request");
  }
  return {
    header: rawHeader as unknown as DSHTrajectoryHeader,
    events,
    scope,
  };
}

export function trajectoryCategory(type: string): TrajectoryCategory {
  if (type === "user/message") return "user";
  if (type.startsWith("assistant/")) return "assistant";
  if (
    type.startsWith("tool/") ||
    type.includes("subagent") ||
    type.includes("code-dispatch")
  ) {
    return "tool";
  }
  return "system";
}

function textFromContent(value: unknown): string {
  if (typeof value === "string") return value;
  if (Array.isArray(value)) {
    return value
      .map((part) => {
        if (typeof part === "string") return part;
        if (!isRecord(part)) return "";
        if (typeof part.text === "string") return part.text;
        if (typeof part.content === "string") return part.content;
        if (typeof part.name === "string") return part.name;
        return "";
      })
      .filter(Boolean)
      .join(" ");
  }
  if (!isRecord(value)) return "";
  if (typeof value.text === "string") return value.text;
  if (typeof value.content === "string") return value.content;
  return textFromContent(value.content);
}

function compact(value: string, limit = 320): string {
  const oneLine = value.replace(/\s+/g, " ").trim();
  return oneLine.length > limit ? `${oneLine.slice(0, limit - 1)}…` : oneLine;
}

export function trajectoryEventSummary(event: DSHTrajectoryEvent): string {
  const data = event.data;
  switch (event.type) {
    case "user/message":
      return compact(textFromContent(data.content)) || "User message";
    case "assistant/message": {
      const message = isRecord(data.message) ? data.message : data;
      return compact(textFromContent(message.content)) || "Assistant message";
    }
    case "assistant/chunk": {
      const chunk = isRecord(data.chunk) ? data.chunk : data.chunk;
      return compact(textFromContent(chunk)) || "Streaming chunk";
    }
    case "tool/call": {
      const name = typeof data.name === "string" ? data.name : "tool";
      const args = typeof data.arguments === "string" ? data.arguments : "";
      return compact(`${name}${args ? ` · ${args}` : ""}`);
    }
    case "tool/result": {
      const message = isRecord(data.message) ? data.message : data;
      return compact(textFromContent(message.content)) || "Tool result";
    }
    case "turn/start":
      return `Turn ${String(data.turn ?? "")}`.trim();
    case "turn/end": {
      const reason = isRecord(data.reason) ? data.reason.kind : data.reason;
      return compact(`Turn ${String(data.turn ?? "")} · ${String(reason ?? "ended")}`);
    }
    case "step/start":
    case "step/end":
      return `Turn ${String(data.turn ?? "")} · Step ${String(data.step ?? "")}`;
    case "request/header": {
      const header = isRecord(data.header) ? data.header : {};
      const config = isRecord(header.config) ? header.config : {};
      return compact(
        [config.provider, config.model, data.reason].filter(Boolean).map(String).join(" · "),
      ) || "Model request";
    }
    case "todo/write": {
      const count = Array.isArray(data.todos) ? data.todos.length : 0;
      return `${count} todo${count === 1 ? "" : "s"}`;
    }
    default:
      return compact(textFromContent(data)) || event.type;
  }
}

export function trajectoryEventTurn(event: DSHTrajectoryEvent): number | null {
  return Number.isSafeInteger(event.data.turn) ? Number(event.data.turn) : null;
}

export function trajectoryEventStep(event: DSHTrajectoryEvent): number | null {
  return Number.isSafeInteger(event.data.step) ? Number(event.data.step) : null;
}

export interface TrajectorySpan {
  startSeq: number;
  endSeq: number;
  startTime: number;
  endTime: number;
  category: TrajectoryCategory;
  label: string;
}

export function buildTrajectorySpans(events: DSHTrajectoryEvent[]): TrajectorySpan[] {
  const spans: TrajectorySpan[] = [];
  const openSteps = new Map<string, DSHTrajectoryEvent>();
  const openTools = new Map<string, DSHTrajectoryEvent>();

  for (const event of events) {
    if (event.type === "step/start") {
      openSteps.set(`${String(event.data.turn)}:${String(event.data.step)}`, event);
      continue;
    }
    if (event.type === "step/end") {
      const key = `${String(event.data.turn)}:${String(event.data.step)}`;
      const start = openSteps.get(key);
      if (start) {
        spans.push({
          startSeq: start.seq,
          endSeq: event.seq,
          startTime: start.time,
          endTime: Math.max(start.time, event.time),
          category: "assistant",
          label: `Step ${String(event.data.step)}`,
        });
        openSteps.delete(key);
      }
      continue;
    }
    if (event.type === "tool/call") {
      const callID = typeof event.data.callId === "string" ? event.data.callId : "";
      if (callID) openTools.set(callID, event);
      continue;
    }
    if (event.type === "tool/result") {
      const message = isRecord(event.data.message) ? event.data.message : {};
      const callID =
        typeof message.toolCallId === "string"
          ? message.toolCallId
          : typeof event.data.callId === "string"
            ? event.data.callId
            : "";
      const start = callID ? openTools.get(callID) : undefined;
      if (start) {
        spans.push({
          startSeq: start.seq,
          endSeq: event.seq,
          startTime: start.time,
          endTime: Math.max(start.time, event.time),
          category: "tool",
          label: typeof start.data.name === "string" ? start.data.name : "Tool",
        });
        openTools.delete(callID);
      }
    }
  }

  for (const event of events) {
    if (event.type !== "user/message" && event.type !== "assistant/message") continue;
    spans.push({
      startSeq: event.seq,
      endSeq: event.seq,
      startTime: event.time,
      endTime: event.time,
      category: trajectoryCategory(event.type),
      label: event.type,
    });
  }
  return spans.sort((left, right) => left.startTime - right.startTime || left.startSeq - right.startSeq);
}
