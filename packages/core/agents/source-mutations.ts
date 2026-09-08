import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { workspaceKeys } from "../workspace/queries";
import { agentSourceKeys } from "./queries";

export function usePreviewAgentSourceSync(agentId: string) {
  return useMutation({
    mutationFn: async (ref: string) => {
      const preview = await api.previewAgentSourceSync(agentId, ref);
      if (!preview.preview_id || !preview.resolved_sha) {
        throw new Error("The server returned an invalid source preview");
      }
      return preview;
    },
  });
}

export function useSyncAgentSource(wsId: string, agentId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (previewId: string) => {
      const result = await api.syncAgentSource(agentId, previewId);
      if (result.source.agent_id !== agentId || !result.source.synced_commit_sha) {
        throw new Error("The server returned an invalid source confirmation");
      }
      return result;
    },
    onSettled: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: agentSourceKeys.detail(wsId, agentId) }),
        queryClient.invalidateQueries({ queryKey: workspaceKeys.agents(wsId) }),
        queryClient.invalidateQueries({ queryKey: workspaceKeys.skills(wsId) }),
      ]);
    },
  });
}

export function useExportAgent(agentId: string) {
  return useMutation({ mutationFn: () => api.exportAgent(agentId) });
}
