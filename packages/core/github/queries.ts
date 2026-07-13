import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

export const githubKeys = {
  all: (wsId: string) => ["github", wsId] as const,
  installations: (wsId: string) => [...githubKeys.all(wsId), "installations"] as const,
  agentRepositories: (wsId: string, installationId: string) =>
    [...githubKeys.all(wsId), "agent-repositories", installationId] as const,
  pullRequests: (issueId: string) => ["github", "pull-requests", issueId] as const,
};

export const githubInstallationsOptions = (wsId: string) =>
  queryOptions({
    queryKey: githubKeys.installations(wsId),
    queryFn: () => api.listGitHubInstallations(wsId),
    enabled: !!wsId,
  });

export const issuePullRequestsOptions = (issueId: string) =>
  queryOptions({
    queryKey: githubKeys.pullRequests(issueId),
    queryFn: () => api.listIssuePullRequests(issueId),
    enabled: !!issueId,
  });

export const githubAgentRepositoriesOptions = (wsId: string, installationId: string) =>
  queryOptions({
    queryKey: githubKeys.agentRepositories(wsId, installationId),
    queryFn: () => api.listGitHubAgentRepositories(wsId, installationId),
    enabled: !!wsId && !!installationId,
  });
