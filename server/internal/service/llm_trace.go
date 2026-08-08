package service

import (
	"encoding/json"
	"net/url"
	"strconv"
	"strings"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	LLMTraceCapability       = "llm_trace_v1"
	llmTraceEnabledEnvKey   = "MULTICA_LLM_TRACE_ENABLED"
	llmTraceSinkURLEnvKey   = "MULTICA_LLM_TRACE_SINK_URL"
	llmTraceTokenEnvKey     = "MULTICA_LLM_TRACE_TOKEN"
	llmTraceExpiresAtEnvKey = "MULTICA_LLM_TRACE_EXPIRES_AT"
)

func llmTraceEnv(
	runtime db.AgentRuntime,
	runtimeConfig []byte,
	taskContext []byte,
	relayBaseURL string,
	taskID string,
) map[string]string {
	if !CloudSandboxRuntimeHasCapability(runtime, LLMTraceCapability) {
		return nil
	}
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
			if relayURL := llmTraceRelayURL(relayBaseURL, taskID); relayURL != "" {
				sinkURL = relayURL
				token = callback.TelemetryToken
				expiresAt = strconv.FormatInt(callback.TelemetryExpiresAt, 10)
			}
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

func llmTraceRelayURL(baseURL, taskID string) string {
	baseURL = strings.TrimSpace(baseURL)
	taskID = strings.TrimSpace(taskID)
	parsed, err := url.Parse(baseURL)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") ||
		parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" ||
		parsed.Fragment != "" || taskID == "" {
		return ""
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") +
		"/api/daemon/tasks/" + url.PathEscape(taskID) + "/llm-traces"
	return parsed.String()
}
