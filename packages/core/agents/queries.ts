import type { AgentMemoryLoop } from "../types/agent";
import { infiniteQueryOptions, queryOptions } from "@tanstack/react-query";
import { api } from "../api";
import type {
  WorkspaceWorkingAgentMineRelation,
  WorkspaceWorkingAgentType,
} from "../types";

export const agentTaskSnapshotKeys = {
  all: (wsId: string) => ["workspaces", wsId, "agent-task-snapshot"] as const,
  list: (wsId: string) => [...agentTaskSnapshotKeys.all(wsId), "list"] as const,
};

export const workspaceWorkingAgentsKeys = {
  all: (wsId: string) => ["workspaces", wsId, "working-agents"] as const,
  list: (
    wsId: string,
    type?: WorkspaceWorkingAgentType,
    mineRelation?: WorkspaceWorkingAgentMineRelation,
    parentIssueId?: string,
  ) =>
    [
      ...workspaceWorkingAgentsKeys.all(wsId),
      "list",
      type ?? "all",
      mineRelation
        ? `mine:${mineRelation}`
        : parentIssueId
          ? `parent:${parentIssueId}`
          : "workspace",
    ] as const,
};

export const agentActivityKeys = {
  all: (wsId: string) => ["workspaces", wsId, "agent-activity"] as const,
  last30d: (wsId: string) => [...agentActivityKeys.all(wsId), "30d"] as const,
};

export const agentRunCountsKeys = {
  all: (wsId: string) => ["workspaces", wsId, "agent-run-counts"] as const,
  last30d: (wsId: string) => [...agentRunCountsKeys.all(wsId), "30d"] as const,
};

// Workspace-scoped agent task snapshot — every active task plus each agent's
// most recent terminal task. This is the single shared source of truth that
// powers per-agent presence derivation across the app. One fetch per
// workspace; all agent dots / hover cards / list rows derive presence from
// this cache with zero additional network traffic.
//
// Presence itself is derived from the active tasks only (see derive-presence.ts
// and #1823). The one terminal row per agent is used solely for the Squad hover
// card's "last activity" line; MUL-5436 tracks moving it to a dedicated lazy
// endpoint so this hot query stops carrying history at all.
//
// The 30s staleTime is a safety net only; the primary freshness signal is
// WS task events, which invalidate this query immediately. Without WS,
// presence still updates within 30s on focus / mount.
export function agentTaskSnapshotOptions(wsId: string) {
  return queryOptions({
    queryKey: agentTaskSnapshotKeys.list(wsId),
    queryFn: () => api.getAgentTaskSnapshot(),
    staleTime: 30 * 1000,
    gcTime: 5 * 60 * 1000,
    refetchOnWindowFocus: true,
  });
}

// Working-agent summaries, optionally narrowed to a My Issues relation or to
// one issue's direct children. Task lifecycle WebSocket events invalidate
// every narrowing immediately; the short stale time is the reconnect /
// missed-event safety net.
export function workspaceWorkingAgentsOptions(
  wsId: string,
  type?: WorkspaceWorkingAgentType,
  mineRelation?: WorkspaceWorkingAgentMineRelation,
  parentIssueId?: string,
) {
  return queryOptions({
    queryKey: workspaceWorkingAgentsKeys.list(
      wsId,
      type,
      mineRelation,
      parentIssueId,
    ),
    queryFn: () =>
      api.getWorkspaceWorkingAgents(type, mineRelation, parentIssueId),
    staleTime: 30 * 1000,
    gcTime: 5 * 60 * 1000,
    refetchOnWindowFocus: true,
  });
}

// Workspace-wide daily task activity for the last 30 days, anchored on
// completed_at. One fetch backs both the Agents-list sparkline (which
// only uses the trailing 7 buckets via `summarizeActivityWindow`) and
// the agent detail "Last 30 days" panel. WS task lifecycle events
// invalidate this query in useRealtimeSync; the staleTime is a
// tab-focus safety net.
export function agentActivity30dOptions(wsId: string) {
  return queryOptions({
    queryKey: agentActivityKeys.last30d(wsId),
    queryFn: () => api.getWorkspaceAgentActivity30d(),
    staleTime: 60 * 1000,
    gcTime: 5 * 60 * 1000,
    refetchOnWindowFocus: true,
  });
}

// Workspace-wide 30-day run counts for the Agents-list RUNS column. Same
// single-fetch / WS-invalidate pattern as activity24hOptions.
export function agentRunCounts30dOptions(wsId: string) {
  return queryOptions({
    queryKey: agentRunCountsKeys.last30d(wsId),
    queryFn: () => api.getWorkspaceAgentRunCounts(),
    staleTime: 60 * 1000,
    gcTime: 5 * 60 * 1000,
    refetchOnWindowFocus: true,
  });
}

