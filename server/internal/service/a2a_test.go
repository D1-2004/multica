package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/jackc/pgx/v5/pgtype"
	a2aintegration "github.com/multica-ai/multica/server/internal/integrations/a2a"
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
		name                  string
		mutate                func(*a2a.SendMessageRequest)
		wantErr               error
		wantReturnImmediately bool
	}{
		{name: "valid", wantReturnImmediately: true},
		{name: "missing message", mutate: func(request *a2a.SendMessageRequest) { request.Message = nil }, wantErr: a2a.ErrInvalidParams},
		{name: "explicit blocking", mutate: func(request *a2a.SendMessageRequest) { request.Config.ReturnImmediately = false }},
		{name: "default blocking", mutate: func(request *a2a.SendMessageRequest) { request.Config = nil }},
		{name: "agent role", mutate: func(request *a2a.SendMessageRequest) { request.Message.Role = a2a.MessageRoleAgent }, wantErr: a2a.ErrInvalidParams},
		{name: "missing message id", mutate: func(request *a2a.SendMessageRequest) { request.Message.ID = "" }, wantErr: a2a.ErrInvalidParams},
		{name: "external context", mutate: func(request *a2a.SendMessageRequest) { request.Message.ContextID = "external-context" }, wantReturnImmediately: true},
		{name: "task continuation", mutate: func(request *a2a.SendMessageRequest) { request.Message.TaskID = "external-task" }, wantReturnImmediately: true},
		{name: "raw part", mutate: func(request *a2a.SendMessageRequest) {
			request.Message.Parts = a2a.ContentParts{a2a.NewRawPart([]byte("x"))}
		}, wantReturnImmediately: true},
		{name: "blank text", mutate: func(request *a2a.SendMessageRequest) { request.Message.Parts = a2a.ContentParts{a2a.NewTextPart("  ")} }, wantReturnImmediately: true},
		{name: "unsupported output", mutate: func(request *a2a.SendMessageRequest) {
			request.Config.AcceptedOutputModes = []string{"application/json"}
		}, wantReturnImmediately: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := valid()
			if test.mutate != nil {
				test.mutate(request)
			}
			got, err := validateA2ASendRequest(context.Background(), request, time.Now())
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("validate error = %v, want %v", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.MessageID != "message-1" || len(got.Parts) != len(request.Message.Parts) || got.ReturnImmediately != test.wantReturnImmediately ||
				got.ContextID != request.Message.ContextID || got.TaskID != string(request.Message.TaskID) {
				t.Fatalf("validated send = %+v", got)
			}
			if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(got.Fingerprint) {
				t.Fatalf("fingerprint = %q", got.Fingerprint)
			}
		})
	}
}

