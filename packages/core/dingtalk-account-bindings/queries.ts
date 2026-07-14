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
