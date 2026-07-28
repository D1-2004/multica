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
export const FC_E2B_RUNTIME_PROVIDERS = ["hermes", "opencode", "pi"] as const;
export type FCE2BRuntimeProvider = (typeof FC_E2B_RUNTIME_PROVIDERS)[number];

export interface CreateFCE2BRuntimeRequest {
  name?: string;
  template_id?: string;
  template_channel?: "stable" | "candidate";
  provider?: FCE2BRuntimeProvider;
  visibility?: RuntimeVisibility;
}

export interface UpdateFCE2BRuntimeTemplateRequest {
  template_id: string;
}

export interface FCE2BTemplate {
  id?: string;
  build_id?: string;
  name?: string;
  template: string;
  status?: string;
  created_at?: string;
  updated_at?: string;
  manifest_version: number;
  providers: string[];
  capabilities: string[];
  component_versions: Record<string, string>;
  runner_protocol: string;
  metadata?: Record<string, unknown>;
}

export interface FCE2BRuntimeMetadata {
  kind: "fc-e2b";
  template: string | null;
  templateId: string | null;
  templateBuildId: string | null;
  templateName: string | null;
  templateStatus: string | null;
  templateChannel: "stable" | "candidate";
}

export interface FCE2BStableTemplateBinding {
  template_id: string;
  template_build_id: string;
  template_alias: string;
  release_id: string;
}

export type FCE2BStableReleaseStatus =
  | "validating"
  | "rolling_out"
  | "observing"
  | "completed"
  | "paused"
  | "rolling_back"
  | "rolled_back"
  | "failed";

export interface FCE2BStableRelease {
  id: string;
  template_id: string;
  template_build_id: string;
  template_alias: string;
  source_revision: string;
  note: string;
  actor_user_id: string;
  bootstrap: boolean;
  status: FCE2BStableReleaseStatus;
  current_batch: number;
  target_percentage: number;
  previous_template_id: string;
  previous_template_build_id: string;
  previous_template_alias: string;
  manifest?: Record<string, unknown>;
  total_targets: number;
  updated_targets: number;
  failed_targets: number;
  rollout_started_at?: string;
  batch_started_at?: string;
  next_batch_at?: string;
  completed_at?: string;
  validation_error?: string;
  created_at: string;
  updated_at: string;
}

export interface FCE2BStableChannel {
  current: FCE2BStableTemplateBinding | null;
  active_release: FCE2BStableRelease | null;
  can_publish: boolean;
}

export interface CreateFCE2BStableReleaseRequest {
  template_id: string;
  expected_build_id: string;
  note?: string;
}

function metadataString(
  metadata: Record<string, unknown>,
  key: string,
): string | null {
  const value = metadata[key];
  return typeof value === "string" && value.trim() ? value.trim() : null;
}

/**
 * Parses the FC/E2B fields carried in a runtime's open-ended metadata map.
 * The returned shape uses camelCase so views never read wire keys directly.
 */
export function parseFCE2BRuntimeMetadata(
  runtime: Pick<AgentRuntime, "runtime_mode" | "metadata"> | null | undefined,
): FCE2BRuntimeMetadata | null {
  if (runtime?.runtime_mode !== "cloud") return null;
  const metadata = runtime.metadata;
  if (
    !metadata ||
    typeof metadata !== "object" ||
    Array.isArray(metadata) ||
    metadataString(metadata, "kind") !== "fc-e2b"
  ) {
    return null;
  }
  return {
    kind: "fc-e2b",
    template: metadataString(metadata, "template"),
    templateId: metadataString(metadata, "template_id"),
    templateBuildId: metadataString(metadata, "template_build_id"),
    templateName: metadataString(metadata, "template_name"),
    templateStatus: metadataString(metadata, "template_status"),
    templateChannel:
      metadataString(metadata, "template_channel") === "candidate"
        ? "candidate"
        : "stable",
  };
}

export function isReadyFCE2BTemplate(template: FCE2BTemplate): boolean {
  return (
    typeof template.id === "string" &&
    template.id.trim().length > 0 &&
    typeof template.build_id === "string" &&
    template.build_id.trim().length > 0 &&
    template.status?.trim().toLowerCase() === "ready" &&
    template.manifest_version === 2 &&
    template.runner_protocol === "root-log-v1" &&
    template.providers.some((provider) =>
      (FC_E2B_RUNTIME_PROVIDERS as readonly string[]).includes(provider),
    )
  );
}

/**
 * First server-supported provider declared by the verified template manifest.
 */
export function fcE2BProviderForTemplate(
  template: FCE2BTemplate,
): FCE2BRuntimeProvider | null {
  for (const provider of FC_E2B_RUNTIME_PROVIDERS) {
    if (template.providers.includes(provider)) {
      return provider;
    }
  }
  return null;
}

export const cloudRuntimeKeys = {
  all: (wsId: string) => ["cloud-runtime", wsId] as const,
  nodes: (wsId: string) => [...cloudRuntimeKeys.all(wsId), "nodes"] as const,
  fcE2BTemplates: (wsId: string) =>
    [...cloudRuntimeKeys.all(wsId), "fc-e2b-templates"] as const,
  fcE2BStableChannel: () => ["fc-e2b-stable-channel"] as const,
  fcE2BStableRelease: (releaseId: string) =>
    ["fc-e2b-stable-release", releaseId] as const,
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

export function useFCE2BStableChannel() {
  return useQuery({
    queryKey: cloudRuntimeKeys.fcE2BStableChannel(),
    queryFn: () => api.getFCE2BStableChannel(),
    refetchInterval: (query) => (query.state.data?.active_release ? 5000 : false),
    staleTime: 15 * 1000,
  });
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
  return parseFCE2BRuntimeMetadata(runtime) !== null;
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

export function useUpdateFCE2BRuntimeTemplate(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({
      runtimeId,
      data,
    }: {
      runtimeId: string;
      data: UpdateFCE2BRuntimeTemplateRequest;
    }) => api.updateFCE2BRuntimeTemplate(runtimeId, data),
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: runtimeKeys.all(wsId) });
    },
  });
}

export function useCreateFCE2BStableRelease() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({
      data,
      idempotencyKey,
    }: {
      data: CreateFCE2BStableReleaseRequest;
      idempotencyKey: string;
    }) => api.createFCE2BStableRelease(data, idempotencyKey),
    onSuccess: async (release) => {
      await Promise.all([
        qc.invalidateQueries({ queryKey: cloudRuntimeKeys.fcE2BStableChannel() }),
        qc.invalidateQueries({
          queryKey: cloudRuntimeKeys.fcE2BStableRelease(release.id),
        }),
      ]);
    },
  });
}

export function useMutateFCE2BStableRelease(
  action: "pause" | "resume" | "rollback",
) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (releaseId: string) =>
      api.mutateFCE2BStableRelease(releaseId, action),
    onSuccess: async (release) => {
      await Promise.all([
        qc.invalidateQueries({ queryKey: cloudRuntimeKeys.fcE2BStableChannel() }),
        qc.invalidateQueries({
          queryKey: cloudRuntimeKeys.fcE2BStableRelease(release.id),
        }),
      ]);
    },
  });
}