export const agentTasksKeys = {
  all: (wsId: string) => ["workspaces", wsId, "agent-tasks"] as const,
  detail: (wsId: string, agentId: string) =>
    [...agentTasksKeys.all(wsId), agentId] as const,
};

export const agentSourceKeys = {
  all: (wsId: string) => ["workspaces", wsId, "agent-sources"] as const,
  detail: (wsId: string, agentId: string) =>
    [...agentSourceKeys.all(wsId), agentId] as const,
};

export function agentSourceOptions(wsId: string, agentId: string) {
  return queryOptions({
    queryKey: agentSourceKeys.detail(wsId, agentId),
    queryFn: () => api.getAgentSource(agentId),
    enabled: !!wsId && !!agentId,
    retry: false,
  });
}

export function agentSourceBranchesOptions(wsId: string, agentId: string) {
  return queryOptions({
    queryKey: [...agentSourceKeys.detail(wsId, agentId), "branches"],
    queryFn: () => api.listAgentSourceBranches(agentId),
    enabled: !!wsId && !!agentId,
    retry: false,
  });
}

export function agentPublicationsOptions(wsId: string, agentId: string) {
  return infiniteQueryOptions({
    queryKey: [...agentSourceKeys.detail(wsId, agentId), "publications"],
    queryFn: ({ pageParam }) => api.listAgentPublications(agentId, pageParam),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (page) => page.next_cursor ?? undefined,
    enabled: !!wsId && !!agentId,
    retry: false,
  });
}

// All tasks for a single agent (the agent detail page consumer). Powers both
// the inspector's 7-day throughput stats and the Tasks tab list — shared so
// they don't fetch twice. Task events only mark history stale; mounted views
// refetch on a bounded cadence instead of restarting on every event.
export function agentTasksOptions(wsId: string, agentId: string) {
  return queryOptions({
    queryKey: agentTasksKeys.detail(wsId, agentId),
    queryFn: ({ signal }) => api.listAgentTasks(agentId, signal),
    staleTime: 30 * 1000,
    gcTime: 5 * 60 * 1000,
    refetchInterval: 30 * 1000,
    refetchOnWindowFocus: false,
  });
}

export const agentCoordinatorSessionsKeys = {
  all: (wsId: string) =>
    ["workspaces", wsId, "agent-coordinator-sessions"] as const,
  list: (wsId: string, agentId: string) =>
    [...agentCoordinatorSessionsKeys.all(wsId), agentId] as const,
};

export function agentCoordinatorSessionsOptions(wsId: string, agentId: string) {
  return queryOptions({
    queryKey: agentCoordinatorSessionsKeys.list(wsId, agentId),
    queryFn: () => api.listAgentCoordinatorSessions(agentId),
    enabled: !!wsId && !!agentId,
    staleTime: 15 * 1000,
    gcTime: 5 * 60 * 1000,
    refetchOnWindowFocus: true,
  });
}

/** Unknown future modes are never silently mapped to a writable namespace. */
export function agentSceneMemoryLoop(mode?: string): AgentMemoryLoop | null {
  if (mode === undefined || mode === "coordinator") return "coordinator";
  return mode === "employee" ? "employee" : null;
}

export const agentSceneMemoryKeys = {
  all: (wsId: string) =>
    ["workspaces", wsId, "agent-scene-memory"] as const,
  list: (wsId: string, agentId: string, loop: AgentMemoryLoop = "coordinator") =>
    [...agentSceneMemoryKeys.all(wsId), agentId, loop] as const,
  // Nested under list so the memory mutations' list invalidation refreshes
  // it too.
  detail: (wsId: string, agentId: string, sceneId: string, loop: AgentMemoryLoop = "coordinator") =>
    [...agentSceneMemoryKeys.list(wsId, agentId, loop), "memory", sceneId] as const,
};

/** One scene's memory by its scene_id; the scene detail uses it because the
 * list endpoint returns at most the 200 newest rows. */
export function agentSceneMemoryDetailOptions(
  wsId: string,
  agentId: string,
  sceneId: string,
  enabled = true,
  loop: AgentMemoryLoop = "coordinator",
) {
  const active = enabled && !!wsId && !!agentId && !!sceneId;
  return queryOptions({
    queryKey: agentSceneMemoryKeys.detail(wsId, agentId, sceneId, loop),
    queryFn: () => api.getAgentSceneMemory(agentId, sceneId, loop),
    enabled: active,
    staleTime: 15 * 1000,
    gcTime: 5 * 60 * 1000,
    refetchInterval: active ? 15 * 1000 : false,
    refetchOnWindowFocus: true,
  });
}

