import { queryOptions, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import type { AgentRuntime } from "../types";
import type { RuntimeVisibility } from "../types/agent";
import { runtimeKeys } from "./queries";

export interface CloudRuntimeNode {
  id: string;
  owner_id: string;
  instance_id: string;
  region: string;
  instance_type: string;
  image_id: string;
  subnet_id: string;
  name: string;
  status: string;
  tags: Record<string, string>;
  metadata: Record<string, unknown>;
  created_at: string;
  updated_at: string;
}

export interface ListCloudRuntimeNodesParams {
  limit?: number;
  offset?: number;
}

export interface CreateCloudRuntimeNodeRequest {
  instance_type: string;
  name?: string;
  region?: string;
  image_id?: string;
  subnet_id?: string;
  key_name?: string;
  iam_instance_profile?: string;
  disk_size_gb?: number;
  tags?: Record<string, string>;
}

export interface CreateFCE2BRuntimeRequest {
  name?: string;
  template_id: string;
  visibility?: RuntimeVisibility;
}

export interface FCE2BTemplate {
  id?: string;
  name?: string;
  template: string;
  status?: string;
  created_at?: string;
  updated_at?: string;
  metadata?: Record<string, unknown>;
}

export interface DWSAuthProfile {
  id: string;
  workspace_id: string;
  owner_id?: string;
  label: string;
  corp_id?: string;
  corp_name?: string;
  user_id?: string;
  user_name?: string;
  status: string;
  created_at: string;
  updated_at: string;
}

export interface DWSAuthSession {
  id: string;
  status: "pending" | "succeeded" | "failed";
  message?: string;
  login_url?: string;
  user_code?: string;
  error?: string;
  profile?: DWSAuthProfile;
  created_at: string;
  updated_at: string;
}

export const cloudRuntimeKeys = {
  all: (wsId: string) => ["cloud-runtime", wsId] as const,
  nodes: (wsId: string) => [...cloudRuntimeKeys.all(wsId), "nodes"] as const,
  fcE2BTemplates: (wsId: string) =>
    [...cloudRuntimeKeys.all(wsId), "fc-e2b-templates"] as const,
  dwsProfiles: (wsId: string) =>
    [...cloudRuntimeKeys.all(wsId), "dws-profiles"] as const,
};

const PENDING_NODE_STATUSES = new Set([
  "launching",
  "pending",
  "starting",
  "stopping",
  "rebooting",
  "terminating",
]);

export function isCloudRuntimeNodePending(status: string): boolean {
  return PENDING_NODE_STATUSES.has(status.toLowerCase());
}

export function cloudRuntimeNodeListOptions(
  wsId: string,
  params?: ListCloudRuntimeNodesParams,
) {
  const limit = params?.limit ?? 20;
  const offset = params?.offset ?? 0;
  return queryOptions({
    queryKey: [...cloudRuntimeKeys.nodes(wsId), { limit, offset }] as const,
    queryFn: () => api.listCloudRuntimeNodes({ limit, offset }),
    refetchInterval: (query) =>
      query.state.data?.some((node) => isCloudRuntimeNodePending(node.status))
        ? 5000
        : false,
    staleTime: 15 * 1000,
  });
}

export function fcE2BTemplateListOptions(wsId: string) {
  return queryOptions({
    queryKey: cloudRuntimeKeys.fcE2BTemplates(wsId),
    queryFn: () => api.listFCE2BTemplates(),
    staleTime: 30 * 1000,
  });
}

export function useFCE2BTemplates(wsId: string) {
  return useQuery(fcE2BTemplateListOptions(wsId));
}

export function useCreateCloudRuntimeNode(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (data: CreateCloudRuntimeNodeRequest) =>
      api.createCloudRuntimeNode(data),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: cloudRuntimeKeys.all(wsId) });
    },
  });
}

export function useDeleteCloudRuntimeNode(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (instanceId: string) => api.deleteCloudRuntimeNode(instanceId),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: cloudRuntimeKeys.all(wsId) });
    },
  });
}

export function isFCE2BRuntime(
  runtime: Pick<AgentRuntime, "runtime_mode" | "metadata"> | null | undefined,
): boolean {
  return runtime?.runtime_mode === "cloud" && runtime.metadata?.kind === "fc-e2b";
}

export function useCreateFCE2BRuntime(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (data: CreateFCE2BRuntimeRequest) =>
      api.createFCE2BRuntime(data),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: runtimeKeys.all(wsId) });
    },
  });
}
