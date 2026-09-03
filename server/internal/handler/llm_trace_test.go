package handler

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type fakeLLMTraceRouter struct {
	path       string
	capability string
	payload    []byte
	status     int
	calls      int
}

type fakeLLMTraceSink struct {
	url     string
	payload []byte
	status  int
	calls   int
}

func (f *fakeLLMTraceSink) SubmitLLMTrace(
	_ context.Context,
	sinkURL string,
	payload []byte,
) (int, error) {
	f.calls++
	f.url = sinkURL
	f.payload = append([]byte(nil), payload...)
	return f.status, nil
}

func (f *fakeLLMTraceRouter) SubmitLLMTrace(
	_ context.Context,
	path string,
	capability string,
	payload []byte,
) (int, error) {
	f.calls++
	f.path = path
	f.capability = capability
	f.payload = append([]byte(nil), payload...)
	return f.status, nil
}

func TestRelayTaskLLMTraceUsesStoredRelativeCallback(t *testing.T) {
	router := &fakeLLMTraceRouter{status: http.StatusCreated}
	task := db.AgentTaskQueue{Context: []byte(`{
		"completion_callback": {
			"telemetry_url": "/api/v1/dispatch-tasks/router-task-1/llm-traces",
			"telemetry_token": "task-capability",
			"telemetry_expires_at": 1786464000000
		}
	}`)}
	payload := []byte(`{"sequence":1,"request":{"body":"request"},"response":{"body":"response"}}`)

	status, err := relayTaskLLMTrace(
		context.Background(),
		task,
		[]byte(`{"llm_trace":{"enabled":true,"sink_url":""}}`),
		payload,
		time.UnixMilli(1786377600000),
		router,
		nil,
		nil,
	)
	if err != nil || status != http.StatusNoContent {
		t.Fatalf("status=%d err=%v", status, err)
	}
	if router.calls != 1 ||
		router.path != "/api/v1/dispatch-tasks/router-task-1/llm-traces" ||
		router.capability != "task-capability" || string(router.payload) != string(payload) {
		t.Fatalf("relay = %#v", router)
	}
}

func TestRelayTaskLLMTraceDisabledAgentSinkStillForwardsRouterCallback(t *testing.T) {
	router := &fakeLLMTraceRouter{status: http.StatusCreated}
	sink := &fakeLLMTraceSink{status: http.StatusNoContent}
	task := db.AgentTaskQueue{Context: []byte(`{
		"completion_callback": {
			"telemetry_url": "/api/v1/dispatch-tasks/router-task-1/llm-traces",
			"telemetry_token": "task-capability",
			"telemetry_expires_at": 1786464000000
		}
	}`)}
	payload := []byte(`{"sequence":1}`)

	status, err := relayTaskLLMTrace(
		context.Background(),
		task,
		[]byte(`{"llm_trace":{"enabled":false,"sink_url":"https://trace.example.test/ingest"}}`),
		payload,
		time.UnixMilli(1786377600000),
		router,
		sink,
		nil,
	)
	if err != nil || status != http.StatusNoContent {
		t.Fatalf("status=%d err=%v", status, err)
	}
	if router.calls != 1 || sink.calls != 0 {
		t.Fatalf("router calls=%d sink calls=%d", router.calls, sink.calls)
	}
	if string(router.payload) != string(payload) {
		t.Fatalf("router payload=%s want=%s", router.payload, payload)
	}
}

