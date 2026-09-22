import { z } from "zod";
export const ModelRefSchema = z.object({
  provider: z.string(),
  model: z.string(),
});
export const ModelProviderSchema = z
  .object({
    id: z.string(),
    name: z.string(),
    base_url: z.string(),
    models: z.array(z.string()).default([]),
    enabled: z.boolean(),
    builtin: z.boolean().default(false),
    has_key: z.boolean().default(false),
  })
  .transform((p) => ({
    id: p.id,
    name: p.name,
    baseUrl: p.base_url,
    models: p.models,
    enabled: p.enabled,
    builtin: p.builtin,
    hasKey: p.has_key,
    apiKey: "",
  }));
export const GlobalModelsSchema = z
  .object({
    revision: z.number(),
    providers: z.array(ModelProviderSchema),
    agent_models: z.array(ModelRefSchema),
    default_model: ModelRefSchema,
    coordinator: z.array(ModelRefSchema),
    diamond_fallback: z.boolean(),
  })
  .transform((v) => ({
    revision: v.revision,
    providers: v.providers,
    agentModels: v.agent_models,
    defaultModel: v.default_model,
    coordinator: v.coordinator,
    diamondFallback: v.diamond_fallback,
  }));
export type GlobalModels = z.output<typeof GlobalModelsSchema>;
export type ModelProvider = GlobalModels["providers"][number];
export type ModelRef = z.output<typeof ModelRefSchema>;
export const EMPTY_GLOBAL_MODELS: GlobalModels = {
  revision: -1,
  providers: [],
  agentModels: [],
  defaultModel: { provider: "", model: "" },
  coordinator: [],
  diamondFallback: true,
};
export const DeveloperCapabilitiesSchema = z.object({ developer: z.boolean() });
export const DiscoveredModelsSchema = z.object({ models: z.array(z.string()) });
export function globalModelsWire(c: GlobalModels) {
  return {
    revision: c.revision,
    providers: c.providers.map((p) => ({
      id: p.id,
      name: p.name,
      base_url: p.baseUrl,
      models: p.models.map(m=>m.trim()).filter(Boolean),
      enabled: p.enabled,
      builtin: p.builtin,
      api_key: p.apiKey || undefined,
    })),
    agent_models: c.agentModels,
    default_model: c.defaultModel,
    coordinator: c.coordinator,
    diamond_fallback: c.diamondFallback,
  };
}

export const ModelProbeSchema = z
  .object({ valid: z.boolean(), status: z.number(), elapsed_ms: z.number() })
  .transform((v) => ({
    valid: v.valid,
    status: v.status,
    elapsedMs: v.elapsed_ms,
  }));
