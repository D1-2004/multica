import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";

export interface ASBNetworkPolicySettings {
  defaultAction: "deny";
  defaultTargets: string[];
  customTargets: string[];
  effectiveTargets: string[];
  available: boolean;
}

export const asbNetworkPolicyKey = (wsId: string, runtimeId: string) =>
  ["runtimes", wsId, runtimeId, "asb-network-policy"] as const;

export function useASBNetworkPolicy(wsId: string, runtimeId: string) {
  return useQuery({
    queryKey: asbNetworkPolicyKey(wsId, runtimeId),
    queryFn: () => api.getASBNetworkPolicy(runtimeId),
    enabled: Boolean(wsId && runtimeId),
  });
}

export function useUpdateASBNetworkPolicy(wsId: string, runtimeId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (customTargets: string[]) => api.updateASBNetworkPolicy(runtimeId, customTargets),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: asbNetworkPolicyKey(wsId, runtimeId) }),
  });
}
