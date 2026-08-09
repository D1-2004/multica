package service

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestValidateA2ASendRequest(t *testing.T) {
	valid := func() *a2a.SendMessageRequest {
		return &a2a.SendMessageRequest{
			Config: &a2a.SendMessageConfig{ReturnImmediately: true},
			Message: &a2a.Message{
				ID:    "message-1",
				Role:  a2a.MessageRoleUser,
				Parts: a2a.ContentParts{a2a.NewTextPart("first"), a2a.NewTextPart("second")},
			},
		}
	}

	tests := []struct {
		name    string
		mutate  func(*a2a.SendMessageRequest)
		wantErr error
	}{
		{name: "valid"},
		{name: "missing message", mutate: func(request *a2a.SendMessageRequest) { request.Message = nil }, wantErr: a2a.ErrInvalidParams},
		{name: "must return immediately", mutate: func(request *a2a.SendMessageRequest) { request.Config.ReturnImmediately = false }, wantErr: a2a.ErrUnsupportedOperation},
		{name: "agent role", mutate: func(request *a2a.SendMessageRequest) { request.Message.Role = a2a.MessageRoleAgent }, wantErr: a2a.ErrInvalidParams},
		{name: "missing message id", mutate: func(request *a2a.SendMessageRequest) { request.Message.ID = "" }, wantErr: a2a.ErrInvalidParams},
		{name: "external context", mutate: func(request *a2a.SendMessageRequest) { request.Message.ContextID = "external-context" }, wantErr: a2a.ErrUnsupportedOperation},
		{name: "task continuation", mutate: func(request *a2a.SendMessageRequest) { request.Message.TaskID = "external-task" }, wantErr: a2a.ErrUnsupportedOperation},
		{name: "raw part", mutate: func(request *a2a.SendMessageRequest) {
			request.Message.Parts = a2a.ContentParts{a2a.NewRawPart([]byte("x"))}
		}, wantErr: a2a.ErrUnsupportedContentType},
		{name: "blank text", mutate: func(request *a2a.SendMessageRequest) { request.Message.Parts = a2a.ContentParts{a2a.NewTextPart("  ")} }, wantErr: a2a.ErrInvalidParams},
		{name: "unsupported output", mutate: func(request *a2a.SendMessageRequest) {
			request.Config.AcceptedOutputModes = []string{"application/json"}
		}, wantErr: a2a.ErrUnsupportedContentType},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := valid()
			if test.mutate != nil {
				test.mutate(request)
			}
			got, err := validateA2ASendRequest(request)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("validate error = %v, want %v", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.MessageID != "message-1" || got.Content != "first\nsecond" {
				t.Fatalf("validated send = %+v", got)
			}
			if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(got.Fingerprint) {
				t.Fatalf("fingerprint = %q", got.Fingerprint)
			}
		})
	}
}

func TestValidateA2ASendRequestFromV1JSONWire(t *testing.T) {
	var request a2a.SendMessageRequest
	if err := json.Unmarshal([]byte(`{
		"configuration":{"returnImmediately":true,"acceptedOutputModes":["text/plain"]},
		"message":{"messageId":"wire-message-1","role":"ROLE_USER","parts":[{"text":"hello from wire"}]}
	}`), &request); err != nil {
		t.Fatal(err)
	}
	validated, err := validateA2ASendRequest(&request)
	if err != nil {
		t.Fatal(err)
	}
	if validated.MessageID != "wire-message-1" || validated.Content != "hello from wire" {
		t.Fatalf("validated wire request = %+v", validated)
	}
}

