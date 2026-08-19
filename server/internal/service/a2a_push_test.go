package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestValidateA2APushConfig(t *testing.T) {
	valid := func() *a2a.PushConfig {
		return &a2a.PushConfig{
			TaskID: "task-1",
			URL:    "https://push.example/webhook",
			Token:  "notification-token",
			Auth:   &a2a.PushAuthInfo{Scheme: "bearer", Credentials: "callback-secret"},
		}
	}
	if err := validateA2APushConfig(valid(), "task-1"); err != nil {
		t.Fatalf("valid push config: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*a2a.PushConfig)
	}{
		{name: "task mismatch", mutate: func(config *a2a.PushConfig) { config.TaskID = "other" }},
		{name: "HTTP callback", mutate: func(config *a2a.PushConfig) { config.URL = "http://push.example/webhook" }},
		{name: "private callback", mutate: func(config *a2a.PushConfig) { config.URL = "https://127.0.0.1/webhook" }},
		{name: "callback credentials", mutate: func(config *a2a.PushConfig) { config.URL = "https://user:password@push.example/webhook" }},
		{name: "unsupported auth", mutate: func(config *a2a.PushConfig) { config.Auth.Scheme = "digest" }},
		{name: "missing auth credentials", mutate: func(config *a2a.PushConfig) { config.Auth.Credentials = "" }},
		{name: "newline token", mutate: func(config *a2a.PushConfig) { config.Token = "secret\nheader" }},
		{name: "newline credentials", mutate: func(config *a2a.PushConfig) { config.Auth.Credentials = "secret\r\nheader" }},
		{name: "tenant", mutate: func(config *a2a.PushConfig) { config.Tenant = "tenant-1" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := valid()
			test.mutate(config)
			if err := validateA2APushConfig(config, "task-1"); err == nil {
				t.Fatal("invalid push config was accepted")
			}
		})
	}
}

func TestA2APushWorkerDeliversEncryptedBearerAndNotificationToken(t *testing.T) {
	box, err := secretbox.New(make([]byte, secretbox.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	notificationToken, err := box.Seal([]byte("notification-secret"))
	if err != nil {
		t.Fatal(err)
	}
	authCredentials, err := box.Seal([]byte("bearer-secret"))
	if err != nil {
		t.Fatal(err)
	}
	eventPayload, err := json.Marshal(&a2a.TaskStatusUpdateEvent{
		TaskID: "task-1", ContextID: "context-1",
		Status:   a2a.TaskStatus{State: a2a.TaskStateCompleted},
		Metadata: map[string]any{"eventId": "event-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var requestBody []byte
	var authorization string
	var notification string
	client := &http.Client{Transport: a2aRoundTripperFunc(func(request *http.Request) (*http.Response, error) {
		requestBody, err = io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		authorization = request.Header.Get("Authorization")
		notification = request.Header.Get("A2A-Notification-Token")
		return &http.Response{
			StatusCode: http.StatusNoContent,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    request,
		}, nil
	})}
	worker := &A2APushWorker{Service: &A2AService{PushSecrets: box}, Client: client}
	err = worker.deliver(context.Background(), db.ClaimNextA2APushDeliveryRow{
		CallbackUrl:                "https://push.example/webhook",
		NotificationTokenEncrypted: notificationToken,
		AuthScheme:                 pgtype.Text{String: "bearer", Valid: true},
		AuthCredentialsEncrypted:   authCredentials,
		EventType:                  "status",
		EventPayload:               eventPayload,
	})
	if err != nil {
		t.Fatalf("deliver() error = %v", err)
	}
	if authorization != "Bearer bearer-secret" || notification != "notification-secret" {
		t.Fatalf("headers Authorization=%q notification=%q", authorization, notification)
	}
	if strings.Contains(string(requestBody), "bearer-secret") || strings.Contains(string(requestBody), "notification-secret") || !strings.Contains(string(requestBody), "event-1") {
		t.Fatalf("push payload leaked secrets or lost event id: %s", requestBody)
	}
}

func TestA2APushRetryScheduleMatchesContract(t *testing.T) {
	want := []time.Duration{
		5 * time.Second,
		30 * time.Second,
		2 * time.Minute,
		10 * time.Minute,
		30 * time.Minute,
		2 * time.Hour,
		6 * time.Hour,
		24 * time.Hour,
	}
	if len(a2aPushRetrySchedule) != len(want) {
		t.Fatalf("retry schedule length = %d, want %d", len(a2aPushRetrySchedule), len(want))
	}
	for index := range want {
		if a2aPushRetrySchedule[index] != want[index] {
			t.Fatalf("retry %d = %s, want %s", index, a2aPushRetrySchedule[index], want[index])
		}
	}
}

func TestPublicA2APushConfigNeverReturnsSecrets(t *testing.T) {
	config := publicA2APushConfig("task-1", db.A2aPushConfig{
		PublicConfigID:             "push-1",
		CallbackUrl:                "https://push.example/webhook",
		NotificationTokenEncrypted: []byte("encrypted-token"),
		AuthScheme:                 pgtype.Text{String: "basic", Valid: true},
		AuthCredentialsEncrypted:   []byte("encrypted-credentials"),
	})
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if config.Token != "" || config.Auth == nil || config.Auth.Credentials != "" || strings.Contains(string(encoded), "encrypted") {
		t.Fatalf("public config exposed secrets: %s", encoded)
	}
}
