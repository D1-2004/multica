import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

export const hostedSiteKeys = {
  all: ["hosted-sites"] as const,
  list: (workspaceId: string) =>
    [...hostedSiteKeys.all, "list", workspaceId] as const,
};

export function hostedSiteListOptions(workspaceId: string) {
  return queryOptions({
    queryKey: hostedSiteKeys.list(workspaceId),
    queryFn: () => api.listHostedSites(),
  });
}