func TestFingerprintA2ASendRequestIsCanonicalAndContentSensitive(t *testing.T) {
	newRequest := func(metadata map[string]any, text string) *a2a.SendMessageRequest {
		return &a2a.SendMessageRequest{
			Config:   &a2a.SendMessageConfig{ReturnImmediately: true},
			Metadata: metadata,
			Message: &a2a.Message{
				ID:    "message-1",
				Role:  a2a.MessageRoleUser,
				Parts: a2a.ContentParts{a2a.NewTextPart(text)},
			},
		}
	}

	first, err := fingerprintA2ASendRequest(newRequest(map[string]any{"a": 1, "b": 2}, "hello"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := fingerprintA2ASendRequest(newRequest(map[string]any{"b": 2, "a": 1}, "hello"))
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("equivalent requests produced %q and %q", first, second)
	}
	changed, err := fingerprintA2ASendRequest(newRequest(map[string]any{"a": 1, "b": 2}, "changed"))
	if err != nil {
		t.Fatal(err)
	}
	if first == changed {
		t.Fatal("content change did not change fingerprint")
	}
}

func TestProjectA2ATaskState(t *testing.T) {
	tests := []struct {
		localStatus     string
		cancelRequested bool
		want            a2a.TaskState
		wantErr         bool
	}{
		{localStatus: "queued", want: a2a.TaskStateSubmitted},
		{localStatus: "deferred", want: a2a.TaskStateSubmitted},
		{localStatus: "dispatched", want: a2a.TaskStateWorking},
		{localStatus: "running", want: a2a.TaskStateWorking},
		{localStatus: "waiting_local_directory", want: a2a.TaskStateWorking},
		{localStatus: "completed", want: a2a.TaskStateCompleted},
		{localStatus: "failed", want: a2a.TaskStateFailed},
		{localStatus: "cancelled", want: a2a.TaskStateCanceled},
		{localStatus: "running", cancelRequested: true, want: a2a.TaskStateCanceled},
		{localStatus: "unknown", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.localStatus, func(t *testing.T) {
			got, err := projectA2ATaskState(test.localStatus, test.cancelRequested)
			if (err != nil) != test.wantErr {
				t.Fatalf("project state error = %v, wantErr %v", err, test.wantErr)
			}
			if !test.wantErr && got != test.want {
				t.Fatalf("project state = %q, want %q", got, test.want)
			}
		})
	}
}

func TestProjectA2ACompletedTaskUsesStablePublicIDsAndOutputArtifact(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	row := db.GetA2ATaskProjectionForClientRow{
		PublicTaskID:        "tsk_public_1234567890",
		PublicContextID:     "ctx_public_1234567890",
		ArtifactID:          "art_public_1234567890",
		TaskStatus:          "completed",
		TaskResult:          []byte(`{"task_id":"local-task-secret","output":"raw local output"}`),
		TaskCreatedAt:       pgtype.Timestamptz{Time: now.Add(-time.Minute), Valid: true},
		TaskCompletedAt:     pgtype.Timestamptz{Time: now, Valid: true},
		TaskError:           pgtype.Text{String: "/private/workdir/local-secret", Valid: true},
		AssistantResultText: "redacted finished",
	}

	task, err := projectA2ATask(row)
	if err != nil {
		t.Fatal(err)
	}
	if task.ID != a2a.TaskID(row.PublicTaskID) || task.ContextID != row.PublicContextID {
		t.Fatalf("task identity = (%q, %q)", task.ID, task.ContextID)
	}
	if task.Status.State != a2a.TaskStateCompleted || task.Status.Timestamp == nil || !task.Status.Timestamp.Equal(now) {
		t.Fatalf("task status = %+v", task.Status)
	}
	if len(task.Artifacts) != 1 || task.Artifacts[0].ID != a2a.ArtifactID(row.ArtifactID) {
		t.Fatalf("artifacts = %+v", task.Artifacts)
	}
	if len(task.Artifacts[0].Parts) != 1 || task.Artifacts[0].Parts[0].Text() != "redacted finished" {
		t.Fatalf("artifact parts = %+v", task.Artifacts[0].Parts)
	}
	encoded, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "local-task-secret") || strings.Contains(string(encoded), "local-secret") || strings.Contains(string(encoded), "raw local output") {
		t.Fatalf("projection leaked local data: %s", encoded)
	}
}

func TestNewA2APublicIDIsOpaqueAndPathSafe(t *testing.T) {
	first, err := newA2APublicID("tsk_")
	if err != nil {
		t.Fatal(err)
	}
	second, err := newA2APublicID("tsk_")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("random ids unexpectedly match")
	}
	if !regexp.MustCompile(`^tsk_[A-Za-z0-9_-]{32}$`).MatchString(first) {
		t.Fatalf("public id = %q", first)
	}
}
