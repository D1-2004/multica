import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

export const hostedSiteKeys = {
  all: ["hosted-sites"] as const,
  list: () => [...hostedSiteKeys.all, "list"] as const,
};

export function hostedSiteListOptions() {
  return queryOptions({
    queryKey: hostedSiteKeys.list(),
    queryFn: () => api.listHostedSites(),
  });
}
