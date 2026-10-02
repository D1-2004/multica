import { infiniteQueryOptions, queryOptions } from "@tanstack/react-query";
import { api } from "../api";
import type { ContextNodeRef, SceneRoutinesTarget } from "../types/context-capability";

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
  /** The agent's detail per tenant (the enterprise and personal levels and
   * the scene list live there). */
  details: (agentId: string) =>
    [...contextConfigKeys.agent(agentId), "detail"] as const,
  detail: (agentId: string, orgId: string) =>
    [...contextConfigKeys.details(agentId), orgId] as const,
  scenes: (agentId: string) =>
    [...contextConfigKeys.agent(agentId), "scenes"] as const,
  scene: (agentId: string, sceneId: string) =>
    [...contextConfigKeys.scenes(agentId), sceneId] as const,
  /** A scene's routines (例行任务). */
  sceneRoutines: (agentId: string, sceneId: string) =>
    [...contextConfigKeys.scene(agentId, sceneId), "routines"] as const,
};

/**
 * Admin keys (agent detail → offer sections and the 场域 section),
 * workspace-scoped. Everything nests under the agent, so invalidating the
 * agent key (e.g. after an offer change) also refreshes the tenants, their
 * groups and people, and every Context Builder node.
 */
export const contextCapabilityKeys = {
  all: (wsId: string) => ["workspaces", wsId, "context-capabilities"] as const,
  agent: (wsId: string, agentId: string) =>
    [...contextCapabilityKeys.all(wsId), agentId] as const,
  /** The agent's tenants and unassigned orgs. */
  tenants: (wsId: string, agentId: string) =>
    [...contextCapabilityKeys.agent(wsId, agentId), "tenants"] as const,
  tenant: (wsId: string, agentId: string, orgId: string) =>
    [...contextCapabilityKeys.tenants(wsId, agentId), orgId] as const,
  tenantGroups: (wsId: string, agentId: string, orgId: string) =>
    [...contextCapabilityKeys.tenant(wsId, agentId, orgId), "groups"] as const,
  tenantPersons: (wsId: string, agentId: string, orgId: string) =>
    [...contextCapabilityKeys.tenant(wsId, agentId, orgId), "persons"] as const,
  /** Every Context Builder node of the agent. A write at one level changes
   * the effective preview of the levels below it. */
  contextNodes: (wsId: string, agentId: string) =>
    [...contextCapabilityKeys.agent(wsId, agentId), "context"] as const,
  contextNode: (wsId: string, agentId: string, node: ContextNodeRef) =>
    [
      ...contextCapabilityKeys.contextNodes(wsId, agentId),
      node.orgId,
      node.scopeType,
      node.scopeKey,
    ] as const,
  /** A scene node's routines (例行任务), under the node. */
  contextNodeRoutines: (wsId: string, agentId: string, node: ContextNodeRef) =>
    [...contextCapabilityKeys.contextNode(wsId, agentId, node), "routines"] as const,
  /** Official apps with this agent's status (连接应用). Nested under the
   * agent, so offer changes refresh them too. */
  connectedApps: (wsId: string, agentId: string) =>
    [...contextCapabilityKeys.agent(wsId, agentId), "connected-apps"] as const,
  connectedApp: (wsId: string, agentId: string, slug: string) =>
    [...contextCapabilityKeys.connectedApps(wsId, agentId), slug] as const,
};

/** Page size of a tenant's group list. */
export const AGENT_SCENES_PAGE_SIZE = 50;

export function contextConfigAgentsOptions() {
  return queryOptions({
    queryKey: contextConfigKeys.agents(),
    queryFn: () => api.listContextConfigAgents(),
  });
}

/** The agent's detail in one tenant; "" lets the server choose. */
export function contextConfigAgentOptions(agentId: string, orgId = "") {
  return queryOptions({
    queryKey: contextConfigKeys.detail(agentId, orgId),
    queryFn: () => api.getContextConfigAgent(agentId, orgId),
    enabled: Boolean(agentId),
  });
}

/** One scene of the configure page, by its scene_id. `orgId` is the
 * scene's tenant ("" for the agent's own org); a scene_id names one scene of
 * one org, so it is not part of the key. */
export function contextConfigSceneOptions(agentId: string, sceneId: string, orgId = "") {
  return queryOptions({
    queryKey: contextConfigKeys.scene(agentId, sceneId),
    queryFn: () => api.getContextConfigScene(agentId, sceneId, orgId),
    enabled: Boolean(agentId && sceneId),
  });
}

