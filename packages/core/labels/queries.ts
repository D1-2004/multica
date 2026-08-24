import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";
import type { LabelResourceType } from "../types";
import type { LabelUsageParams } from "../types";

export const labelKeys = {
  all: (wsId: string) => ["labels", wsId] as const,
  list: (wsId: string, resourceType: LabelResourceType = "issue") =>
    [...labelKeys.all(wsId), "list", resourceType] as const,
  usageRoot: (wsId: string) => [...labelKeys.all(wsId), "usage"] as const,
  listWithUsage: (wsId: string, resourceType: LabelResourceType = "issue") =>
    [...labelKeys.usageRoot(wsId), "list", resourceType] as const,
  detail: (wsId: string, id: string) =>
    [...labelKeys.all(wsId), "detail", id] as const,
  usage: (wsId: string, id: string, params: LabelUsageParams) =>
    [...labelKeys.usageRoot(wsId), "detail", id, params] as const,
  byIssue: (wsId: string, issueId: string) =>
    [...labelKeys.all(wsId), "issue", issueId] as const,
  byResource: (wsId: string, resourceType: "agent" | "skill", resourceId: string) =>
    [...labelKeys.all(wsId), resourceType, resourceId] as const,
};

export function labelListOptions(
  wsId: string,
  resourceType: LabelResourceType = "issue",
  options?: { includeUsage?: boolean },
) {
  const includeUsage = options?.includeUsage === true;
  return queryOptions({
    queryKey: includeUsage
      ? labelKeys.listWithUsage(wsId, resourceType)
      : labelKeys.list(wsId, resourceType),
    queryFn: () => api.listLabels(resourceType, { includeUsage }),
    select: (data) => data.labels,
  });
}

export function labelUsageOptions(
  wsId: string,
  labelId: string,
  params: LabelUsageParams,
) {
  return queryOptions({
    queryKey: labelKeys.usage(wsId, labelId, params),
    queryFn: () => api.getLabelUsage(labelId, params),
    enabled: Boolean(wsId && labelId),
  });
}

export function resourceLabelsOptions(
  wsId: string,
  resourceType: "agent" | "skill",
  resourceId: string,
) {
  return queryOptions({
    queryKey: labelKeys.byResource(wsId, resourceType, resourceId),
    queryFn: () => api.listLabelsForResource(resourceType, resourceId),
    select: (data) => data.labels,
    enabled: Boolean(resourceId),
  });
}

export function issueLabelsOptions(wsId: string, issueId: string) {
  return queryOptions({
    queryKey: labelKeys.byIssue(wsId, issueId),
    queryFn: () => api.listLabelsForIssue(issueId),
    select: (data) => data.labels,
    enabled: Boolean(issueId),
  });
}
