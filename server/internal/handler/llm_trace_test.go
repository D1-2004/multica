package handler

import (
	"context"
	"errors"
	"net/http"
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
		"Bearer task-capability",
		payload,
		time.UnixMilli(1786377600000),
		router,
	)
	if err != nil || status != http.StatusCreated {
		t.Fatalf("status=%d err=%v", status, err)
	}
	if router.calls != 1 ||
		router.path != "/api/v1/dispatch-tasks/router-task-1/llm-traces" ||
		router.capability != "task-capability" || string(router.payload) != string(payload) {
		t.Fatalf("relay = %#v", router)
	}
}

func TestRelayTaskLLMTraceRejectsWrongOrExpiredCapability(t *testing.T) {
	baseTask := db.AgentTaskQueue{Context: []byte(`{
		"completion_callback": {
			"telemetry_url": "/api/v1/dispatch-tasks/router-task-1/llm-traces",
			"telemetry_token": "task-capability",
			"telemetry_expires_at": 1786464000000
		}
	}`)}
	for _, test := range []struct {
		name  string
		auth  string
		now   time.Time
		task  db.AgentTaskQueue
		match error
	}{
		{name: "wrong token", auth: "Bearer wrong", now: time.UnixMilli(1786377600000), task: baseTask, match: errLLMTraceUnauthorized},
		{name: "expired", auth: "Bearer task-capability", now: time.UnixMilli(1786464000000), task: baseTask, match: errLLMTraceExpired},
		{
			name: "absolute callback",
			auth: "Bearer task-capability",
			now:  time.UnixMilli(1786377600000),
			task: db.AgentTaskQueue{Context: []byte(`{"completion_callback":{"telemetry_url":"https://router.example.test/api/v1/dispatch-tasks/router-task-1/llm-traces","telemetry_token":"task-capability","telemetry_expires_at":1786464000000}}`)},
			match: errLLMTraceUnavailable,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			router := &fakeLLMTraceRouter{status: http.StatusCreated}
			_, err := relayTaskLLMTrace(context.Background(), test.task, test.auth, []byte(`{}`), test.now, router)
			if !errors.Is(err, test.match) {
				t.Fatalf("error=%v want=%v", err, test.match)
			}
			if router.calls != 0 {
				t.Fatalf("router calls=%d", router.calls)
			}
		})
	}
}
