"use client";

import {
  useMutation,
  useQueryClient,
  queryOptions,
} from "@tanstack/react-query";
import { api } from "../api";
import type { CreateWorkspaceMCPConnection } from "../api/workspace-mcp-schema";

export const workspaceMCPKeys = (wsId: string) =>
  ["workspace-mcp", wsId] as const;
export function workspaceMCPConnectionsOptions(wsId: string) {
  return queryOptions({
    queryKey: workspaceMCPKeys(wsId),
    queryFn: () => api.listWorkspaceMCPConnections(wsId),
  });
}
export function useCreateWorkspaceMCPConnection(wsId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (data: CreateWorkspaceMCPConnection) =>
      api.createWorkspaceMCPConnection(wsId, data),
    gcTime: 0,
    onSuccess: () =>
      queryClient.invalidateQueries({ queryKey: workspaceMCPKeys(wsId) }),
  });
}
export function useRevokeWorkspaceMCPConnection(wsId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.revokeWorkspaceMCPConnection(wsId, id),
    onSuccess: () =>
      queryClient.invalidateQueries({ queryKey: workspaceMCPKeys(wsId) }),
  });
}
