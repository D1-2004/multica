package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
)

type enterpriseIdentityStatusTestResponse struct {
	Configured     bool  `json:"configured"`
	CanManage      bool  `json:"can_manage"`
	BindingVersion int64 `json:"binding_version"`
	Identity       *struct {
		EmployeeID          string `json:"employee_id"`
		DisplayName         string `json:"display_name"`
		Status              string `json:"status"`
		AIPID               string `json:"aip_id"`
		AgentSPIFFEID       string `json:"agent_spiffe_id"`
		BUCStatus           string `json:"buc_status"`
		AgentIdentityStatus string `json:"agent_identity_status"`
	} `json:"identity"`
}

type fakeEnterpriseIdentityHandlerService struct{}

func (f *fakeEnterpriseIdentityHandlerService) Run(context.Context) {}

func (f *fakeEnterpriseIdentityHandlerService) StartBinding(
	context.Context,
	service.StartEnterpriseIdentityBindingInput,
) (service.StartEnterpriseIdentityBindingResult, error) {
	return service.StartEnterpriseIdentityBindingResult{}, nil
}

func (f *fakeEnterpriseIdentityHandlerService) PrepareBindingCompletion(
	context.Context,
	string,
) (service.PreparedEnterpriseIdentityBinding, error) {
	return service.PreparedEnterpriseIdentityBinding{}, nil
}

func (f *fakeEnterpriseIdentityHandlerService) CompletePreparedBinding(
	context.Context,
	service.PreparedEnterpriseIdentityBinding,
	string,
) (service.CompleteEnterpriseIdentityBindingResult, error) {
	return service.CompleteEnterpriseIdentityBindingResult{}, nil
}

func (f *fakeEnterpriseIdentityHandlerService) Revoke(
	context.Context,
	pgtype.UUID,
	pgtype.UUID,
) error {
	return nil
}

func createEnterpriseIdentityHandlerFixture(t *testing.T) (agentID string, memberUserID string) {
	t.Helper()
	if testHandler == nil {
		t.Skip("database not available")
	}

	ctx := context.Background()
	suffix := time.Now().UnixNano()
	if err := testPool.QueryRow(ctx, `
		INSERT INTO "user" (name, email)
		VALUES ($1, $2)
		RETURNING id
	`, "Enterprise Identity Member", fmt.Sprintf("enterprise-identity-%d@multica.ai", suffix)).Scan(&memberUserID); err != nil {
		t.Fatalf("create member user: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
		INSERT INTO member (workspace_id, user_id, role)
		VALUES ($1, $2, 'member')
	`, testWorkspaceID, memberUserID); err != nil {
		t.Fatalf("create workspace member: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, visibility, permission_mode, max_concurrent_tasks, owner_id
		)
		VALUES ($1, $2, '', 'cloud', '{}'::jsonb, $3, 'workspace', 'public_to', 1, $4)
		RETURNING id
	`, testWorkspaceID, fmt.Sprintf("Enterprise Identity Agent %d", suffix), testRuntimeID, testUserID).Scan(&agentID); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
		INSERT INTO agent_enterprise_identity (
			workspace_id,
			agent_id,
			raw_emp_id,
			display_name,
			buc_agent_id,
			agent_spiffe_id,
			aip_id,
			buc_tokens_encrypted,
			buc_access_expires_at,
			authx_refresh_token_encrypted,
			authx_refresh_expires_at,
			status,
			bound_by
		)
		VALUES ($1, $2, '12345', 'Zhang San', 'buc-agent-1', $3, 'aip-1',
			$4, now() + interval '1 hour', $5, now() + interval '24 hours',
			'active', $6)
	`, testWorkspaceID, agentID,
		"spiffe://agents.example/ns/multica/agents/"+agentID,
		bytes.Repeat([]byte{0x42}, 32),
		bytes.Repeat([]byte{0x43}, 32),
		testUserID,
	); err != nil {
		t.Fatalf("create enterprise identity: %v", err)
	}

	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent WHERE id = $1`, agentID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, memberUserID)
	})
	return agentID, memberUserID
}

func getEnterpriseIdentityStatusForUser(
	t *testing.T,
	handler *Handler,
	userID string,
	agentID string,
) (*httptest.ResponseRecorder, enterpriseIdentityStatusTestResponse) {
	t.Helper()
	request := newRequestAsUser(
		userID,
		http.MethodGet,
		"/api/workspaces/"+testWorkspaceID+"/agent-identity/enterprise/status?agent_id="+agentID,
		nil,
	)
	request = withURLParams(request, "id", testWorkspaceID)
	response := httptest.NewRecorder()
	handler.GetAgentEnterpriseIdentityStatus(response, request)

	var payload enterpriseIdentityStatusTestResponse
	if response.Code == http.StatusOK {
		if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
			t.Fatalf("decode status response: %v", err)
		}
	}
	return response, payload
}

