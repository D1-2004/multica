package service

import (
	"encoding/json"
	"net/url"
	"strings"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	LLMTraceCapability      = "llm_trace_v1"
	llmTraceEnabledEnvKey   = "MULTICA_LLM_TRACE_ENABLED"
	llmTraceSinkURLEnvKey   = "MULTICA_LLM_TRACE_SINK_URL"
	llmTraceTokenEnvKey     = "MULTICA_LLM_TRACE_TOKEN"
	llmTraceExpiresAtEnvKey = "MULTICA_LLM_TRACE_EXPIRES_AT"
)

// llmTraceEnv decides whether the sandbox captures model request/response
// pairs and where it posts them. captureAlways is true when the server has a
// destination of its own (the Langfuse exporter), so capture no longer depends
// on a Router callback or the Agent's static sink being configured.
func llmTraceEnv(
	runtime db.AgentRuntime,
	runtimeConfig []byte,
	taskContext []byte,
	relayBaseURL string,
	taskID string,
	captureAlways bool,
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
	callback := contextPayload.CompletionCallback
	hasRouterTelemetry := strings.TrimSpace(callback.TelemetryURL) != "" &&
		strings.TrimSpace(callback.TelemetryToken) != "" && callback.TelemetryExpiresAt > 0
	hasStaticSink := config.LLMTrace.Enabled && strings.TrimSpace(config.LLMTrace.SinkURL) != ""
	if hasRouterTelemetry || hasStaticSink || captureAlways {
		sinkURL = llmTraceRelayURL(relayBaseURL, taskID)
	}
	if strings.TrimSpace(sinkURL) != "" {
		enabled = "true"
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
