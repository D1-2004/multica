import { z } from "zod";

export const InternalConnectorSchema = z.object({
  id: z.string().uuid(),
  workspace_id: z.string().uuid(),
  name: z.string(),
  upstream_url: z.string().url(),
  credential_ref: z.string(),
  credential_ready: z.boolean(),
  credential_source: z.enum(["none", "environment", "workspace", "unavailable"]).optional().default("none"),
  allowed_tools: z.array(z.string()),
  agent_ids: z.array(z.string()),
  enabled: z.boolean(),
}).transform((v) => ({
  id: v.id,
  workspaceId: v.workspace_id,
  name: v.name,
  upstreamUrl: v.upstream_url,
  credentialRef: v.credential_ref,
  credentialReady: v.credential_ready,
  credentialSource: v.credential_source,
  allowedTools: v.allowed_tools,
  agentIds: v.agent_ids,
  enabled: v.enabled,
}));

export const InternalConnectorListSchema = z.array(InternalConnectorSchema);
export type InternalConnector = z.infer<typeof InternalConnectorSchema>;

export const AvailableInternalConnectorSchema = z.object({
  id: z.string().uuid(),
  name: z.string(),
  agent_id: z.string().uuid(),
  agent_name: z.string(),
  tools: z.array(z.string()),
}).transform((v) => ({
  id: v.id,
  name: v.name,
  agentId: v.agent_id,
  agentName: v.agent_name,
  tools: v.tools,
}));
export const AvailableInternalConnectorListSchema = z.array(AvailableInternalConnectorSchema);
export type AvailableInternalConnector = z.infer<typeof AvailableInternalConnectorSchema>;

export const SavedInternalConnectorSchema = z.object({
  id: z.string().uuid(),
  credential_ref: z.string(),
});

export type InternalConnectorInput = {
  name: string;
  upstream_url: string;
  allowed_tools: string[];
  agent_ids: string[];
  enabled: boolean;
};

export const InternalConnectorTestSchema = z.object({
  reachable: z.boolean(),
  message: z.string().optional(),
  tools: z.array(z.string()).optional(),
  has_more: z.boolean().optional(),
  duration_ms: z.number().optional(),
});
export type InternalConnectorTest = z.infer<typeof InternalConnectorTestSchema>;