func TestAgentEnterpriseIdentityStatusMasksManagerFields(t *testing.T) {
	agentID, _ := createEnterpriseIdentityHandlerFixture(t)
	handler := *testHandler
	handler.EnterpriseIdentity = &service.EnterpriseIdentityService{}

	response, payload := getEnterpriseIdentityStatusForUser(t, &handler, testUserID, agentID)
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	if !payload.Configured || !payload.CanManage || payload.Identity == nil {
		t.Fatalf("unexpected status response: %#v", payload)
	}
	if payload.Identity.EmployeeID != "*2345" ||
		payload.Identity.DisplayName != "Zhang San" ||
		payload.Identity.AIPID != "aip-1" ||
		payload.Identity.Status != "active" ||
		payload.Identity.BUCStatus != "active" ||
		payload.Identity.AgentIdentityStatus != "active" {
		t.Fatalf("unexpected manager identity: %#v", payload.Identity)
	}
	if strings.Contains(response.Body.String(), `"12345"`) {
		t.Fatal("status response exposed the raw employee ID")
	}
}

func TestAgentEnterpriseIdentityStatusAllowsMemberWithoutIdentifiers(t *testing.T) {
	agentID, memberUserID := createEnterpriseIdentityHandlerFixture(t)
	handler := *testHandler
	handler.EnterpriseIdentity = &service.EnterpriseIdentityService{}

	response, payload := getEnterpriseIdentityStatusForUser(t, &handler, memberUserID, agentID)
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	if !payload.Configured || payload.CanManage || payload.Identity == nil {
		t.Fatalf("unexpected status response: %#v", payload)
	}
	if payload.Identity.Status != "active" ||
		payload.Identity.BUCStatus != "active" ||
		payload.Identity.AgentIdentityStatus != "active" {
		t.Fatalf("unexpected member-visible status: %#v", payload.Identity)
	}
	if payload.Identity.EmployeeID != "" ||
		payload.Identity.DisplayName != "" ||
		payload.Identity.AIPID != "" ||
		payload.Identity.AgentSPIFFEID != "" {
		t.Fatalf("member response exposed identity identifiers: %#v", payload.Identity)
	}
}

func TestAgentEnterpriseIdentityStatusTreatsRevokedBindingAsUnbound(t *testing.T) {
	agentID, _ := createEnterpriseIdentityHandlerFixture(t)
	if _, err := testPool.Exec(
		context.Background(),
		`UPDATE agent_enterprise_identity SET status = 'revoked' WHERE agent_id = $1`,
		agentID,
	); err != nil {
		t.Fatalf("revoke enterprise identity fixture: %v", err)
	}
	handler := *testHandler
	handler.EnterpriseIdentity = &service.EnterpriseIdentityService{}

	response, payload := getEnterpriseIdentityStatusForUser(t, &handler, testUserID, agentID)
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	if !payload.Configured || !payload.CanManage || payload.Identity != nil {
		t.Fatalf("revoked identity should be reported as unbound: %#v", payload)
	}
}

func TestEnterpriseIdentityCallbackProgressPagePollsNewBindingVersion(t *testing.T) {
	t.Parallel()

	page := enterpriseIdentityCallbackProgressPage(
		"nonce",
		"/api/workspaces/workspace-id/agent-identity/enterprise/status?agent_id=agent-id&attempt_id=attempt-id",
		"/settings?enterprise_identity=connected",
		"/settings",
		7,
	)
	for _, expected := range []string{
		"通常需要 1–2 分钟",
		"集团账号权限助手",
		"完成所有“前往授权”",
		"fetch(statusURL",
		"payload.binding_attempt?.status===\"failed\"",
		"showBindingFailure(payload.binding_attempt.error_code)",
		"callback-spinner",
		"本次绑定已经停止，不会继续创建沙箱",
		"href=\"/settings\" hidden",
		"payload.binding_version",
		"expectedBindingVersion=7",
		"window.location.replace(redirectURL)",
		"</body></html>",
	} {
		if !strings.Contains(page, expected) {
			t.Fatalf("callback progress page does not contain %q", expected)
		}
	}
	if strings.Contains(page, "%!") {
		t.Fatalf("callback progress page contains a formatting error: %s", page)
	}
}

func TestEnterpriseIdentitySourceRotationEnvironmentGuard(t *testing.T) {
	cases := []struct {
		environment string
		want        bool
	}{
		{environment: "pre", want: true},
		{environment: "prepub", want: true},
		{environment: "staging", want: true},
		{environment: "production", want: false},
		{environment: "", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.environment, func(t *testing.T) {
			t.Setenv("AONE_ENV_TYPE", tc.environment)
			if got := enterpriseIdentitySourceRotationEnabled(); got != tc.want {
				t.Fatalf("rotation enabled = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRotateAgentEnterpriseIdentitySourceIsHiddenOutsidePrepub(t *testing.T) {
	t.Setenv("AONE_ENV_TYPE", "production")

	handler := &Handler{}
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/workspaces/workspace-id/agent-identity/enterprise/source/rotate",
		strings.NewReader(`{"agent_id":"agent-id"}`),
	)
	response := httptest.NewRecorder()
	handler.RotateAgentEnterpriseIdentitySource(response, request)

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}
