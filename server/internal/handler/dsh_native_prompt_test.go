package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/dshhost"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestDSHNativeInvokeDoesNotGrantAdminPrivateRunAccess(t *testing.T) {
	owner := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	admin := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	agent := db.Agent{ID: pgtype.UUID{Bytes: uuid.New(), Valid: true}, WorkspaceID: pgtype.UUID{Bytes: uuid.New(), Valid: true}, OwnerID: owner, PermissionMode: "private"}
	h := &Handler{}
	q := db.New(nil)
	if err := h.dshNativeInvoke(context.Background(), q, agent, owner); err != nil {
		t.Fatal("owner cannot invoke", err)
	}
	if err := h.dshNativeInvoke(context.Background(), q, agent, admin); !errors.Is(err, dshhost.ErrNativeAccessDenied) {
		t.Fatal("management access bypassed private invocation")
	}
}

func nativePromptHandlerFixture(t *testing.T) (dshhost.NativeAccessManager, string, map[string]any) {
	t.Helper()
	host := dshhost.Host{Key: dshhost.Key{WorkspaceID: uuid.New(), AgentID: uuid.New()}, Generation: 1, SandboxID: "sbx-native-test", State: "running"}
	manager := dshhost.NativeAccessManager{Store: &nativeCallbackStore{}, CheckManage: func(context.Context, dshhost.Key, uuid.UUID) error { return nil }}
	_, entry, err := manager.Issue(context.Background(), host, uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := manager.Exchange(context.Background(), entry, host)
	if err != nil {
		t.Fatal(err)
	}
	sid := uuid.NewString()
	return manager, token, map[string]any{
		"workspace_id": host.WorkspaceID.String(), "agent_id": host.AgentID.String(), "generation": 1, "sandbox_id": host.SandboxID,
		"session_id": sid, "request_id": uuid.NewString(), "workdir": dshhost.MountPath + "/workspaces/" + sid, "content": "native prompt",
	}
}

func TestDSHNativePromptBoundaryRejectsForgedConfiguration(t *testing.T) {
	for _, name := range []string{"user_id", "chat_session_id", "model", "credentials", "workdir", "request_id", "session_id", "extra JSON", "large body", "query credential", "no credential", "duplicate credential", "wrong host"} {
		t.Run(name, func(t *testing.T) {
			manager, token, input := nativePromptHandlerFixture(t)
			status, query := http.StatusBadRequest, ""
			switch name {
			case "workdir":
				input[name] = "/tmp/other"
			case "request_id":
				input[name] = "1"
			case "session_id":
				input[name] = "../other"
			case "query credential":
				query = "?token=fixture"
				status = http.StatusUnauthorized
			case "no credential", "duplicate credential":
				status = http.StatusUnauthorized
			case "wrong host":
				input["generation"] = 2
				status = http.StatusUnauthorized
			case "large body":
				input["content"] = strings.Repeat("x", 2*1024*1024)
			case "extra JSON":
			default:
				input[name] = "untrusted"
			}
			body, _ := json.Marshal(input)
			if name == "extra JSON" {
				body = append(body, []byte(" {}")...)
			}
			r := httptest.NewRequest(http.MethodPost, "/api/dsh-native/prompts"+query, strings.NewReader(string(body)))
			if name != "no credential" {
				r.Header.Set("Authorization", "Bearer "+token)
			}
			if name == "duplicate credential" {
				r.Header.Add("Authorization", "Bearer "+token)
			}
			called := false
			w := httptest.NewRecorder()
			dshNativePromptHandler(w, r, manager, func(context.Context, dshhost.NativeAccess, service.DSHNativeChatInput, string) (dshNativePromptReceipt, error) {
				called = true
				return dshNativePromptReceipt{}, nil
			})
			if w.Code != status || called || w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), token) {
				t.Fatalf("boundary status=%d wanted=%d called=%v", w.Code, status, called)
			}
		})
	}
}

func TestDSHNativePromptUsesGrantIdentityAndReportsReplay(t *testing.T) {
	manager, token, input := nativePromptHandlerFixture(t)
	store := manager.Store.(*nativeCallbackStore)
	u := func() pgtype.UUID { return pgtype.UUID{Bytes: uuid.New(), Valid: true} }
	task, message, session := u(), u(), u()
	calls := 0
	submit := func(_ context.Context, access dshhost.NativeAccess, prompt service.DSHNativeChatInput, content string) (dshNativePromptReceipt, error) {
		if access.UserID != store.record.UserID || access.Key != store.record.Key || content != input["content"] || prompt.SessionID != input["session_id"] || prompt.RequestID.String() != input["request_id"] {
			t.Fatal("native identity changed")
		}
		calls++
		return dshNativePromptReceipt{SessionID: prompt.SessionID, RequestID: prompt.RequestID, ChatSessionID: session, TaskID: task, MessageID: message, Replayed: calls > 1}, nil
	}
	body, _ := json.Marshal(input)
	for _, want := range []int{http.StatusCreated, http.StatusOK} {
		r := httptest.NewRequest(http.MethodPost, "/api/dsh-native/prompts", strings.NewReader(string(body)))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		dshNativePromptHandler(w, r, manager, submit)
		var got dshNativePromptReceipt
		if w.Code != want || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.TaskID != task || got.MessageID != message || got.ChatSessionID != session || strings.Contains(w.Body.String(), token) {
			t.Fatal("invalid admission receipt", w.Code)
		}
	}
	manager.CheckManage = func(context.Context, dshhost.Key, uuid.UUID) error { return errors.New("revoked private detail") }
	r := httptest.NewRequest(http.MethodPost, "/api/dsh-native/prompts", strings.NewReader(string(body)))
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	dshNativePromptHandler(w, r, manager, submit)
	if w.Code != http.StatusUnauthorized || calls != 2 || strings.Contains(w.Body.String(), "private detail") {
		t.Fatal("revoked grant reached task admission")
	}
}

func TestDSHNativePromptAdmissionFailureIsNotSuccess(t *testing.T) {
	for _, item := range []struct {
		err    error
		status int
	}{
		{dshhost.ErrNativeAccessDenied, 403}, {dshhost.ErrChanged, 409}, {service.ErrChatSessionArchived, 409},
		{service.ErrDSHNativeInput, 400}, {errors.New("private backend credential detail"), 503},
	} {
		manager, token, input := nativePromptHandlerFixture(t)
		body, _ := json.Marshal(input)
		r := httptest.NewRequest(http.MethodPost, "/api/dsh-native/prompts", strings.NewReader(string(body)))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		dshNativePromptHandler(w, r, manager, func(context.Context, dshhost.NativeAccess, service.DSHNativeChatInput, string) (dshNativePromptReceipt, error) {
			return dshNativePromptReceipt{}, item.err
		})
		if w.Code != item.status || strings.Contains(w.Body.String(), "private backend") {
			t.Fatalf("error status=%d wanted=%d", w.Code, item.status)
		}
	}
}
