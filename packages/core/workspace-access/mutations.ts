import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import type {
  CreateWorkspaceAccessGrantRequest,
  CreateWorkspaceAccessTokenRequest,
  UpdateWorkspaceAccessGrantRequest,
} from "../types";
import { workspaceAccessKeys } from "./queries";

export function useCreateWorkspaceAccessGrant(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (data: CreateWorkspaceAccessGrantRequest) =>
      api.createWorkspaceAccessGrant(wsId, data),
    onSuccess: () => qc.invalidateQueries({ queryKey: workspaceAccessKeys.grants(wsId) }),
  });
}

export function useUpdateWorkspaceAccessGrant(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ grantId, data }: { grantId: string; data: UpdateWorkspaceAccessGrantRequest }) =>
      api.updateWorkspaceAccessGrant(wsId, grantId, data),
    onSuccess: () => qc.invalidateQueries({ queryKey: workspaceAccessKeys.grants(wsId) }),
  });
}

export function useSetWorkspaceAccessGrantEnabled(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ grantId, enabled }: { grantId: string; enabled: boolean }) =>
      api.setWorkspaceAccessGrantEnabled(wsId, grantId, enabled),
    onSuccess: () => qc.invalidateQueries({ queryKey: workspaceAccessKeys.grants(wsId) }),
  });
}

export function useCreateWorkspaceAccessToken(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ grantId, data, onToken }: { grantId: string; data: CreateWorkspaceAccessTokenRequest; onToken: (token: string) => void }) => {
      const result = await api.createWorkspaceAccessToken(wsId, grantId, data);
      if (!result.token) throw new Error("Invalid DTA access token response");
      const { token, ...metadata } = result;
      onToken(token);
      // Never leave the one-time secret in TanStack Mutation data. Only the
      // secret dialog owns it after this stack frame returns.
      return metadata;
    },
    onSuccess: (_result, variables) =>
      qc.invalidateQueries({ queryKey: workspaceAccessKeys.tokens(wsId, variables.grantId) }),
  });
}

export function useUpdateWorkspaceAccessTokenExpiry(wsId: string, grantId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ tokenId, expiresAt }: { tokenId: string; expiresAt: string | null }) =>
      api.updateWorkspaceAccessTokenExpiry(wsId, grantId, tokenId, expiresAt),
    onSuccess: () => qc.invalidateQueries({ queryKey: workspaceAccessKeys.tokens(wsId, grantId) }),
  });
}

export function useRevokeWorkspaceAccessToken(wsId: string, grantId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (tokenId: string) => api.revokeWorkspaceAccessToken(wsId, grantId, tokenId),
    onSuccess: () => qc.invalidateQueries({ queryKey: workspaceAccessKeys.tokens(wsId, grantId) }),
  });
}
