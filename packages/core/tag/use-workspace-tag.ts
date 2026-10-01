import { useQuery } from "@tanstack/react-query";
import { tagOptions } from "./queries";

/** The workspace Tag state (Tag, tenants, caller rights). */
export function useWorkspaceTag(wsId: string) {
  return useQuery(tagOptions(wsId));
}
