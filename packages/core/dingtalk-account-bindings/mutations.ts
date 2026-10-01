import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { dingtalkAccountBindingKeys } from "./queries";
import type {
  BindDingTalkMessageRouteManuallyRequest,
  DingTalkBindingMode,
  DingTalkProcessingSurface,
} from "../types";

export function useBeginDingTalkAccountBinding(wsId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ agentId, bindingMode }: { agentId: string; bindingMode: DingTalkBindingMode }) =>
      api.beginDingTalkAccountBinding(wsId, agentId, bindingMode),
    onSettled: () =>
      queryClient.invalidateQueries({
        queryKey: dingtalkAccountBindingKeys.all(wsId),
      }),
  });
}

export function useDeleteDingTalkAccountBinding(wsId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ agentId, bindingMode }: { agentId: string; bindingMode: DingTalkBindingMode }) =>
      api.deleteDingTalkAccountBinding(wsId, agentId, bindingMode),
    onSuccess: () =>
      queryClient.invalidateQueries({
        queryKey: dingtalkAccountBindingKeys.all(wsId),
      }),
  });
}

export function useUpdateDingTalkAccountBindingSurface(wsId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({
      agentId,
      surfaceType,
    }: {
      agentId: string;
      surfaceType: DingTalkProcessingSurface;
    }) => api.updateDingTalkAccountBindingSurface(wsId, agentId, surfaceType),
    onSuccess: () =>
      queryClient.invalidateQueries({
        queryKey: dingtalkAccountBindingKeys.all(wsId),
      }),
  });
}

export function useReuseDingTalkIdentity(wsId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ agentId, sourceAgentId }: { agentId: string; sourceAgentId: string }) =>
      api.reuseDingTalkIdentity(wsId, agentId, sourceAgentId),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: dingtalkAccountBindingKeys.all(wsId) }),
  });
}

// Native subscription and the digital-employee message binding are mutually
// exclusive on the server, so both mutations below refresh the bindings list
// on settle: a conflict means this client's projection is stale.
export function useSetDingTalkNativeSubscription(wsId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ agentId, enabled }: { agentId: string; enabled: boolean }) =>
      api.setDingTalkNativeSubscription(wsId, agentId, enabled),
    onSettled: () =>
      queryClient.invalidateQueries({
        queryKey: dingtalkAccountBindingKeys.all(wsId),
      }),
  });
}

export function useBindDingTalkMessageRouteManually(wsId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({
      agentId,
      ...request
    }: BindDingTalkMessageRouteManuallyRequest & { agentId: string }) =>
      api.bindDingTalkMessageRouteManually(wsId, agentId, request),
    onSettled: () =>
      queryClient.invalidateQueries({
        queryKey: dingtalkAccountBindingKeys.all(wsId),
      }),
  });
}
