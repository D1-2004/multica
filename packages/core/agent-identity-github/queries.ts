import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

export const agentIdentityGithubKeys = {
  all: (wsId: string) => ["agent-identity-github", wsId] as const,
  status: (wsId: string, agentId: string) => [
    ...agentIdentityGithubKeys.all(wsId),
    "status",
    agentId,
  ] as const,
};

export const agentIdentityGithubStatusOptions = (wsId: string, agentId: string) =>
  queryOptions({
    queryKey: agentIdentityGithubKeys.status(wsId, agentId),
    queryFn: () => api.getAgentIdentityGitHubStatus(wsId, agentId),
    enabled: !!wsId && !!agentId,
    refetchOnWindowFocus: "always" as const,
  });
