import { useMutation, useQueryClient, type QueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { workspaceKeys } from "../workspace/queries";
import type { CreateTagInput, TagState } from "./types";
import { tagKeys } from "./queries";

/** Tag writes create, rename or reconfigure agents, so the agent lists are
 * refreshed together with the Tag. */
function invalidateTagViews(queryClient: QueryClient, wsId: string) {
  return Promise.all([
    queryClient.invalidateQueries({ queryKey: tagKeys.all(wsId) }),
    queryClient.invalidateQueries({ queryKey: workspaceKeys.agents(wsId) }),
  ]);
}

function setTagState(queryClient: QueryClient, wsId: string, state: TagState) {
  queryClient.setQueryData(tagKeys.all(wsId), state);
}

export function useCreateTag(wsId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: CreateTagInput) => api.createTag(wsId, input),
    onSuccess: (state) => setTagState(queryClient, wsId, state),
    onSettled: () => invalidateTagViews(queryClient, wsId),
  });
}

export function useSetTagSidebarVisible(wsId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (visible: boolean) => api.setTagSidebarVisible(wsId, visible),
    onSuccess: (state) => setTagState(queryClient, wsId, state),
    onSettled: () => queryClient.invalidateQueries({ queryKey: tagKeys.all(wsId) }),
  });
}

export function useDeleteTag(wsId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () => api.deleteTag(wsId),
    onSettled: () => invalidateTagViews(queryClient, wsId),
  });
}

export function useCreateTagTenant(wsId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (name: string) => api.createTagTenant(wsId, name),
    onSettled: () => invalidateTagViews(queryClient, wsId),
  });
}

export function useAdoptTagTenant(wsId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: { agentId: string; name: string }) =>
      api.adoptTagTenant(wsId, input.agentId, input.name),
    onSettled: () => invalidateTagViews(queryClient, wsId),
  });
}

export function useRenameTagTenant(wsId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: { tenantId: string; name: string }) =>
      api.renameTagTenant(wsId, input.tenantId, input.name),
    onSettled: () => queryClient.invalidateQueries({ queryKey: tagKeys.all(wsId) }),
  });
}

export function useDeleteTagTenant(wsId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (tenantId: string) => api.deleteTagTenant(wsId, tenantId),
    onSettled: () => invalidateTagViews(queryClient, wsId),
  });
}

export function useApplyTag(wsId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: { tenantIds: string[]; note?: string }) =>
      api.applyTag(wsId, input.tenantIds, input.note ?? ""),
    onSettled: () => invalidateTagViews(queryClient, wsId),
  });
}
