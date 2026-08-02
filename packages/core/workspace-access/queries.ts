import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

export const workspaceAccessKeys = {
  all: (wsId: string) => ["workspaces", wsId, "workspace-access"] as const,
  grants: (wsId: string) => [...workspaceAccessKeys.all(wsId), "grants"] as const,
  tokens: (wsId: string, grantId: string) =>
    [...workspaceAccessKeys.grants(wsId), grantId, "tokens"] as const,
};

export function workspaceAccessGrantListOptions(wsId: string) {
  return queryOptions({
    queryKey: workspaceAccessKeys.grants(wsId),
    queryFn: () => api.listWorkspaceAccessGrants(wsId),
    enabled: !!wsId,
  });
}

export function workspaceAccessTokenListOptions(wsId: string, grantId: string) {
  return queryOptions({
    queryKey: workspaceAccessKeys.tokens(wsId, grantId),
    queryFn: () => api.listWorkspaceAccessTokens(wsId, grantId),
    enabled: !!wsId && !!grantId,
  });
}
