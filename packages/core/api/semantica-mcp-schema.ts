import { z } from "zod";

export const SemanticaMCPStatusSchema = z.object({
  available: z.boolean(),
  agent_id: z.string().uuid().nullable(),
  agent_name: z.string().nullable(),
  tools: z.array(z.string()),
}).transform((wire) => ({
  available: wire.available,
  agentId: wire.agent_id,
  agentName: wire.agent_name,
  tools: wire.tools,
}));

export type SemanticaMCPStatus = z.infer<typeof SemanticaMCPStatusSchema>;

export const EMPTY_SEMANTICA_MCP_STATUS: SemanticaMCPStatus = {
  available: false,
  agentId: null,
  agentName: null,
  tools: [],
};
