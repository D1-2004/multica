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

function firstMeaningfulLine(text: string): string {
  for (const raw of text.split("\n")) {
    const line = raw.replace(/^[-*]\s+/, "").replace(/^#+\s+/, "").trim();
    if (line && !isEmptyMemoryBody(line)) {
      return line;
    }
  }
  return "";
}

export function sceneDisplayTitle(
  memory: Pick<AgentSceneMemory, "scene_title" | "memory_text">,
  untitled: string,
): string {
  const named = memory.scene_title.trim();
  if (named) {
    return named;
  }
  for (const section of parseMemorySections(memory.memory_text)) {
    const fromBody = firstMeaningfulLine(section.body);
    if (fromBody) {
      return fromBody.length > 22 ? `${fromBody.slice(0, 22)}…` : fromBody;
    }
  }
  return untitled;
}

export function scenePreview(
  memory: Pick<AgentSceneMemory, "memory_text">,
): string {
  return memory.memory_text
    .replace(/^#+\s+/gm, "")
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
