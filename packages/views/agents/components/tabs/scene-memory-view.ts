import type { AgentSceneMemory } from "@multica/core/types";

export type MemorySection = {
  heading: string;
  body: string;
};

const EMPTY_MARKERS = new Set(["(暂无)", "（暂无）", "(none)", "暂无", "无"]);

export function parseMemorySections(text: string): MemorySection[] {
  const trimmed = text.trim();
  if (!trimmed) {
    return [];
  }
  if (!/^##\s+/m.test(trimmed)) {
    return [{ heading: "", body: trimmed }];
  }
  const chunks = trimmed.split(/^##\s+/m).filter((chunk) => chunk.trim() !== "");
  return chunks.map((chunk) => {
    const newline = chunk.indexOf("\n");
    if (newline < 0) {
      return { heading: chunk.trim(), body: "" };
    }
    return {
      heading: chunk.slice(0, newline).trim(),
      body: chunk.slice(newline + 1).trim(),
    };
  });
}

export function isEmptyMemoryBody(body: string): boolean {
  return EMPTY_MARKERS.has(body.trim());
}

const GENERIC_TITLES = new Set([
  "钉钉群聊",
  "钉钉群",
  "钉钉单聊",
  "钉钉消息",
  "群聊",
  "群聊场景",
  "本会话尚在观察中",
  "[推断] 本会话尚在观察中",
]);

function stripTitlePunct(value: string): string {
  return value.replace(/[。．.\s]+$/u, "").trim();
}

function isGenericSceneTitle(line: string): boolean {
  const normalized = stripTitlePunct(line);
  if (GENERIC_TITLES.has(normalized) || normalized.startsWith("[推断]")) {
    return true;
  }
  return /^(群聊|钉钉群)/.test(normalized);
}

function locatingTitle(text: string): string {
  const sections = parseMemorySections(text);
  const locating =
    sections.find((section) => section.heading === "场域定位") ?? sections[0];
  if (!locating) {
    return "";
  }
  let named = "";
  let members = "";
  for (const raw of locating.body.split("\n")) {
    const line = raw.replace(/^[-*]\s+/, "").replace(/^#+\s+/, "").trim();
    if (!line || isEmptyMemoryBody(line)) {
      continue;
    }
    const memberMatch =
      line.match(/(?:已知)?成员[：:]\s*(.+)$/) ??
      line.match(/参与者包括(.+)$/);
    if (memberMatch?.[1]) {
      members = stripTitlePunct(
        memberMatch[1].replace(/和/g, "、").replace(/[。．.]+$/u, ""),
      );
      continue;
    }
    if (/^本会话/.test(line) || /^这是与/.test(line)) {
      continue;
    }
    if (!named && !isGenericSceneTitle(line)) {
      named = stripTitlePunct(line);
    }
  }
  return named || members;
}

export function sceneDisplayTitle(
  memory: Pick<AgentSceneMemory, "scene_title" | "memory_text">,
  untitled: string,
): string {
  const stored = memory.scene_title.trim();
  if (stored && !isGenericSceneTitle(stored)) {
    return stored;
  }
  const fromLocating = locatingTitle(memory.memory_text);
  if (fromLocating) {
    return fromLocating;
  }
  return stored || untitled;
}

export function displayMatterTitle(raw: string, untitled = ""): string {
  let value = raw.replace(/<@[^>]+>/g, " ").trim();
  const delegated = value.match(/^.{1,16}委托[:：]\s*(.+)$/);
  if (delegated?.[1]) {
    value = delegated[1].trim();
  }
  value = value.replace(/\s+/g, " ").trim();
  return value || untitled;
}

export function partitionSceneMemories<T extends { scene_kind: string }>(
  memories: T[],
): { dms: T[]; groups: T[] } {
  const dms: T[] = [];
  const groups: T[] = [];
  for (const memory of memories) {
    if (memory.scene_kind === "group") {
      groups.push(memory);
    } else {
      dms.push(memory);
    }
  }
  return { dms, groups };
}

const LOCATING_HEADING = "场域定位";

export function visibleMemorySections(
  text: string,
  sceneKind = "",
): MemorySection[] {
  return parseMemorySections(text).filter((section) => {
    if (section.heading === LOCATING_HEADING && sceneKind !== "group") {
      return false;
    }
    if (!section.body || isEmptyMemoryBody(section.body)) {
      return false;
    }
    return true;
  });
}

function locatingPurposeLine(body: string): string {
  let fallback = "";
  for (const raw of body.split("\n")) {
    const line = raw.replace(/^[-*]\s+/, "").replace(/^#+\s+/, "").trim();
    if (!line || isEmptyMemoryBody(line)) {
      continue;
    }
    if (/(?:已知)?成员[：:]/.test(line) || /参与者包括/.test(line)) {
      continue;
    }
    const purpose = line.match(/^用途[：:]\s*(.+)$/);
    if (purpose?.[1] && !isGenericSceneTitle(purpose[1])) {
      return stripTitlePunct(purpose[1]);
    }
    if (!fallback && !isGenericSceneTitle(line) && !/^本会话/.test(line)) {
      fallback = stripTitlePunct(line);
    }
  }
  return fallback;
}

export function scenePreview(
  memory: Pick<AgentSceneMemory, "memory_text" | "scene_kind">,
): string {
  const sections = parseMemorySections(memory.memory_text);
  const knowledge = sections.find((section) =>
    /稳定知识|约定/.test(section.heading),
  );
  if (knowledge?.body && !isEmptyMemoryBody(knowledge.body)) {
    return knowledge.body
      .replace(/^[-*]\s+/gm, "")
      .replace(/[（(]暂无[)）]/g, " ")
      .replace(/\s+/g, " ")
      .trim();
  }
  if (memory.scene_kind === "group") {
    const locating = sections.find(
      (section) => section.heading === LOCATING_HEADING,
    );
    const purpose = locatingPurposeLine(locating?.body ?? "");
    if (purpose) {
      return purpose;
    }
  }
  const source = sections
    .filter((section) => section.heading !== LOCATING_HEADING)
    .map((section) => section.body)
    .join(" ");
  return source
    .replace(/^[-*]\s+/gm, "")
    .replace(/[（(]暂无[)）]/g, " ")
    .replace(/\s+/g, " ")
    .trim();
}

export function memoryStatusKey(
  status: string,
): "clean" | "pending" | "running" | "retrying" | "blocked" {
  switch (status) {
    case "pending":
    case "running":
    case "retrying":
    case "blocked":
      return status;
    default:
      return "clean";
  }
}
