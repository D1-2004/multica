import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

export const runnerBindingKeys = {
  all: (workspaceId: string) => ["runner-bindings", workspaceId] as const,
  accountAll: () => ["runner-bindings", "account"] as const,
  account: (userId: string) =>
    [...runnerBindingKeys.accountAll(), userId] as const,
  agent: (workspaceId: string, agentId: string) =>
    [...runnerBindingKeys.all(workspaceId), "agent", agentId] as const,
};

export function accountRunnerBindingsOptions(userId: string) {
  return queryOptions({
    queryKey: runnerBindingKeys.account(userId),
    queryFn: async () => {
      const inventory = await api.listAccountRunnerBindings();
      if (inventory === null) {
        throw new Error("Runner inventory response is invalid");
      }
      return inventory;
    },
    enabled: !!userId,
    refetchInterval: 5_000,
    refetchOnWindowFocus: "always" as const,
  });
}

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
