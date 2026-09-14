import { z } from "zod";

export const AgentDshPluginConfigSchema = z.object({
  agent_id: z.string().min(1),
  plugin_id: z.string().min(1),
  revision: z.number().int().nonnegative().max(Number.MAX_SAFE_INTEGER),
  inherited: z.boolean(),
  row_id: z.string(),
  config: z.record(z.string(), z.unknown()),
}).transform((row) => ({
  agentId: row.agent_id,
  pluginId: row.plugin_id,
  revision: row.revision,
  inherited: row.inherited,
  rowId: row.row_id,
  config: row.config,
}));
