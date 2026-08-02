import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import type {
  CreateWorkspaceAccessTokenRequest,
  RegenerateWorkspaceAccessTokenRequest,
  UpdateWorkspaceAccessTokenRequest,
} from "../types";
import { workspaceAccessKeys } from "./queries";

type SecretReceiver = (token: string) => void;

function consumeOneTimeToken<T extends { token: string }>(result: T, onToken: SecretReceiver): Omit<T, "token"> {
  if (!result.token) throw new Error("Invalid DTA access token response");
  const { token, ...metadata } = result;
  onToken(token);
  // Only the one-time dialog owns the secret after this stack frame returns;
  // TanStack Mutation data receives metadata without the token.
  return metadata;
}

export function useCreateWorkspaceAccessToken(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ data, onToken }: { data: CreateWorkspaceAccessTokenRequest; onToken: SecretReceiver }) =>
      consumeOneTimeToken(await api.createWorkspaceAccessToken(wsId, data), onToken),
    onSuccess: () => qc.invalidateQueries({ queryKey: workspaceAccessKeys.tokens(wsId) }),
  });
}

export function useUpdateWorkspaceAccessToken(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ tokenId, data }: { tokenId: string; data: UpdateWorkspaceAccessTokenRequest }) =>
      api.updateWorkspaceAccessToken(wsId, tokenId, data),
    onSuccess: () => qc.invalidateQueries({ queryKey: workspaceAccessKeys.tokens(wsId) }),
  });
}

export function useRegenerateWorkspaceAccessToken(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ tokenId, data, onToken }: { tokenId: string; data: RegenerateWorkspaceAccessTokenRequest; onToken: SecretReceiver }) =>
      consumeOneTimeToken(await api.regenerateWorkspaceAccessToken(wsId, tokenId, data), onToken),
    onSuccess: () => qc.invalidateQueries({ queryKey: workspaceAccessKeys.tokens(wsId) }),
  });
}

export function useRevokeWorkspaceAccessToken(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (tokenId: string) => api.revokeWorkspaceAccessToken(wsId, tokenId),
    onSuccess: () => qc.invalidateQueries({ queryKey: workspaceAccessKeys.tokens(wsId) }),
  });
}
