import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { runnerBindingKeys } from "./queries";

export function useCreateAgentRunnerPairing(agentId: string) {
  return useMutation({
    mutationFn: () => api.createAgentRunnerPairing(agentId),
  });
}

export function useRevokeAgentRunnerBinding(
  workspaceId: string,
  agentId: string,
) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (bindingId: string) =>
      api.revokeAgentRunnerBinding(agentId, bindingId),
    onSuccess: async () => {
      await queryClient.invalidateQueries({
        queryKey: runnerBindingKeys.agent(workspaceId, agentId),
      });
    },
  });
}
