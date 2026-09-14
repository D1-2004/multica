import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import type { CreateAgentPackageRequest } from "../types/agent-package";
import { workspaceKeys } from "../workspace/queries";

export function usePreviewAgentPackage(workspaceId: string) {
  return useMutation({ mutationFn: (file: File) => api.previewAgentPackage(workspaceId, file) });
}

export function useCreateAgentPackage(workspaceId: string, squadId: string | null) {
  const client = useQueryClient();
  return useMutation({
    gcTime: 0,
    retry: false,
    mutationFn: async (request: CreateAgentPackageRequest) => {
      const result = await api.createAgentFromPackage(workspaceId, request);
      if (squadId) {
        try { await api.addSquadMember(squadId, { member_type: "agent", member_id: result.agent.id }); }
        catch (error) { result.warnings = [...result.warnings, error instanceof Error ? error.message : "Could not add agent to squad"]; }
      }
      return result;
    },
    onSuccess: async () => {
      await Promise.all([
        client.invalidateQueries({ queryKey: workspaceKeys.agents(workspaceId) }),
        client.invalidateQueries({ queryKey: workspaceKeys.skills(workspaceId) }),
        client.invalidateQueries({ queryKey: workspaceKeys.squads(workspaceId) }),
      ]);
    },
  });
}
