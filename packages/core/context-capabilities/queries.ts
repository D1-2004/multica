import { queryOptions } from "@tanstack/react-query";
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

/** Admin keys (agent detail → Context capabilities tab), workspace-scoped. */
export const contextCapabilityKeys = {
  all: (wsId: string) => ["workspaces", wsId, "context-capabilities"] as const,
  agent: (wsId: string, agentId: string) =>
    [...contextCapabilityKeys.all(wsId), agentId] as const,
};

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
