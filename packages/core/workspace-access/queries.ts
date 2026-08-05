import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

export const workspaceAccessKeys = {
  all: (wsId: string) => ["workspaces", wsId, "workspace-access"] as const,
  tokens: (wsId: string) => [...workspaceAccessKeys.all(wsId), "tokens"] as const,
};

export function workspaceAccessTokenListOptions(wsId: string) {
  return queryOptions({
    queryKey: workspaceAccessKeys.tokens(wsId),
    queryFn: () => api.listWorkspaceAccessTokens(wsId),
    enabled: !!wsId,
  });
}
