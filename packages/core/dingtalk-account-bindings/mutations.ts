import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { dingtalkAccountBindingKeys } from "./queries";
import type { DingTalkBindingMode, DingTalkProcessingSurface } from "../types";

export function useBeginDingTalkAccountBinding(wsId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ agentId, bindingMode }: { agentId: string; bindingMode: DingTalkBindingMode }) =>
      api.beginDingTalkAccountBinding(wsId, agentId, bindingMode),
    onSettled: () =>
      queryClient.invalidateQueries({
        queryKey: dingtalkAccountBindingKeys.list(wsId),
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
        queryKey: dingtalkAccountBindingKeys.list(wsId),
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
        queryKey: dingtalkAccountBindingKeys.list(wsId),
      }),
  });
}
