export interface LLMTraceRuntimeConfig {
  enabled: boolean;
  sinkUrl: string;
}

function asRecord(raw: unknown): Record<string, unknown> {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return {};
  return raw as Record<string, unknown>;
}

export function parseLLMTraceRuntimeConfig(
  raw: unknown,
): LLMTraceRuntimeConfig {
  const trace = asRecord(asRecord(raw).llm_trace);
  return {
    enabled: trace.enabled === true,
    sinkUrl: typeof trace.sink_url === "string" ? trace.sink_url : "",
  };
}

export function mergeLLMTraceRuntimeConfig(
  raw: unknown,
  next: LLMTraceRuntimeConfig,
): Record<string, unknown> {
  return {
    ...asRecord(raw),
    llm_trace: {
      enabled: next.enabled,
      sink_url: next.sinkUrl,
    },
  };
}
