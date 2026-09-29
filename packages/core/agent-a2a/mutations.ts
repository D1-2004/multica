import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import type {
  CreateAgentA2AClientRequest,
  CreateAgentA2ACredentialRequest,
  UpdateAgentA2AClientRequest,
  UpdateAgentA2AConfigRequest,
  UpdateAgentA2AOperatorForwardRequest,
  UpdateAgentA2AOperatorIdentityRequest,
} from "../types";
import { agentA2AKeys } from "./queries";

type SecretReceiver = (token: string) => void;

export function useUpdateAgentA2AConfig(wsId: string, agentId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (data: UpdateAgentA2AConfigRequest) =>
      api.updateAgentA2AConfig(agentId, data),
    onSuccess: () => queryClient.invalidateQueries({
      queryKey: agentA2AKeys.all(wsId, agentId),
    }),
  });
}

export function useCreateAgentA2AClient(wsId: string, agentId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (data: CreateAgentA2AClientRequest) =>
      api.createAgentA2AClient(agentId, data),
    onSuccess: () => queryClient.invalidateQueries({
      queryKey: agentA2AKeys.all(wsId, agentId),
    }),
  });
}

export function useUpdateAgentA2AClient(wsId: string, agentId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({
      clientId,
      data,
    }: {
      clientId: string;
      data: UpdateAgentA2AClientRequest;
    }) => api.updateAgentA2AClient(agentId, clientId, data),
    onSuccess: () => queryClient.invalidateQueries({
      queryKey: agentA2AKeys.all(wsId, agentId),
    }),
  });
}

export function useCreateAgentA2ACredential(wsId: string, agentId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async ({
      clientId,
      data,
      onToken,
    }: {
      clientId: string;
      data: CreateAgentA2ACredentialRequest;
      onToken: SecretReceiver;
    }) => {
      const result = await api.createAgentA2ACredential(agentId, clientId, data);
      if (!result.token) throw new Error("Invalid A2A credential response");
      onToken(result.token);
      // Only the one-time dialog owns the secret after this stack frame. The
      // value retained as TanStack Mutation data is credential metadata only.
      return result.credential;
    },
    onSuccess: () => queryClient.invalidateQueries({
      queryKey: agentA2AKeys.all(wsId, agentId),
    }),
  });
}

export function useDeleteAgentA2ACredential(wsId: string, agentId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({
      clientId,
      credentialId,
    }: {
      clientId: string;
      credentialId: string;
    }) => api.deleteAgentA2ACredential(agentId, clientId, credentialId),
    onSuccess: () => queryClient.invalidateQueries({
      queryKey: agentA2AKeys.all(wsId, agentId),
    }),
  });
}

export function useUpdateAgentA2AOperatorIdentity(wsId: string, agentId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (data: UpdateAgentA2AOperatorIdentityRequest) =>
      api.updateAgentA2AOperatorIdentity(agentId, data),
    onSuccess: () => queryClient.invalidateQueries({
      queryKey: agentA2AKeys.all(wsId, agentId),
    }),
  });
}

export function useDeleteAgentA2AOperatorIdentity(wsId: string, agentId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () => api.deleteAgentA2AOperatorIdentity(agentId),
    onSuccess: () => queryClient.invalidateQueries({
      queryKey: agentA2AKeys.all(wsId, agentId),
    }),
  });
}

// The forward token is write-only: it goes to the server and is never kept in
// query state.
export function useUpdateAgentA2AOperatorForward(wsId: string, agentId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (data: UpdateAgentA2AOperatorForwardRequest) =>
      api.updateAgentA2AOperatorForward(agentId, data),
    onSuccess: () => queryClient.invalidateQueries({
      queryKey: agentA2AKeys.all(wsId, agentId),
    }),
  });
}

export function useDeleteAgentA2AOperatorForward(wsId: string, agentId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () => api.deleteAgentA2AOperatorForward(agentId),
    onSuccess: () => queryClient.invalidateQueries({
      queryKey: agentA2AKeys.all(wsId, agentId),
    }),
  });
}
