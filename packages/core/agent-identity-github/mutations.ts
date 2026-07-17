import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { agentIdentityGithubKeys } from "./queries";

export function useBeginAgentIdentityGitHubOAuth(wsId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ agentId, returnUrl }: { agentId: string; returnUrl: string }) =>
      api.beginAgentIdentityGitHubOAuth(wsId, agentId, returnUrl),
    onSettled: (_data, _error, variables) =>
      queryClient.invalidateQueries({
        queryKey: agentIdentityGithubKeys.status(wsId, variables.agentId),
      }),
  });
}

export function useTestAgentIdentityGitHubConnection(wsId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({
      agentId,
      connectionId,
    }: {
      agentId: string;
      connectionId: string;
    }) => api.testAgentIdentityGitHubConnection(wsId, agentId, connectionId),
    onSettled: (_data, _error, variables) =>
      queryClient.invalidateQueries({
        queryKey: agentIdentityGithubKeys.status(wsId, variables.agentId),
      }),
  });
}
