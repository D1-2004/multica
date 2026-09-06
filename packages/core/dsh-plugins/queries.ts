import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";
import type { Agent } from "../types";
import type { DshPluginBinding } from "./types";

/**
 * Query keys for the DSH plugin surfaces.
 *
 * Workspace-scoped keys carry wsId so a workspace switch drops the cache. The
 * community catalog is public data and is not workspace-scoped, but its cache
 * still hangs off the workspace tree so one broad invalidation covers a
 * refresh triggered from any workspace's page.
 */
export const dshPluginKeys = {
  all: (wsId: string) => ["workspaces", wsId, "dsh-plugins"] as const,
  list: (wsId: string) => ["workspaces", wsId, "dsh-plugins", "list"] as const,
  detail: (wsId: string, id: string) =>
    ["workspaces", wsId, "dsh-plugins", "detail", id] as const,
  bindings: (wsId: string) =>
    ["workspaces", wsId, "dsh-plugins", "bindings"] as const,
  agent: (wsId: string, agentId: string) =>
    ["workspaces", wsId, "dsh-plugins", "agent", agentId] as const,
  catalog: (
    wsId: string,
    params: { query: string; category: string; offset: number },
  ) =>
    [
      "workspaces",
      wsId,
      "dsh-plugins",
      "catalog",
      params.query,
      params.category,
      params.offset,
    ] as const,
  files: (wsId: string, id: string) =>
    ["workspaces", wsId, "dsh-plugins", "files", id] as const,
  file: (wsId: string, id: string, path: string) =>
    ["workspaces", wsId, "dsh-plugins", "file", id, path] as const,
  categories: (wsId: string) =>
    ["workspaces", wsId, "dsh-plugins", "categories"] as const,
};

export function dshPluginListOptions(wsId: string) {
  return queryOptions({
    queryKey: dshPluginKeys.list(wsId),
    queryFn: () => api.listDshPlugins(),
    enabled: !!wsId,
  });
}

export function dshPluginDetailOptions(wsId: string, pluginId: string) {
  return queryOptions({
    queryKey: dshPluginKeys.detail(wsId, pluginId),
    queryFn: () => api.getDshPlugin(pluginId),
    enabled: !!wsId && !!pluginId,
  });
}

export function dshPluginBindingsOptions(wsId: string) {
  return queryOptions({
    queryKey: dshPluginKeys.bindings(wsId),
    queryFn: () => api.listDshPluginBindings(),
    enabled: !!wsId,
  });
}

export function agentDshPluginsOptions(wsId: string, agentId: string) {
  return queryOptions({
    queryKey: dshPluginKeys.agent(wsId, agentId),
    queryFn: () => api.listAgentDshPlugins(agentId),
    enabled: !!wsId && !!agentId,
  });
}

export function dshPluginCatalogOptions(
  wsId: string,
  params: { query: string; category: string; offset: number; limit: number },
) {
  return queryOptions({
    queryKey: dshPluginKeys.catalog(wsId, params),
    queryFn: () =>
      api.browseDshPluginCatalog({
        query: params.query,
        category: params.category,
        offset: params.offset,
        limit: params.limit,
      }),
    enabled: !!wsId,
    // The catalog is a cached public index that only changes when someone
    // refreshes it, so refetching on every focus is pure noise.
    staleTime: 5 * 60 * 1000,
  });
}

export function dshPluginCategoriesOptions(wsId: string) {
  return queryOptions({
    queryKey: dshPluginKeys.categories(wsId),
    queryFn: () => api.listDshPluginCatalogCategories(),
    enabled: !!wsId,
    staleTime: 5 * 60 * 1000,
  });
}

/**
 * Builds a `Map<pluginId, Agent[]>` from the binding list and the cached agent
 * list, so the "used by" column costs no extra request.
 *
 * A plain helper rather than a `select`, for the same reason skill assignments
 * are: a select would return a fresh Map on every subscription tick and cascade
 * re-renders. Callers wrap this in `useMemo`.
 */
export function selectDshPluginAssignments(
  bindings: DshPluginBinding[] | undefined,
  agents: Agent[] | undefined,
): Map<string, Agent[]> {
  const map = new Map<string, Agent[]>();
  if (!bindings || !agents) return map;
  const byId = new Map(agents.filter((a) => !a.archived_at).map((a) => [a.id, a]));
  for (const binding of bindings) {
    const agent = byId.get(binding.agentId);
    if (!agent) continue;
    const existing = map.get(binding.pluginId);
    if (existing) existing.push(agent);
    else map.set(binding.pluginId, [agent]);
  }
  return map;
}

export function dshPluginFilesOptions(wsId: string, pluginId: string) {
  return queryOptions({
    queryKey: dshPluginKeys.files(wsId, pluginId),
    queryFn: () => api.listDshPluginFiles(pluginId),
    enabled: !!wsId && !!pluginId,
    // A stored package is immutable for the life of the row, so there is
    // nothing to refetch until the plugin itself changes.
    staleTime: 10 * 60 * 1000,
  });
}

export function dshPluginFileOptions(wsId: string, pluginId: string, path: string) {
  return queryOptions({
    queryKey: dshPluginKeys.file(wsId, pluginId, path),
    queryFn: () => api.getDshPluginFile(pluginId, path),
    enabled: !!wsId && !!pluginId && !!path,
    staleTime: 10 * 60 * 1000,
  });
}
