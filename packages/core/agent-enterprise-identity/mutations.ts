import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { agentEnterpriseIdentityKeys } from "./queries";

export function useBeginAgentEnterpriseIdentityBinding(wsId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({
      agentId,
      employeeId,
      redirectPath,
    }: {
      agentId: string;
      employeeId: string;
      redirectPath: string;
    }) =>
      api.beginAgentEnterpriseIdentityBinding(
        wsId,
        agentId,
        employeeId,
        redirectPath,
      ),
    onSettled: (_data, _error, variables) =>
      queryClient.invalidateQueries({
        queryKey: agentEnterpriseIdentityKeys.status(wsId, variables.agentId),
      }),
  });
}

export function useTestAgentEnterpriseIdentity(wsId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (agentId: string) =>
      api.testAgentEnterpriseIdentity(wsId, agentId),
    onSettled: (_data, _error, agentId) =>
      queryClient.invalidateQueries({
        queryKey: agentEnterpriseIdentityKeys.status(wsId, agentId),
      }),
  });
}

export function useRevokeAgentEnterpriseIdentity(wsId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (agentId: string) =>
      api.revokeAgentEnterpriseIdentity(wsId, agentId),
    onSuccess: async (_data, agentId) => {
      await queryClient.invalidateQueries({
        queryKey: agentEnterpriseIdentityKeys.status(wsId, agentId),
      });
    },
  });
}
