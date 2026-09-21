import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

export const filesystemKeys = {
  roots: (workspaceId: string) => ["workspace", workspaceId, "filesystem", "roots"] as const,
  entries: (workspaceId: string, root: string, path: string, offset: number) =>
    ["workspace", workspaceId, "filesystem", "entries", root, path, offset] as const,
  grants: (workspaceId: string) => ["workspace", workspaceId, "filesystem", "grants"] as const,
};

export function filesystemRootsOptions(workspaceId: string) {
  return queryOptions({
    queryKey: filesystemKeys.roots(workspaceId),
    queryFn: ({ signal }) => api.listFilesystemRoots(signal),
    enabled: !!workspaceId,
    staleTime: 5000,
  });
}

export function filesystemGrantsOptions(workspaceId: string) {
  return queryOptions({
    queryKey: filesystemKeys.grants(workspaceId),
    queryFn: ({ signal }) => api.listFilesystemGrants(signal),
    enabled: !!workspaceId,
    staleTime: 5000,
  });
}

export function filesystemEntriesOptions(
  workspaceId: string,
  root: string,
  path: string,
  offset = 0,
  recursive = false,
) {
  return queryOptions({
    queryKey: filesystemKeys.entries(workspaceId, root, recursive ? "recursive" : path, offset),
    queryFn: ({ signal }) =>
      api.listFilesystemEntries({
        root,
        path: path === "." ? "" : path,
        offset,
        recursive,
      }, signal),
    enabled: !!workspaceId && !!root,
    staleTime: 2000,
  });
}
