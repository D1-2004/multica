import { useQuery } from "@tanstack/react-query";
import { api } from "../api";

export function useASBRegions(workspaceId: string, runtimeId: string) {
  return useQuery({
    queryKey: ["runtimes", workspaceId, runtimeId, "asb-regions"],
    queryFn: () => api.getASBRegions(runtimeId),
    enabled: Boolean(workspaceId && runtimeId),
    staleTime: 30_000,
  });
}
