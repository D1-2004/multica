package service

import (
	"reflect"
	"testing"
)

func TestLLMTraceEnvUsesAgentRuntimeConfig(t *testing.T) {
	tests := []struct {
		name          string
		runtimeConfig string
		taskContext   string
		want          map[string]string
	}{
		{
			name:          "enabled with receiver",
			runtimeConfig: `{"llm_trace":{"enabled":true,"sink_url":"https://trace.example.test/ingest"}}`,
			taskContext:   `{}`,
			want: map[string]string{
				"MULTICA_LLM_TRACE_ENABLED":    "true",
				"MULTICA_LLM_TRACE_SINK_URL":   "https://trace.example.test/ingest",
				"MULTICA_LLM_TRACE_TOKEN":      "",
				"MULTICA_LLM_TRACE_EXPIRES_AT": "",
			},
		},
		{
			name:          "router telemetry supplies the effective receiver",
			runtimeConfig: `{"llm_trace":{"enabled":true,"sink_url":""}}`,
			taskContext:   `{"completion_callback":{"telemetry_url":"https://router.example.test/api/v1/dispatch-tasks/task-1/llm-traces","telemetry_token":"task-capability","telemetry_expires_at":1786377600000}}`,
			want: map[string]string{
				"MULTICA_LLM_TRACE_ENABLED":    "true",
				"MULTICA_LLM_TRACE_SINK_URL":   "https://router.example.test/api/v1/dispatch-tasks/task-1/llm-traces",
				"MULTICA_LLM_TRACE_TOKEN":      "task-capability",
				"MULTICA_LLM_TRACE_EXPIRES_AT": "1786377600000",
			},
		},
		{
			name:          "disabled keeps an empty receiver",
			runtimeConfig: `{"llm_trace":{"enabled":false,"sink_url":"https://trace.example.test/ingest"}}`,
			taskContext:   `{"completion_callback":{"telemetry_url":"https://router.example.test/api/v1/dispatch-tasks/task-1/llm-traces","telemetry_token":"task-capability","telemetry_expires_at":1786377600000}}`,
			want: map[string]string{
				"MULTICA_LLM_TRACE_ENABLED":    "false",
				"MULTICA_LLM_TRACE_SINK_URL":   "",
				"MULTICA_LLM_TRACE_TOKEN":      "",
				"MULTICA_LLM_TRACE_EXPIRES_AT": "",
			},
		},
		{
			name:          "missing config is disabled",
			runtimeConfig: `{}`,
			taskContext:   `{}`,
			want: map[string]string{
				"MULTICA_LLM_TRACE_ENABLED":    "false",
				"MULTICA_LLM_TRACE_SINK_URL":   "",
				"MULTICA_LLM_TRACE_TOKEN":      "",
				"MULTICA_LLM_TRACE_EXPIRES_AT": "",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := llmTraceEnv([]byte(tt.runtimeConfig), []byte(tt.taskContext)); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("llmTraceEnv(%s, %s) = %#v, want %#v", tt.runtimeConfig, tt.taskContext, got, tt.want)
			}
		})
	}
}
