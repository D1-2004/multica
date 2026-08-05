package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/integrations/dingtalk"
)

// DingTalk-install handler unit tests focus on the no-config
// short-circuits — verifying that a deployment without
// MULTICA_DINGTALK_SECRET_KEY does NOT serve revoke / install, and that
// list degrades gracefully to an empty response so the Integrations tab
// still renders. Happy-path flows (begin device-flow + poll status)
// need a real DB and are covered by the dingtalk package's service
// tests plus the migration-suite integration tests.

func TestRevokeDingTalkInstallation_NotConfigured(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest(http.MethodDelete, "/api/workspaces/x/dingtalk/installations/y", nil)
	w := httptest.NewRecorder()
	h.RevokeDingTalkInstallation(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
}

func TestDingTalkInstallStatusToResponseMarksApproving(t *testing.T) {
	installationID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	resp := dingTalkInstallStatusToResponse(dingtalk.RegistrationSessionState{
		Status:             dingtalk.RegistrationStatusSuccess,
		InstallationID:     installationID,
		RegistrationStatus: "APPROVING",
	})
	if resp.Status != "approving" {
		t.Fatalf("status = %q, want approving", resp.Status)
	}
	if resp.InstallationID == "" {
		t.Fatal("approving response must retain installation_id")
	}
}

func TestBeginDingTalkInstall_NotConfigured(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest(http.MethodPost, "/api/workspaces/x/dingtalk/install/begin?agent_id=y", nil)
	w := httptest.NewRecorder()
	h.BeginDingTalkInstall(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestManualInstallDingTalk_NotConfigured(t *testing.T) {
	// Manual install is gated on the at-rest key (DingTalkInstallations),
	// NOT on the device-flow RegistrationService — but with neither wired
	// it must still 503 rather than panic on a nil InstallationService.
	h := &Handler{}
	req := httptest.NewRequest(http.MethodPost, "/api/workspaces/x/dingtalk/install/manual", nil)
	w := httptest.NewRecorder()
	h.ManualInstallDingTalk(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestGetDingTalkInstallStatus_NotConfigured(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest(http.MethodGet, "/api/workspaces/x/dingtalk/install/sess_y/status", nil)
	w := httptest.NewRecorder()
	h.GetDingTalkInstallStatus(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
}

func TestListDingTalkInstallations_NotConfiguredReturnsEmpty(t *testing.T) {
	// Listing is intentionally a "soft" endpoint: when dingtalk is not
	// configured we return an empty list + configured:false rather than
	// a 503, so the Integrations tab renders normally with a "not
	// connected" empty state instead of an error banner.
	h := &Handler{}
	req := httptest.NewRequest(http.MethodGet, "/api/workspaces/x/dingtalk/installations", nil)
	w := httptest.NewRecorder()
	h.ListDingTalkInstallations(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Installations    []any `json:"installations"`
		Configured       bool  `json:"configured"`
		InstallSupported bool  `json:"install_supported"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Configured {
		t.Fatalf("configured should be false when DingTalkInstallations is nil")
	}
	if resp.InstallSupported {
		t.Fatalf("install_supported should be false when DingTalkInstallations is nil")
	}
	if len(resp.Installations) != 0 {
		t.Fatalf("expected empty installations list, got %d", len(resp.Installations))
	}
}

func TestManualInstallDingTalkAllowsAgentOwnerAndRejectsOtherMember(t *testing.T) {
	ctx := context.Background()
	installations := configureDingTalkChatDispatchForTest(t)
	if installations == nil {
		t.Fatal("dingtalk installations not configured")
	}

	var memberUserID, ownAgentID, foreignAgentID string
	if err := testPool.QueryRow(ctx, `
INSERT INTO "user" (name, email, principal_type)
VALUES ('DingTalk Member Installer', 'dingtalk-member-installer-' || gen_random_uuid()::text || '@multica.ai', 'workspace_access_token')
RETURNING id
`).Scan(&memberUserID); err != nil {
		t.Fatalf("create member user: %v", err)
	}
	if _, err := testPool.Exec(ctx, `INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'member')`, testWorkspaceID, memberUserID); err != nil {
		t.Fatalf("create member: %v", err)
	}
	createAgent := func(ownerID, name string) string {
		t.Helper()
		var agentID string
		if err := testPool.QueryRow(ctx, `
INSERT INTO agent (
    workspace_id, name, description, runtime_mode, runtime_config,
    runtime_id, visibility, permission_mode, max_concurrent_tasks, owner_id
) VALUES ($1, $2, '', 'cloud', '{}'::jsonb, $3, 'workspace', 'private', 1, $4)
RETURNING id
`, testWorkspaceID, name, testRuntimeID, ownerID).Scan(&agentID); err != nil {
			t.Fatalf("create agent: %v", err)
		}
		return agentID
	}
	ownAgentID = createAgent(memberUserID, "dingtalk-member-own-agent-"+memberUserID)
	foreignAgentID = createAgent(testUserID, "dingtalk-member-foreign-agent-"+memberUserID)
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM channel_installation WHERE client_id = ANY($1)`, []string{"member-own-app-" + memberUserID, "member-foreign-app-" + memberUserID, "member-existing-foreign-app-" + memberUserID})
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent WHERE id = ANY($1)`, []string{ownAgentID, foreignAgentID})
		_, _ = testPool.Exec(context.Background(), `DELETE FROM member WHERE workspace_id = $1 AND user_id = $2`, testWorkspaceID, memberUserID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, memberUserID)
	})

	call := func(agentID, suffix string) *httptest.ResponseRecorder {
		t.Helper()
		req := newRequest(http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/dingtalk/install/manual", map[string]any{
			"agent_id":      agentID,
			"client_id":     "member-" + suffix + "-app-" + memberUserID,
			"client_secret": "secret-" + suffix,
			"robot_code":    "robot-" + suffix + "-" + memberUserID,
		})
		req.Header.Set("X-User-ID", memberUserID)
		req.Header.Set("X-Workspace-ID", testWorkspaceID)
		req = withURLParam(req, "id", testWorkspaceID)
		rec := httptest.NewRecorder()
		testHandler.ManualInstallDingTalk(rec, req)
		return rec
	}

	ownRec := call(ownAgentID, "own")
	if ownRec.Code != http.StatusOK {
		t.Fatalf("own agent install: expected 200, got %d: %s", ownRec.Code, ownRec.Body.String())
	}
	var ownInstallation DingTalkInstallationResponse
	if err := json.NewDecoder(ownRec.Body).Decode(&ownInstallation); err != nil {
		t.Fatalf("decode own installation: %v", err)
	}
	revoke := func(installationID string) *httptest.ResponseRecorder {
		t.Helper()
		req := newRequest(http.MethodDelete, "/api/workspaces/"+testWorkspaceID+"/dingtalk/installations/"+installationID, nil)
		req.Header.Set("X-User-ID", memberUserID)
		req.Header.Set("X-Workspace-ID", testWorkspaceID)
		req = withURLParams(req, "id", testWorkspaceID, "installationId", installationID)
		rec := httptest.NewRecorder()
		testHandler.RevokeDingTalkInstallation(rec, req)
		return rec
	}
	if rec := revoke(ownInstallation.ID); rec.Code != http.StatusNoContent {
		t.Fatalf("own installation revoke: expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
	foreignInstallation, err := installations.Upsert(ctx, dingtalk.InstallationParams{
		WorkspaceID:     parseUUID(testWorkspaceID),
		AgentID:         parseUUID(foreignAgentID),
		ClientID:        "member-existing-foreign-app-" + memberUserID,
		ClientSecret:    "secret-existing-foreign",
		RobotCode:       "robot-existing-foreign-" + memberUserID,
		InstallerUserID: parseUUID(testUserID),
	})
	if err != nil {
		t.Fatalf("create foreign installation: %v", err)
	}
	if rec := revoke(uuidToString(foreignInstallation.ID)); rec.Code != http.StatusForbidden {
		t.Fatalf("foreign installation revoke: expected 403, got %d: %s", rec.Code, rec.Body.String())
	}

	if rec := call(foreignAgentID, "foreign"); rec.Code != http.StatusForbidden {
		t.Fatalf("foreign agent install: expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}
