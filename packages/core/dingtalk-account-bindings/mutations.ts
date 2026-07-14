import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { dingtalkAccountBindingKeys } from "./queries";

export function useBeginDingTalkAccountBinding(wsId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (agentId: string) =>
      api.beginDingTalkAccountBinding(wsId, agentId),
    onSettled: () =>
      queryClient.invalidateQueries({
        queryKey: dingtalkAccountBindingKeys.list(wsId),
      }),
  });
}

export function useDeleteDingTalkAccountBinding(wsId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (installationId: string) =>
      api.deleteDingTalkAccountBinding(wsId, installationId),
    onSuccess: () =>
      queryClient.invalidateQueries({
        queryKey: dingtalkAccountBindingKeys.list(wsId),
      }),
  });
}
