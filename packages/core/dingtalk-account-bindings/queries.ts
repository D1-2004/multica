import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

export const dingtalkAccountBindingKeys = {
  all: (wsId: string) => ["dingtalk-account-bindings", wsId] as const,
  list: (wsId: string) => [
    ...dingtalkAccountBindingKeys.all(wsId),
    "list",
  ] as const,
};

export const dingtalkAccountBindingsOptions = (wsId: string) =>
  queryOptions({
    queryKey: dingtalkAccountBindingKeys.list(wsId),
    queryFn: () => api.listDingTalkAccountBindings(wsId),
    enabled: !!wsId,
    refetchOnWindowFocus: "always" as const,
  });

// The stream state changes without any event this client sees, so it is
// polled while native subscription is on. It sits under all(wsId): the
// native subscription switch refreshes it on settle.
export const DINGTALK_NATIVE_STREAM_POLL_MS = 10_000;

export const dingtalkNativeSubscriptionStatusOptions = (
  wsId: string,
  agentId: string,
  enabled: boolean,
) =>
  queryOptions({
    queryKey: [...dingtalkAccountBindingKeys.all(wsId), "native-subscription", agentId],
    queryFn: () => api.getDingTalkNativeSubscriptionStatus(wsId, agentId),
    enabled: enabled && !!wsId && !!agentId,
    refetchInterval: enabled ? DINGTALK_NATIVE_STREAM_POLL_MS : false,
    refetchOnWindowFocus: "always",
  });

export const reusableDingTalkIdentitiesOptions = (wsId: string, agentId: string) =>
  queryOptions({
    queryKey: [...dingtalkAccountBindingKeys.all(wsId), "reusable", agentId],
    queryFn: () => api.listReusableDingTalkIdentities(wsId, agentId),
    enabled: !!wsId && !!agentId,
    refetchOnWindowFocus: "always",
  });
