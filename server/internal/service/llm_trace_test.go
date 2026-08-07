package service

import (
	"reflect"
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func llmTraceTestRuntime(capabilities ...string) db.AgentRuntime {
	runtime := db.AgentRuntime{
		RuntimeMode: "cloud",
		Provider:    "hermes",
		Metadata:    []byte(`{"kind":"fc-e2b","template_id":"template-id"}`),
	}
	if len(capabilities) == 0 {
		return runtime
	}
	runtime.Metadata = []byte(`{"kind":"fc-e2b","template_id":"template-id","capabilities":["llm_trace_v1"]}`)
	return runtime
}

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
			if got := llmTraceEnv(llmTraceTestRuntime(LLMTraceCapability), []byte(tt.runtimeConfig), []byte(tt.taskContext)); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("llmTraceEnv(%s, %s) = %#v, want %#v", tt.runtimeConfig, tt.taskContext, got, tt.want)
			}
		})
	}
}

func TestLLMTraceEnvSkipsRuntimeWithoutCapability(t *testing.T) {
	got := llmTraceEnv(
		llmTraceTestRuntime(),
		[]byte(`{"llm_trace":{"enabled":true,"sink_url":"https://trace.example.test/ingest"}}`),
		[]byte(`{"completion_callback":{"telemetry_url":"https://router.example.test/llm-traces","telemetry_token":"task-capability","telemetry_expires_at":1786377600000}}`),
	)
	if len(got) != 0 {
		t.Fatalf("unsupported runtime received LLM trace env: %#v", got)
	}
}

func TestLLMTraceEnvKeysAreAllowedForCloudRunner(t *testing.T) {
	env := llmTraceEnv(
		llmTraceTestRuntime(LLMTraceCapability),
		[]byte(`{"llm_trace":{"enabled":true,"sink_url":""}}`),
		[]byte(`{"completion_callback":{"telemetry_url":"https://router.example.test/llm-traces","telemetry_token":"task-capability","telemetry_expires_at":1786377600000}}`),
	)
	for key := range env {
		if !isAllowedFCE2BRunnerExtraEnv(key) {
			t.Fatalf("LLM trace runner environment key %q is not allowed", key)
		}
	}
}