export function agentSceneMemoryOptions(
  wsId: string,
  agentId: string,
  enabled = true,
  loop: AgentMemoryLoop = "coordinator",
) {
  return queryOptions({
    queryKey: agentSceneMemoryKeys.list(wsId, agentId, loop),
    queryFn: () => api.listAgentSceneMemory(agentId, loop),
    enabled: enabled && !!wsId && !!agentId,
    staleTime: 15 * 1000,
    gcTime: 5 * 60 * 1000,
    refetchInterval: enabled ? 15 * 1000 : false,
    refetchOnWindowFocus: true,
  });
}

export const agentSceneRelationKeys = {
  list: (wsId: string, agentId: string, sceneId: string) =>
    [...agentSceneMemoryKeys.all(wsId), agentId, "relations", sceneId] as const,
};

/** Issues linked to one scene, by its scene_id. */
export function agentSceneRelationOptions(
  wsId: string,
  agentId: string,
  sceneId: string,
  enabled = true,
) {
  return queryOptions({
    queryKey: agentSceneRelationKeys.list(wsId, agentId, sceneId),
    queryFn: () => api.listAgentSceneRelations(agentId, sceneId),
    enabled: enabled && !!wsId && !!agentId && !!sceneId,
    staleTime: 15 * 1000,
    gcTime: 5 * 60 * 1000,
    refetchOnWindowFocus: true,
  });
}
// Agent templates are workspace-independent: a static catalog served from
// the server's embedded JSON. Cache effectively forever — the only way the
// list / detail change is a server deploy, and a hard reload picks that up.
export const agentTemplateKeys = {
  all: () => ["agent-templates"] as const,
  list: () => [...agentTemplateKeys.all(), "list"] as const,
  detail: (slug: string) => [...agentTemplateKeys.all(), "detail", slug] as const,
};

export function agentTemplateListOptions() {
  return queryOptions({
    queryKey: agentTemplateKeys.list(),
    queryFn: () => api.listAgentTemplates(),
    staleTime: Infinity,
    gcTime: 30 * 60 * 1000,
  });
}

export function agentTemplateDetailOptions(slug: string) {
  return queryOptions({
    queryKey: agentTemplateKeys.detail(slug),
    queryFn: () => api.getAgentTemplate(slug),
    staleTime: Infinity,
    gcTime: 30 * 60 * 1000,
  });
}

/** Unfinished agent-creation conversations, scoped to the caller. */
export const agentBuilderSessionKeys = {
  all: (wsId: string) => ["workspace", wsId, "agent-builder-sessions"] as const,
  list: (wsId: string) => [...agentBuilderSessionKeys.all(wsId), "list"] as const,
};

export function agentBuilderSessionListOptions(wsId: string) {
  return queryOptions({
    queryKey: agentBuilderSessionKeys.list(wsId),
    queryFn: () => api.listAgentBuilderSessions(),
    enabled: wsId.length > 0,
    // Overrides the client-wide `staleTime: Infinity`. This list changes
    // through work done on another screen — starting a conversation, sending a
    // turn, an agent finally being created — and the surfaces that render it
    // mount on demand. Cached forever it would show the state of the first
    // visit: a user who just held a conversation comes back to "no drafts".
    staleTime: 0,
  });
}

export const agentCoordinatorConversationsKeys = {
  list: (wsId: string, agentId: string) =>
    ["workspaces", wsId, "agent-coordinator-conversations", agentId] as const,
  messages: (wsId: string, agentId: string, sessionId: string) =>
    [
      "workspaces",
      wsId,
      "agent-coordinator-conversation-messages",
      agentId,
      sessionId,
    ] as const,
};

export function agentCoordinatorConversationsOptions(
  wsId: string,
  agentId: string,
) {
  return infiniteQueryOptions({
    queryKey: agentCoordinatorConversationsKeys.list(wsId, agentId),
    queryFn: ({ pageParam }) =>
      api.listAgentCoordinatorConversations(agentId, pageParam),
    initialPageParam: 0,
    getNextPageParam: (page, _pages, previousOffset) =>
      page.has_more === true && page.next_offset > previousOffset
        ? page.next_offset
        : undefined,
    enabled: !!wsId && !!agentId,
    staleTime: 15_000,
  });
}

export function agentCoordinatorConversationMessagesOptions(
  wsId: string,
  agentId: string,
  sessionId: string,
) {
  return infiniteQueryOptions({
    queryKey: agentCoordinatorConversationsKeys.messages(
      wsId,
      agentId,
      sessionId,
    ),
    queryFn: ({ pageParam }) =>
      api.listAgentCoordinatorConversationMessages(
        agentId,
        sessionId,
        pageParam,
      ),
    initialPageParam: null as { created_at: string; id: string } | null,
    getNextPageParam: (page) =>
      page.has_more === true ? (page.next_cursor ?? undefined) : undefined,
    enabled: !!wsId && !!agentId && !!sessionId,
    staleTime: 15_000,
  });
}
