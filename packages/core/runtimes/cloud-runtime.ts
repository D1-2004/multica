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

/**
 * Agent providers an FC/E2B sandbox runtime can run. A template image may
 * ship several of these CLIs; the runtime's provider is chosen at creation.
 * Mirrors the server-side `FCE2BSupportedProviders`.
 */
export const FC_E2B_RUNTIME_PROVIDERS = ["hermes", "opencode"] as const;
export type FCE2BRuntimeProvider = (typeof FC_E2B_RUNTIME_PROVIDERS)[number];

export interface CreateFCE2BRuntimeRequest {
  name?: string;
  template_id: string;
  provider?: FCE2BRuntimeProvider;
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

/**
 * Default provider for a template, sniffed from its identifiers the same way
 * the server does. Only a preselection — the user's explicit choice wins.
 */
export function fcE2BProviderForTemplate(
  template: FCE2BTemplate,
): FCE2BRuntimeProvider {
  const haystack = [template.template, template.id, template.name]
    .filter(Boolean)
    .join(" ")
    .toLowerCase();
  for (const provider of FC_E2B_RUNTIME_PROVIDERS) {
    if (provider !== "hermes" && haystack.includes(provider)) {
      return provider;
    }
  }
  return "hermes";
}

export const cloudRuntimeKeys = {
  all: (wsId: string) => ["cloud-runtime", wsId] as const,
  nodes: (wsId: string) => [...cloudRuntimeKeys.all(wsId), "nodes"] as const,
  fcE2BTemplates: (wsId: string) =>
    [...cloudRuntimeKeys.all(wsId), "fc-e2b-templates"] as const,
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
