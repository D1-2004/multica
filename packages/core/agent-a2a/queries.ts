import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

export const agentA2AKeys = {
  all: (wsId: string, agentId: string) =>
    ["workspaces", wsId, "agents", agentId, "a2a"] as const,
  config: (wsId: string, agentId: string) =>
    [...agentA2AKeys.all(wsId, agentId), "config"] as const,
};

export function agentA2AConfigOptions(wsId: string, agentId: string) {
  return queryOptions({
    queryKey: agentA2AKeys.config(wsId, agentId),
    queryFn: () => api.getAgentA2AConfig(agentId),
    enabled: Boolean(wsId && agentId),
  });
}
