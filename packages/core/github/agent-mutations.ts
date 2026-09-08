import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import type { CreateGitHubAgentRequest, GitHubAgentPreviewRequest } from "../types";
import { workspaceKeys } from "../workspace/queries";

export function usePreviewGitHubAgent(wsId: string) {
  return useMutation({
    mutationFn: async (request: GitHubAgentPreviewRequest) => {
      const result = await api.previewGitHubAgent(wsId, request);
      if (!result.preview_id || !result.resolved_sha) throw new Error("Invalid Git preview response");
      return result;
    },
  });
}

export function useCreateGitHubAgent(wsId: string, squadId: string | null) {
  const client = useQueryClient();
  return useMutation({
    mutationFn: async (request: CreateGitHubAgentRequest) => {
      if (!request.preview_id) throw new Error("Preview the repository before creating an agent");
      const result = await api.createGitHubAgent(wsId, request);
      if (!result.agent.id || !result.source.synced_commit_sha) throw new Error("Invalid Git creation response");
      if (squadId) {
        try {
          await api.addSquadMember(squadId, { member_type: "agent", member_id: result.agent.id });
        } catch (error) {
          result.warnings = [...(result.warnings ?? []), error instanceof Error ? error.message : "Could not add agent to squad"];
        }
      }
      return result;
    },
    onSuccess: async () => {
      await Promise.all([
        client.invalidateQueries({ queryKey: workspaceKeys.agents(wsId) }),
        client.invalidateQueries({ queryKey: workspaceKeys.skills(wsId) }),
        client.invalidateQueries({ queryKey: workspaceKeys.squads(wsId) }),
      ]);
    },
  });
}
