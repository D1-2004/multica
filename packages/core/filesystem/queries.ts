import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

export const filesystemKeys = {
  roots: (workspaceId: string) => ["workspace", workspaceId, "filesystem", "roots"] as const,
};

export function filesystemRootsOptions(workspaceId: string) {
  return queryOptions({
    queryKey: filesystemKeys.roots(workspaceId),
    queryFn: ({ signal }) => api.listFilesystemRoots(signal),
    enabled: !!workspaceId,
    staleTime: 5000,
  });
}
