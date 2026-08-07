package service

import (
	"encoding/json"
	"strconv"
	"strings"
)

const (
	llmTraceEnabledEnvKey   = "MULTICA_LLM_TRACE_ENABLED"
	llmTraceSinkURLEnvKey   = "MULTICA_LLM_TRACE_SINK_URL"
	llmTraceTokenEnvKey     = "MULTICA_LLM_TRACE_TOKEN"
	llmTraceExpiresAtEnvKey = "MULTICA_LLM_TRACE_EXPIRES_AT"
)

func llmTraceEnv(runtimeConfig []byte, taskContext []byte) map[string]string {
	var config struct {
		LLMTrace struct {
			Enabled bool   `json:"enabled"`
			SinkURL string `json:"sink_url"`
		} `json:"llm_trace"`
	}
	_ = json.Unmarshal(runtimeConfig, &config)
	var contextPayload struct {
		CompletionCallback struct {
			TelemetryURL       string `json:"telemetry_url"`
			TelemetryToken     string `json:"telemetry_token"`
			TelemetryExpiresAt int64  `json:"telemetry_expires_at"`
		} `json:"completion_callback"`
	}
	_ = json.Unmarshal(taskContext, &contextPayload)

	enabled := "false"
	sinkURL := ""
	token := ""
	expiresAt := ""
	if config.LLMTrace.Enabled {
		callback := contextPayload.CompletionCallback
		if strings.TrimSpace(callback.TelemetryURL) != "" &&
			strings.TrimSpace(callback.TelemetryToken) != "" && callback.TelemetryExpiresAt > 0 {
			sinkURL = callback.TelemetryURL
			token = callback.TelemetryToken
			expiresAt = strconv.FormatInt(callback.TelemetryExpiresAt, 10)
		} else {
			sinkURL = config.LLMTrace.SinkURL
		}
		if strings.TrimSpace(sinkURL) != "" {
			enabled = "true"
		}
	}
	return map[string]string{
		llmTraceEnabledEnvKey:   enabled,
		llmTraceSinkURLEnvKey:   sinkURL,
		llmTraceTokenEnvKey:     token,
		llmTraceExpiresAtEnvKey: expiresAt,
	}
}
