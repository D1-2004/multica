import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

/**
 * Workspace connector library keys. The list key is the prefix of every
 * other key, so invalidating the list also refreshes the member-visible
 * "available" view (which views read under
 * `[...all(wsId), "available"]`).
 */
export const internalConnectorKeys = {
  all: (wsId: string) => ["workspaces", wsId, "internal-connectors"] as const,
  list: (wsId: string) => internalConnectorKeys.all(wsId),
  available: (wsId: string) => [...internalConnectorKeys.all(wsId), "available"] as const,
};

export function internalConnectorListOptions(wsId: string) {
  return queryOptions({
    queryKey: internalConnectorKeys.list(wsId),
    queryFn: () => api.listInternalConnectors(wsId),
    enabled: Boolean(wsId),
  });
}

/** Member-visible view: every enabled connector granted to an agent, one row
 * per (connector, agent) pair, without URLs or credential state. */
export function availableInternalConnectorsOptions(wsId: string) {
  return queryOptions({
    queryKey: internalConnectorKeys.available(wsId),
    queryFn: () => api.listAvailableInternalConnectors(wsId),
    enabled: Boolean(wsId),
  });
}
