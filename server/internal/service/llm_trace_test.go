package service

import (
	"reflect"
	"testing"
)

func TestLLMTraceEnvUsesAgentRuntimeConfig(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want map[string]string
	}{
		{
			name: "enabled with receiver",
			raw:  `{"llm_trace":{"enabled":true,"sink_url":"https://trace.example.test/ingest"}}`,
			want: map[string]string{
				"MULTICA_LLM_TRACE_ENABLED":  "true",
				"MULTICA_LLM_TRACE_SINK_URL": "https://trace.example.test/ingest",
			},
		},
		{
			name: "disabled keeps an empty receiver",
			raw:  `{"llm_trace":{"enabled":false,"sink_url":"https://trace.example.test/ingest"}}`,
			want: map[string]string{
				"MULTICA_LLM_TRACE_ENABLED":  "false",
				"MULTICA_LLM_TRACE_SINK_URL": "",
			},
		},
		{
			name: "missing config is disabled",
			raw:  `{}`,
			want: map[string]string{
				"MULTICA_LLM_TRACE_ENABLED":  "false",
				"MULTICA_LLM_TRACE_SINK_URL": "",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := llmTraceEnv([]byte(tt.raw)); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("llmTraceEnv(%s) = %#v, want %#v", tt.raw, got, tt.want)
			}
		})
	}
}
