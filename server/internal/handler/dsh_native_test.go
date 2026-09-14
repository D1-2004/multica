package handler

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/dshhost"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type nativeCallbackStore struct {
	record dshhost.NativeAccess
	hash   string
}

func (s *nativeCallbackStore) InsertNativeAccess(_ context.Context, a dshhost.NativeAccess, hash string) (dshhost.NativeAccess, error) {
	a.ExpiresAt = time.Now().Add(time.Minute)
	s.record, s.hash = a, hash
	return a, nil
}
func (s *nativeCallbackStore) GetNativeAccess(_ context.Context, hash, kind string) (dshhost.NativeAccess, error) {
	if hash != s.hash || kind != s.record.Kind {
		return dshhost.NativeAccess{}, dshhost.ErrNativeAccessDenied
	}
	return s.record, nil
}
func (s *nativeCallbackStore) ExchangeNativeAccess(_ context.Context, a dshhost.NativeAccess, entry, session string) (dshhost.NativeAccess, error) {
	if entry != s.hash || s.record.Kind != "entry" {
		return dshhost.NativeAccess{}, dshhost.ErrNativeAccessDenied
	}
	s.record.Kind = "session"
	s.hash = session
	return s.record, nil
}
func (s *nativeCallbackStore) RevokeNativeAccess(context.Context, dshhost.Key, uuid.UUID) error {
	s.record.Kind = "revoked"
	return nil
}

func TestDSHNativeCallbackExchangeScopeAndRevocation(t *testing.T) {
	host := dshhost.Host{Key: dshhost.Key{WorkspaceID: uuid.New(), AgentID: uuid.New()}, Generation: 4, SandboxID: "sbx-test", State: "running"}
	user := uuid.New()
	permitted := true
	checks := 0
	manager := dshhost.NativeAccessManager{Store: &nativeCallbackStore{}, CheckManage: func(_ context.Context, key dshhost.Key, actor uuid.UUID) error {
		checks++
		if !permitted || key != host.Key || actor != user {
			return errors.New("private permission detail")
		}
		return nil
	}}
	_, entry, err := manager.Issue(context.Background(), host, user)
	if err != nil {
		t.Fatal(err)
	}
	scope := map[string]any{"workspace_id": host.WorkspaceID, "agent_id": host.AgentID, "generation": host.Generation, "sandbox_id": host.SandboxID}
	request := func(token string, exchange bool) *httptest.ResponseRecorder {
		b, _ := json.Marshal(scope)
		r := httptest.NewRequest(http.MethodPost, "/api/dsh-native/access/check", strings.NewReader(string(b)))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		dshNativeCallback(w, r, manager, exchange)
		return w
	}
	scope["generation"] = host.Generation + 1
	if w := request(entry, true); w.Code != http.StatusUnauthorized {
		t.Fatal("wrong generation accepted", w.Code)
	}
	scope["generation"] = host.Generation
	w := request(entry, true)
	var response struct {
		SessionToken string `json:"session_token"`
		UserID       string `json:"user_id"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &response) != nil || response.SessionToken == "" || response.UserID != user.String() || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("exchange failed", w.Code)
	}
	if w := request(entry, true); w.Code != http.StatusUnauthorized {
		t.Fatal("entry replay accepted")
	}
	if w := request(response.SessionToken, false); w.Code != http.StatusOK || strings.Contains(w.Body.String(), response.SessionToken) {
		t.Fatal("check failed or returned session secret")
	}
	before := checks
	permitted = false
	if w := request(response.SessionToken, false); w.Code != http.StatusUnauthorized || strings.Contains(w.Body.String(), "private permission detail") {
		t.Fatal("permission revocation ignored or leaked")
	}
	if checks != before+1 {
		t.Fatal("permission was not rechecked")
	}
}

func TestDSHNativeCallbackRejectsAmbiguousBoundary(t *testing.T) {
	body := `{"workspace_id":"00000000-0000-4000-8000-000000000001","agent_id":"00000000-0000-4000-8000-000000000002","generation":1,"sandbox_id":"sbx-test"}`
	for _, fixture := range []struct {
		body    string
		headers []string
		query   string
		status  int
	}{
		{body, nil, "", 401}, {body, []string{"Bearer a", "Bearer b"}, "", 401}, {body, []string{"Bearer a"}, "?token=a", 401},
		{body + ` {}`, []string{"Bearer a"}, "", 400}, {`{"user_id":"untrusted"}`, []string{"Bearer a"}, "", 400},
		{strings.Replace(body, "00000000-0000-4000-8000-000000000001", "bad", 1), []string{"Bearer a"}, "", 400},
	} {
		r := httptest.NewRequest(http.MethodPost, "/api/dsh-native/access/check"+fixture.query, strings.NewReader(fixture.body))
		for _, header := range fixture.headers {
			r.Header.Add("Authorization", header)
		}
		w := httptest.NewRecorder()
		dshNativeCallback(w, r, dshhost.NativeAccessManager{}, false)
		if w.Code != fixture.status {
			t.Fatal("unexpected boundary result", w.Code, fixture.status)
		}
	}
}

func TestDSHNativeEntryRejectsMachinesAndClientRedirect(t *testing.T) {
	h := &Handler{}
	for _, source := range []string{"task_token", "cloud_pat", "workspace_access_token"} {
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		r.Header.Set("X-Actor-Source", source)
		w := httptest.NewRecorder()
		RequireHumanActor(http.HandlerFunc(h.IssueDSHNativeAccess)).ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatal("machine admitted")
		}
	}
	for _, body := range []string{`{"redirect":"https://other.test"}`, `{"sandbox_id":"sbx-other"}`, `{} {}`} {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		w := httptest.NewRecorder()
		h.IssueDSHNativeAccess(w, r)
		if w.Code != http.StatusBadRequest {
			t.Fatal("client configuration admitted")
		}
	}
}
