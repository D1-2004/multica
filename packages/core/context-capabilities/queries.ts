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
  });
}
