import { describe, expect, it } from "vitest";
import {
  mergeLLMTraceRuntimeConfig,
  parseLLMTraceRuntimeConfig,
} from "./llm-trace-runtime-config";

describe("LLM trace runtime config", () => {
  it("uses disabled empty defaults when trace config is absent", () => {
    expect(parseLLMTraceRuntimeConfig({ gateway: { token: "***" } })).toEqual({
      enabled: false,
      sinkUrl: "",
    });
  });

  it("merges the trace switch and sink URL without dropping other runtime config", () => {
    const raw = {
      gateway: { token: "***", host: "gateway.example.test" },
      future_provider_setting: true,
      llm_trace: { enabled: false, sink_url: "https://old.example.test" },
    };

    expect(
      mergeLLMTraceRuntimeConfig(raw, {
        enabled: true,
        sinkUrl: "https://trace.example.test/ingest",
      }),
    ).toEqual({
      gateway: { token: "***", host: "gateway.example.test" },
      future_provider_setting: true,
      llm_trace: {
        enabled: true,
        sink_url: "https://trace.example.test/ingest",
      },
    });
  });
});