export function agentContextCapabilitiesOptions(wsId: string, agentId: string) {
  return queryOptions({
    queryKey: contextCapabilityKeys.agent(wsId, agentId),
    queryFn: () => api.getAgentContextCapabilities(wsId, agentId),
    enabled: Boolean(wsId && agentId),
  });
}

/** The agent's tenants (企业) and the orgs seen without one. */
export function agentTenantsOptions(wsId: string, agentId: string) {
  return queryOptions({
    queryKey: contextCapabilityKeys.tenants(wsId, agentId),
    queryFn: () => api.listAgentTenants(wsId, agentId),
    enabled: Boolean(wsId && agentId),
    staleTime: 15_000,
  });
}

/** A tenant's scenes (group chats and 1:1 chats), newest activity first,
 * paged by offset. */
export function agentTenantGroupsOptions(wsId: string, agentId: string, orgId: string) {
  return infiniteQueryOptions({
    queryKey: contextCapabilityKeys.tenantGroups(wsId, agentId, orgId),
    queryFn: ({ pageParam }) =>
      api.listAgentTenantGroups(wsId, agentId, orgId, {
        limit: AGENT_SCENES_PAGE_SIZE,
        offset: pageParam,
      }),
    initialPageParam: 0,
    getNextPageParam: (lastPage, _pages, lastOffset) =>
      lastPage.hasMore && lastPage.scenes.length > 0
        ? lastOffset + lastPage.scenes.length
        : undefined,
    enabled: Boolean(wsId && agentId && orgId),
    staleTime: 15_000,
  });
}

/** People known under a tenant. */
export function agentTenantPersonsOptions(wsId: string, agentId: string, orgId: string) {
  return queryOptions({
    queryKey: contextCapabilityKeys.tenantPersons(wsId, agentId, orgId),
    queryFn: () => api.listAgentTenantPersons(wsId, agentId, orgId),
    enabled: Boolean(wsId && agentId && orgId),
    staleTime: 15_000,
  });
}

/** One level's Context Builder. */
export function contextNodeOptions(wsId: string, agentId: string, node: ContextNodeRef) {
  return queryOptions({
    queryKey: contextCapabilityKeys.contextNode(wsId, agentId, node),
    queryFn: () => api.getContextNode(wsId, agentId, node),
    enabled: Boolean(wsId && agentId && node.orgId && node.scopeKey),
    // Refetch whenever the builder opens: its 生效预览 includes the agent's
    // own skills and MCP servers, which other tabs change without touching
    // these keys.
    staleTime: 0,
    // An account connected in the system browser (desktop), by a group
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

/** The query key of a scene's routines on the configure page or an admin
 * scene node. */
export function sceneRoutinesKey(target: SceneRoutinesTarget) {
  return target.kind === "node"
    ? contextCapabilityKeys.contextNodeRoutines(target.wsId, target.agentId, target.node)
    : contextConfigKeys.sceneRoutines(target.agentId, target.sceneId);
}

function sceneRoutinesTargetReady(target: SceneRoutinesTarget): boolean {
  return target.kind === "node"
    ? Boolean(target.wsId && target.agentId && target.node.orgId && target.node.scopeKey)
    : Boolean(target.agentId && target.sceneId);
}

/** A routine's run history, under its scene's routines key so every
 * routine write refreshes it too. */
export function sceneRoutineRunsOptions(target: SceneRoutinesTarget, routineId: string) {
  return queryOptions({
    queryKey: [...sceneRoutinesKey(target), routineId, "runs"] as const,
    queryFn: () => api.listSceneRoutineRuns(target, routineId),
    enabled: sceneRoutinesTargetReady(target) && Boolean(routineId),
    staleTime: 15_000,
    refetchInterval: 60_000,
  });
}

/** A scene's routines with their next runs and last result. Refetched on
 * focus and every minute while shown, so a run started elsewhere (cron,
 * webhook, the agent) shows its result. */
export function sceneRoutinesOptions(target: SceneRoutinesTarget) {
  return queryOptions({
    queryKey: sceneRoutinesKey(target),
    queryFn: () => api.listSceneRoutines(target),
    enabled: sceneRoutinesTargetReady(target),
    staleTime: 15_000,
    refetchInterval: 60_000,
  });
}
