import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
export type { DSHHomeStatus } from "../api/dsh-home-schema";

export const dshHomeKeys = {
  detail: (workspaceId: string, agentId: string) =>
    ["workspace", workspaceId, "agents", agentId, "dsh-home"] as const,
};

export function dshHomeOptions(workspaceId: string, agentId: string) {
  return queryOptions({
    queryKey: dshHomeKeys.detail(workspaceId, agentId),
    queryFn: ({ signal }) => api.getDSHHome(agentId, signal),
    enabled: !!workspaceId && !!agentId,
    staleTime: 0,
    retry: false,
    refetchInterval: (query) => {
      const status = query.state.data;
      return !query.state.error && status?.provisioned === false &&
        (status.state === "planned" || status.state === "creating") ? 5000 : false;
    },
  });
}

export function useEnsureDSHHome(workspaceId: string, agentId: string) {
  const client = useQueryClient();
  const queryKey = dshHomeKeys.detail(workspaceId, agentId);
  return useMutation({
    mutationKey: queryKey,
    mutationFn: () => api.ensureDSHHome(agentId),
    retry: false,
    onSuccess: async (status) => {
      await client.cancelQueries({ queryKey });
      client.setQueryData(queryKey, status);
    },
    // A failed response does not prove that cloud creation failed. Reconcile
    // through the read endpoint before the user advances the same intent.
    onSettled: () => client.invalidateQueries({ queryKey }),
  });
}
