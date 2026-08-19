import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

export const runnerBindingKeys = {
  all: (workspaceId: string) => ["runner-bindings", workspaceId] as const,
  agent: (workspaceId: string, agentId: string) =>
    [...runnerBindingKeys.all(workspaceId), "agent", agentId] as const,
};

export function agentRunnerBindingsOptions(
  workspaceId: string,
  agentId: string,
) {
  return queryOptions({
    queryKey: runnerBindingKeys.agent(workspaceId, agentId),
    queryFn: () => api.listAgentRunnerBindings(agentId),
    enabled: !!workspaceId && !!agentId,
    refetchInterval: 5_000,
    refetchOnWindowFocus: "always" as const,
  });
}
