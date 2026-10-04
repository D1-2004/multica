import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

export const tagKeys = {
  all: (wsId: string) => ["workspaces", wsId, "tag"] as const,
};

/** The workspace Tag, its tenants and the caller's rights. Every member can
 * read it: the sidebar needs to know whether to show the Tag entry. */
export function tagOptions(wsId: string) {
  return queryOptions({
    queryKey: tagKeys.all(wsId),
    queryFn: () => api.getTag(wsId),
    enabled: wsId !== "",
    staleTime: 30_000,
  });
}