func TestA2ASendHistoryIsOptIn(t *testing.T) {
	request := &a2a.SendMessageRequest{
		Message: &a2a.Message{
			ID:    "message-history-default",
			Role:  a2a.MessageRoleUser,
			Parts: a2a.ContentParts{a2a.NewTextPart("do not echo this input")},
		},
	}

	validated, err := validateA2ASendRequest(context.Background(), request, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if validated.HistoryLength != 0 {
		t.Fatalf("default SendMessage history length = %d, want 0", validated.HistoryLength)
	}
	if historyLength := a2aSendHistoryLength(request); historyLength == nil || *historyLength != 0 {
		t.Fatalf("default replay history length = %v, want 0", historyLength)
	}

	explicitHistoryLength := 2
	request.Config = &a2a.SendMessageConfig{HistoryLength: &explicitHistoryLength}
	validated, err = validateA2ASendRequest(context.Background(), request, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if validated.HistoryLength != explicitHistoryLength {
		t.Fatalf("explicit SendMessage history length = %d, want %d", validated.HistoryLength, explicitHistoryLength)
	}
	if historyLength := a2aSendHistoryLength(request); historyLength == nil || *historyLength != explicitHistoryLength {
		t.Fatalf("explicit replay history length = %v, want %d", historyLength, explicitHistoryLength)
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
	validated, err := validateA2ASendRequest(context.Background(), &request, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if validated.MessageID != "wire-message-1" || len(validated.Parts) != 1 {
		t.Fatalf("validated wire request = %+v", validated)
	}
}

func TestValidateA2ASendRequestKeepsDEAPDWSTokenRequestBound(t *testing.T) {
	request := &a2a.SendMessageRequest{
		Config: &a2a.SendMessageConfig{ReturnImmediately: true},
		Message: &a2a.Message{
			ID:    "message-deap",
			Role:  a2a.MessageRoleUser,
			Parts: a2a.ContentParts{a2a.NewTextPart("hello")},
		},
	}
	identity := a2aintegration.InvocationIdentity{DEAPDWSToken: "deap-request-token"}
	ctx := a2aintegration.WithInvocationIdentity(context.Background(), identity)
	if _, err := validateA2ASendRequest(ctx, request, time.Now()); !errors.Is(err, a2a.ErrInvalidParams) {
		t.Fatalf("immediate DEAP request error = %v, want invalid params", err)
	}

	request.Config.ReturnImmediately = false
	validated, err := validateA2ASendRequest(ctx, request, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if validated.Identity.DEAPDWSToken != identity.DEAPDWSToken || validated.ReturnImmediately {
		t.Fatalf("blocking DEAP request = %+v", validated)
	}

	request.Config.ReturnImmediately = true
	identity.RequestBound = true
	ctx = a2aintegration.WithInvocationIdentity(context.Background(), identity)
	if _, err = validateA2ASendRequest(ctx, request, time.Now()); err != nil {
		t.Fatalf("stream-bound DEAP request rejected: %v", err)
	}

	identity.ContextToken = "conflicting-context-token"
	ctx = a2aintegration.WithInvocationIdentity(context.Background(), identity)
	if _, err = validateA2ASendRequest(ctx, request, time.Now()); !errors.Is(err, a2a.ErrInvalidParams) {
		t.Fatalf("conflicting identity error = %v, want invalid params", err)
	}
}

func TestCompleteA2ASendHonorsImmediateTerminalAndCancellation(t *testing.T) {
	t.Parallel()
	svc := &A2AService{}
	submitted := &a2a.Task{
		ID:     "tsk_submitted",
		Status: a2a.TaskStatus{State: a2a.TaskStateSubmitted},
	}

	result, err := svc.completeA2ASend(context.Background(), validatedA2ASend{ReturnImmediately: true}, submitted)
	if err != nil || result != submitted {
		t.Fatalf("immediate result = %#v, error = %v", result, err)
	}

	completed := &a2a.Task{
		ID:     "tsk_completed",
		Status: a2a.TaskStatus{State: a2a.TaskStateCompleted},
	}
	result, err = svc.completeA2ASend(context.Background(), validatedA2ASend{}, completed)
	if err != nil || result != completed {
		t.Fatalf("terminal result = %#v, error = %v", result, err)
	}

	canceledContext, cancel := context.WithCancel(context.Background())
	cancel()
	result, err = svc.completeA2ASend(canceledContext, validatedA2ASend{}, submitted)
	if result != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled blocking result = %#v, error = %v", result, err)
	}
}

func TestWaitForA2ATerminalTaskPollsDurableProjection(t *testing.T) {
	t.Parallel()
	submitted := &a2a.Task{
		ID:     "tsk_poll",
		Status: a2a.TaskStatus{State: a2a.TaskStateSubmitted},
	}
	completed := &a2a.Task{
		ID:     submitted.ID,
		Status: a2a.TaskStatus{State: a2a.TaskStateCompleted},
	}
	loads := 0
	result, err := waitForA2ATerminalTask(context.Background(), submitted, time.Millisecond, func(_ context.Context, taskID a2a.TaskID) (*a2a.Task, error) {
		loads++
		if taskID != submitted.ID {
			t.Fatalf("polled task ID = %q, want %q", taskID, submitted.ID)
		}
		if loads == 1 {
			return submitted, nil
		}
		return completed, nil
	})
	if err != nil {
		t.Fatalf("waitForA2ATerminalTask() error = %v", err)
	}
	if result != completed || loads != 2 {
		t.Fatalf("terminal result = %#v after %d loads", result, loads)
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

	identity := a2aintegration.InvocationIdentity{
		ExtensionDeclared: true,
		ContextToken:      "context-token-a",
		ExpiresAtUnixMS:   1_800_000_000_000,
	}
	first, err := fingerprintA2ASendRequest(newRequest(map[string]any{"a": 1, "b": 2}, "hello"), identity)
	if err != nil {
		t.Fatal(err)
	}
	second, err := fingerprintA2ASendRequest(newRequest(map[string]any{"b": 2, "a": 1}, "hello"), identity)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("equivalent requests produced %q and %q", first, second)
	}
	changed, err := fingerprintA2ASendRequest(newRequest(map[string]any{"a": 1, "b": 2}, "changed"), identity)
	if err != nil {
		t.Fatal(err)
	}
	if first == changed {
		t.Fatal("content change did not change fingerprint")
	}

	changedToken := identity
	changedToken.ContextToken = "context-token-b"
	changed, err = fingerprintA2ASendRequest(newRequest(map[string]any{"a": 1, "b": 2}, "hello"), changedToken)
	if err != nil {
		t.Fatal(err)
	}
	if first == changed {
		t.Fatal("identity token change did not change fingerprint")
	}

	changedExpiry := identity
	changedExpiry.ExpiresAtUnixMS++
	changed, err = fingerprintA2ASendRequest(newRequest(map[string]any{"a": 1, "b": 2}, "hello"), changedExpiry)
	if err != nil {
		t.Fatal(err)
	}
	if first == changed {
		t.Fatal("identity expiry change did not change fingerprint")
	}

	undeclared := identity
	undeclared.ExtensionDeclared = false
	changed, err = fingerprintA2ASendRequest(newRequest(map[string]any{"a": 1, "b": 2}, "hello"), undeclared)
	if err != nil {
		t.Fatal(err)
	}
	if first == changed {
		t.Fatal("identity extension declaration change did not change fingerprint")
	}

	deapIdentity := a2aintegration.InvocationIdentity{DEAPDWSToken: "deap-token-a"}
	deapFirst, err := fingerprintA2ASendRequest(newRequest(map[string]any{"a": 1, "b": 2}, "hello"), deapIdentity)
	if err != nil {
		t.Fatal(err)
	}
	deapIdentity.DEAPDWSToken = "deap-token-b"
	deapChanged, err := fingerprintA2ASendRequest(newRequest(map[string]any{"a": 1, "b": 2}, "hello"), deapIdentity)
	if err != nil {
		t.Fatal(err)
	}
	if deapFirst == deapChanged {
		t.Fatal("DEAP DWS token change did not change fingerprint")
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

func TestTerminalA2AStatusMessageUsesLatestRedactedFailureOutcome(t *testing.T) {
	binding := db.A2aTaskBinding{PublicTaskID: "tsk_public_failure"}
	turns := []db.ListA2ATaskTurnsWithOutcomeRow{
		{
			Sequence:            1,
			LocalTaskStatus:     "failed",
			AssistantResultText: "first redacted failure",
		},
		{
			Sequence:            2,
			LocalTaskStatus:     "completed",
			AssistantResultText: "completed output",
		},
		{
			Sequence:            3,
			LocalTaskStatus:     "failed",
			AssistantResultText: "latest redacted failure",
		},
	}

	encoded, err := terminalA2AStatusMessage(binding, "ctx_public_failure", a2a.TaskStateFailed, turns)
	if err != nil {
		t.Fatal(err)
	}
	var message a2a.Message
	if err := json.Unmarshal(encoded, &message); err != nil {
		t.Fatal(err)
	}
	if message.ID != stableA2AAgentMessageID(binding.PublicTaskID, 3) ||
		message.Role != a2a.MessageRoleAgent ||
		message.TaskID != a2a.TaskID(binding.PublicTaskID) ||
		message.ContextID != "ctx_public_failure" {
		t.Fatalf("status message identity = %+v", message)
	}
	if len(message.Parts) != 1 || message.Parts[0].Text() != "latest redacted failure" {
		t.Fatalf("status message parts = %+v", message.Parts)
	}

	encoded, err = terminalA2AStatusMessage(binding, "ctx_public_failure", a2a.TaskStateCompleted, turns)
	if err != nil {
		t.Fatal(err)
	}
	if encoded != nil {
		t.Fatalf("completed status message = %s, want nil", encoded)
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

func TestA2AQueuedExternalIdentityNeedsAuth(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		context string
		want    bool
	}{
		{name: "no external identity", context: `{"multica_origin":"a2a"}`},
		{name: "fresh", context: fmt.Sprintf(`{"multica_origin":"a2a","agent_identity_context_token":"token","agent_identity_context_token_expires_at":%d,"agent_identity_context_token_source":"external"}`, now.Add(2*time.Minute).UnixMilli())},
		{name: "inside safety window", context: fmt.Sprintf(`{"multica_origin":"a2a","agent_identity_context_token":"token","agent_identity_context_token_expires_at":%d,"agent_identity_context_token_source":"external"}`, now.Add(59*time.Second).UnixMilli()), want: true},
		{name: "expired", context: `{"multica_origin":"a2a","agent_identity_context_token":"token","agent_identity_context_token_expires_at":1,"agent_identity_context_token_source":"external"}`, want: true},
		{name: "missing token", context: `{"multica_origin":"a2a","agent_identity_context_token_expires_at":4102444800000,"agent_identity_context_token_source":"external"}`, want: true},
		{name: "malformed", context: `{`, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := a2AQueuedExternalIdentityNeedsAuth(context.Background(), []byte(test.context), now); got != test.want {
				t.Fatalf("a2AQueuedExternalIdentityNeedsAuth() = %v, want %v", got, test.want)
			}
		})
	}

	directContext := []byte(a2aTaskDEAPDWSContextJSON)
	if !a2AQueuedExternalIdentityNeedsAuth(context.Background(), directContext, now) {
		t.Fatal("DEAP DWS turn without its live request token did not require auth")
	}
	requestContext := a2aintegration.WithInvocationIdentity(
		context.Background(),
		a2aintegration.InvocationIdentity{DEAPDWSToken: "deap-request-token"},
	)
	if a2AQueuedExternalIdentityNeedsAuth(requestContext, directContext, now) {
		t.Fatal("DEAP DWS turn rejected its live request token")
	}
}
