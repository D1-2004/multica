import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import type { ConnectGitRepositoryRequest, GitAgentPreviewRequest } from "../types/git-repo";

export const gitRepoKeys = { all: (wsId: string) => ["git-repo", wsId] as const };
export const gitConnectionsOptions = (wsId: string) => queryOptions({
  queryKey: [...gitRepoKeys.all(wsId), "connections"], queryFn: () => api.listGitConnections(wsId), enabled: !!wsId,
});
export const gitAgentBranchesOptions = (wsId: string, connectionId: string, repository: string) => queryOptions({
  queryKey: [...gitRepoKeys.all(wsId), "refs", connectionId, repository],
  queryFn: () => api.listGitAgentBranches(wsId, connectionId, repository), enabled: !!wsId && !!repository,
});
export function usePreviewGitAgent(wsId: string) {
  return useMutation({ mutationFn: async (request: GitAgentPreviewRequest) => {
    const result = await api.previewGitAgent(wsId, request);
    if (!result.preview_id || !result.resolved_sha) throw new Error("Invalid Git preview response");
    return result;
  } });
}
export function useConnectGitRepository(wsId: string) {
  const client = useQueryClient();
  return useMutation({ mutationFn: (request: ConnectGitRepositoryRequest) => api.connectGitRepository(wsId, request),
    onSuccess: () => client.invalidateQueries({ queryKey: gitRepoKeys.all(wsId) }), gcTime: 0 });
}
export function useDeleteGitConnection(wsId: string) {
  const client = useQueryClient();
  return useMutation({ mutationFn: (id: string) => api.deleteGitConnection(wsId, id),
    onSuccess: async () => { await client.invalidateQueries({ queryKey: gitRepoKeys.all(wsId) }); await client.invalidateQueries({ queryKey: ["github", wsId] }); } });
}

export const gitRepositoryOptions = (wsId: string, repository: string) => queryOptions({
  queryKey: [...gitRepoKeys.all(wsId), "repository", repository], queryFn: () => api.resolveGitRepository(wsId, repository), enabled: !!wsId && !!repository, retry: false,
});
