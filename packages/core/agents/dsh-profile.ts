import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";

export const dshProfileKeys = {
  detail: (workspaceId: string, agentId: string) =>
    ["workspace", workspaceId, "agents", agentId, "dsh-profile"] as const,
};

export function dshProfileOptions(workspaceId: string, agentId: string) {
  return queryOptions({
    queryKey: dshProfileKeys.detail(workspaceId, agentId),
    queryFn: ({ signal }) => api.getDSHProfile(workspaceId, agentId, signal),
    enabled: !!workspaceId && !!agentId,
    staleTime: 0,
    retry: false,
    refetchInterval: (query) => !query.state.error &&
      ["waiting_for_builds", "pending_host"].includes(query.state.data?.state ?? "") ? 5000 : false,
  });
}

export function usePrepareDSHProfile(workspaceId: string, agentId: string) {
  const client = useQueryClient();
  const queryKey = dshProfileKeys.detail(workspaceId, agentId);
  return useMutation({
    mutationKey: queryKey,
    mutationFn: () => api.prepareDSHProfile(workspaceId, agentId),
    retry: false,
    onSettled: () => client.invalidateQueries({ queryKey }),
  });
}

export function useRetryDSHProfileBuild(workspaceId: string, agentId: string) {
  const client = useQueryClient();
  const queryKey = dshProfileKeys.detail(workspaceId, agentId);
  return useMutation({
    mutationKey: queryKey,
    mutationFn: ({ revision, buildId }: { revision: string; buildId: string }) => api.retryDSHProfileBuild(workspaceId, agentId, revision, buildId),
    retry: false,
    onSettled: () => client.invalidateQueries({ queryKey }),
  });
}
