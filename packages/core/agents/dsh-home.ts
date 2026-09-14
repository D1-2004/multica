import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { dshProfileKeys } from "./dsh-profile";
import type { DSHNativeEntry } from "../api/dsh-native-schema";
export type { DSHNativeEntry } from "../api/dsh-native-schema";
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

export function useDSHNativeEntry(workspaceId: string, agentId: string) {
  const client = useQueryClient();
  return useMutation({
    mutationKey: [...dshHomeKeys.detail(workspaceId, agentId), "native-entry"],
    // A consumed or interrupted entry must never be automatically replayed.
    retry: false,
    gcTime: 0,
    mutationFn: async ({ onEntry }: { onEntry: (entry: DSHNativeEntry & { workspaceId: string; agentId: string }) => void }) => {
      const entry = await api.issueDSHNativeEntry(workspaceId, agentId);
      if (!entry || Date.parse(entry.expiresAt) <= Date.now()) {
        throw new Error("DSH native entry was not confirmed");
      }
      onEntry({ ...entry, workspaceId, agentId });
      // The component owns the short-lived URL; the mutation cache gets metadata only.
      return { accessId: entry.accessId, expiresAt: entry.expiresAt };
    },
    onSettled: () => Promise.all([
      client.invalidateQueries({ queryKey: dshHomeKeys.detail(workspaceId, agentId) }),
      client.invalidateQueries({ queryKey: dshProfileKeys.detail(workspaceId, agentId) }),
    ]),
  });
}