func TestRelayTaskLLMTraceUsesStoredCapabilityIndependentlyOfDaemonAuthorization(t *testing.T) {
	baseTask := db.AgentTaskQueue{Context: []byte(`{
		"completion_callback": {
			"telemetry_url": "/api/v1/dispatch-tasks/router-task-1/llm-traces",
			"telemetry_token": "task-capability",
			"telemetry_expires_at": 1786464000000
		}
	}`)}
	for _, test := range []struct {
		name  string
		now   time.Time
		task  db.AgentTaskQueue
		match error
	}{
		{name: "expired", now: time.UnixMilli(1786464000000), task: baseTask, match: errLLMTraceExpired},
		{
			name:  "absolute callback",
			now:   time.UnixMilli(1786377600000),
			task:  db.AgentTaskQueue{Context: []byte(`{"completion_callback":{"telemetry_url":"https://router.example.test/api/v1/dispatch-tasks/router-task-1/llm-traces","telemetry_token":"task-capability","telemetry_expires_at":1786464000000}}`)},
			match: errLLMTraceUnavailable,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			router := &fakeLLMTraceRouter{status: http.StatusCreated}
			_, err := relayTaskLLMTrace(
				context.Background(),
				test.task,
				[]byte(`{"llm_trace":{"enabled":true,"sink_url":""}}`),
				[]byte(`{}`),
				test.now,
				router,
				nil,
				nil,
			)
			if !errors.Is(err, test.match) {
				t.Fatalf("error=%v want=%v", err, test.match)
			}
			if router.calls != 0 {
				t.Fatalf("router calls=%d", router.calls)
			}
		})
	}
}

func TestRelayTaskLLMTraceFansOutToRouterAndAgentSink(t *testing.T) {
	router := &fakeLLMTraceRouter{status: http.StatusCreated}
	sink := &fakeLLMTraceSink{status: http.StatusNoContent}
	task := db.AgentTaskQueue{Context: []byte(`{
		"completion_callback": {
			"telemetry_url": "/api/v1/dispatch-tasks/router-task-1/llm-traces",
			"telemetry_token": "task-capability",
			"telemetry_expires_at": 1786464000000
		}
	}`)}
	payload := []byte(`{"sequence":2,"request":{"body":"request"},"response":{"body":"response"}}`)

	status, err := relayTaskLLMTrace(
		context.Background(),
		task,
		[]byte(`{"llm_trace":{"enabled":true,"sink_url":"https://trace.example.test/ingest"}}`),
		payload,
		time.UnixMilli(1786377600000),
		router,
		sink,
		nil,
	)
	if err != nil || status != http.StatusNoContent {
		t.Fatalf("status=%d err=%v", status, err)
	}
	if router.calls != 1 || sink.calls != 1 {
		t.Fatalf("router calls=%d sink calls=%d", router.calls, sink.calls)
	}
	if sink.url != "https://trace.example.test/ingest" || string(sink.payload) != string(payload) {
		t.Fatalf("sink = %#v", sink)
	}
}

func TestRelayTaskLLMTraceSupportsAgentSinkWithoutRouterCallback(t *testing.T) {
	sink := &fakeLLMTraceSink{status: http.StatusAccepted}
	payload := []byte(`{"sequence":3}`)

	status, err := relayTaskLLMTrace(
		context.Background(),
		db.AgentTaskQueue{Context: []byte(`{}`)},
		[]byte(`{"llm_trace":{"enabled":true,"sink_url":"https://trace.example.test/ingest"}}`),
		payload,
		time.UnixMilli(1786377600000),
		nil,
		sink,
		nil,
	)
	if err != nil || status != http.StatusNoContent {
		t.Fatalf("status=%d err=%v", status, err)
	}
	if sink.calls != 1 || string(sink.payload) != string(payload) {
		t.Fatalf("sink = %#v", sink)
	}
}

func TestHTTPTraceExternalSinkPostsUnchangedBodyWithoutAuthorization(t *testing.T) {
	payload := []byte(`{"sequence":4,"request":{"body":"secret"}}`)
	var received []byte
	var authorization string
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		received, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer receiver.Close()

	status, err := newHTTPTraceExternalSink().SubmitLLMTrace(
		context.Background(),
		receiver.URL,
		payload,
	)
	if err != nil || status != http.StatusAccepted {
		t.Fatalf("status=%d err=%v", status, err)
	}
	if authorization != "" {
		t.Fatalf("external sink received Authorization %q", authorization)
	}
	if string(received) != string(payload) {
		t.Fatalf("payload=%s want=%s", received, payload)
	}
}
