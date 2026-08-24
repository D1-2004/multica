import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { runnerBindingKeys } from "./queries";
import type { AccountRunnerBindingTarget } from "./types";

async function invalidateAccountAndAgentBindingQueries(
  queryClient: ReturnType<typeof useQueryClient>,
  userId: string,
  target: AccountRunnerBindingTarget,
) {
  await Promise.all([
    queryClient.invalidateQueries({
      queryKey: runnerBindingKeys.account(userId),
    }),
    queryClient.invalidateQueries({
      queryKey: runnerBindingKeys.agent(target.workspaceId, target.agentId),
    }),
  ]);
}

export function useCreateAgentRunnerPairing(agentId: string) {
  return useMutation({
    mutationFn: () => api.createAgentRunnerPairing(agentId),
  });
}

export function useRevokeAgentRunnerBinding(
  workspaceId: string,
  agentId: string,
) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (bindingId: string) =>
      api.revokeAgentRunnerBinding(agentId, bindingId),
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({
          queryKey: runnerBindingKeys.agent(workspaceId, agentId),
        }),
        queryClient.invalidateQueries({
          queryKey: runnerBindingKeys.accountAll(),
        }),
      ]);
    },
  });
}

export function useDisconnectAgentRunnerBinding(
  workspaceId: string,
  agentId: string,
) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (bindingId: string) =>
      api.disconnectAgentRunnerBinding(agentId, bindingId),
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({
          queryKey: runnerBindingKeys.agent(workspaceId, agentId),
        }),
        queryClient.invalidateQueries({
          queryKey: runnerBindingKeys.accountAll(),
        }),
      ]);
    },
  });
}

export function useCreateAgentRunnerReconnectCommand(agentId: string) {
  return useMutation({
    mutationFn: (bindingId: string) =>
      api.createAgentRunnerReconnectCommand(agentId, bindingId),
  });
}

export function useDisconnectAccountRunnerBinding(userId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (target: AccountRunnerBindingTarget) =>
      api.disconnectAccountRunnerBinding(target.bindingId),
    onSuccess: async (_result, target) => {
      await invalidateAccountAndAgentBindingQueries(queryClient, userId, target);
    },
  });
}

export function useCreateAccountRunnerReconnectCommand(userId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (target: AccountRunnerBindingTarget) =>
      api.createAccountRunnerReconnectCommand(target.bindingId),
    onSuccess: async (_result, target) => {
      await invalidateAccountAndAgentBindingQueries(queryClient, userId, target);
    },
  });
}

export function useRevokeAccountRunnerBinding(userId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (target: AccountRunnerBindingTarget) =>
      api.revokeAccountRunnerBinding(target.bindingId),
    onSuccess: async (_result, target) => {
      await invalidateAccountAndAgentBindingQueries(queryClient, userId, target);
    },
  });
}
