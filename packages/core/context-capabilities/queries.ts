import { infiniteQueryOptions, queryOptions } from "@tanstack/react-query";
import { api } from "../api";

/**
 * Mobile configuration keys. The `/api/context-capabilities/*` routes are
 * authorized by the caller's scene/person grants rather than by workspace
 * membership, so they are keyed by agent (and scene) only.
 */
export const contextConfigKeys = {
  all: () => ["context-config"] as const,
  agents: () => [...contextConfigKeys.all(), "agents"] as const,
  agent: (agentId: string) =>
    [...contextConfigKeys.agents(), agentId] as const,
  scenes: (agentId: string) =>
    [...contextConfigKeys.agent(agentId), "scenes"] as const,
  scene: (agentId: string, sceneKey: string) =>
    [...contextConfigKeys.scenes(agentId), sceneKey] as const,
};

/**
 * Admin keys (agent detail → offer sections and the 场域 section),
 * workspace-scoped. Scenes nest under the agent, so invalidating the agent
 * key (e.g. after an offer change) also refreshes the scene list and every
 * scene detail.
 */
export const contextCapabilityKeys = {
  all: (wsId: string) => ["workspaces", wsId, "context-capabilities"] as const,
  agent: (wsId: string, agentId: string) =>
    [...contextCapabilityKeys.all(wsId), agentId] as const,
  scenes: (wsId: string, agentId: string) =>
    [...contextCapabilityKeys.agent(wsId, agentId), "scenes"] as const,
  scene: (wsId: string, agentId: string, sceneKey: string) =>
    [...contextCapabilityKeys.scenes(wsId, agentId), sceneKey] as const,
  /** Official apps with this agent's status (连接应用). Nested under the
   * agent, so offer changes refresh them too. */
  connectedApps: (wsId: string, agentId: string) =>
    [...contextCapabilityKeys.agent(wsId, agentId), "connected-apps"] as const,
  connectedApp: (wsId: string, agentId: string, slug: string) =>
    [...contextCapabilityKeys.connectedApps(wsId, agentId), slug] as const,
};

/** Page size of the admin scene list. */
export const AGENT_SCENES_PAGE_SIZE = 50;

export function contextConfigAgentsOptions() {
  return queryOptions({
    queryKey: contextConfigKeys.agents(),
    queryFn: () => api.listContextConfigAgents(),
  });
}

export function contextConfigAgentOptions(agentId: string) {
  return queryOptions({
    queryKey: contextConfigKeys.agent(agentId),
    queryFn: () => api.getContextConfigAgent(agentId),
    enabled: Boolean(agentId),
  });
}

export function contextConfigSceneOptions(agentId: string, sceneKey: string) {
  return queryOptions({
    queryKey: contextConfigKeys.scene(agentId, sceneKey),
    queryFn: () => api.getContextConfigScene(agentId, sceneKey),
    enabled: Boolean(agentId && sceneKey),
  });
}

export function agentContextCapabilitiesOptions(wsId: string, agentId: string) {
  return queryOptions({
    queryKey: contextCapabilityKeys.agent(wsId, agentId),
    queryFn: () => api.getAgentContextCapabilities(wsId, agentId),
    enabled: Boolean(wsId && agentId),
  });
}

/** The agent's IM scenes (group chats and 1:1 chats), newest activity
 * first, paged by offset. */
export function agentScenesOptions(wsId: string, agentId: string) {
  return infiniteQueryOptions({
    queryKey: contextCapabilityKeys.scenes(wsId, agentId),
    queryFn: ({ pageParam }) =>
      api.listAgentScenes(wsId, agentId, {
        limit: AGENT_SCENES_PAGE_SIZE,
        offset: pageParam,
      }),
    initialPageParam: 0,
    getNextPageParam: (lastPage, _pages, lastOffset) =>
      lastPage.hasMore && lastPage.scenes.length > 0
        ? lastOffset + lastPage.scenes.length
        : undefined,
    enabled: Boolean(wsId && agentId),
    staleTime: 15_000,
  });
}

export function agentSceneOptions(wsId: string, agentId: string, sceneKey: string) {
  return queryOptions({
    queryKey: contextCapabilityKeys.scene(wsId, agentId, sceneKey),
    queryFn: () => api.getAgentScene(wsId, agentId, sceneKey),
    enabled: Boolean(wsId && agentId && sceneKey),
    staleTime: 15_000,
    // A scene account connected in the system browser (desktop), by a group
    // member or by the person on their phone shows when the admin comes back.
    refetchOnWindowFocus: "always",
  });
}

/** Every official app with its status for this agent (admin view). */
export function agentConnectedAppsOptions(wsId: string, agentId: string) {
  return queryOptions({
    queryKey: contextCapabilityKeys.connectedApps(wsId, agentId),
    queryFn: () => api.listAgentConnectedApps(wsId, agentId),
    enabled: Boolean(wsId && agentId),
    // A provider sign-in (in the system browser on desktop) or a group
    // connecting its account happens in another tab or on a phone; coming
    // back shows the new state. "always": the shared staleTime is Infinity,
    // under which `true` never refetches.
    refetchOnWindowFocus: "always",
  });
}

/** One official app's configuration page: status, usage and tools. */
export function agentConnectedAppOptions(wsId: string, agentId: string, slug: string) {
  return queryOptions({
    queryKey: contextCapabilityKeys.connectedApp(wsId, agentId, slug),
    queryFn: () => api.getAgentConnectedApp(wsId, agentId, slug),
    enabled: Boolean(wsId && agentId && slug),
    refetchOnWindowFocus: "always",
  });
}
