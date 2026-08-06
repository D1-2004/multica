package service

import "encoding/json"

const (
	llmTraceEnabledEnvKey = "MULTICA_LLM_TRACE_ENABLED"
	llmTraceSinkURLEnvKey = "MULTICA_LLM_TRACE_SINK_URL"
)

func llmTraceEnv(raw []byte) map[string]string {
	var config struct {
		LLMTrace struct {
			Enabled bool   `json:"enabled"`
			SinkURL string `json:"sink_url"`
		} `json:"llm_trace"`
	}
	_ = json.Unmarshal(raw, &config)

	enabled := "false"
	sinkURL := ""
	if config.LLMTrace.Enabled {
		enabled = "true"
		sinkURL = config.LLMTrace.SinkURL
	}
	return map[string]string{
		llmTraceEnabledEnvKey: enabled,
		llmTraceSinkURLEnvKey: sinkURL,
	}
}
