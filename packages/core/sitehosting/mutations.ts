import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { hostedSiteKeys } from "./queries";

export function useDeleteHostedSite() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (siteId: string) => api.deleteHostedSite(siteId),
    onSuccess: () =>
      queryClient.invalidateQueries({ queryKey: hostedSiteKeys.all }),
  });
}
