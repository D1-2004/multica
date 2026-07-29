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

	"github.com/multica-ai/multica/server/internal/service"
)

type enterpriseIdentityStatusTestResponse struct {
	Configured bool `json:"configured"`
	CanManage  bool `json:"can_manage"`
	Identity   *struct {
		EmployeeID          string `json:"employee_id"`
		DisplayName         string `json:"display_name"`
		Status              string `json:"status"`
		AIPID               string `json:"aip_id"`
		AgentSPIFFEID       string `json:"agent_spiffe_id"`
		BUCStatus           string `json:"buc_status"`
		AgentIdentityStatus string `json:"agent_identity_status"`
	} `json:"identity"`
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
			buc_anchor_sandbox_id,
			authx_refresh_token_encrypted,
			authx_refresh_expires_at,
			status,
			bound_by
		)
		VALUES ($1, $2, '12345', 'Zhang San', 'buc-agent-1', $3, 'aip-1',
			'anchor-1', $4, now() + interval '24 hours', 'active', $5)
	`, testWorkspaceID, agentID,
		"spiffe://agents.example/ns/multica/agents/"+agentID,
		bytes.Repeat([]byte{0x42}, 32),
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

func TestAgentEnterpriseIdentityMemberCannotTestBinding(t *testing.T) {
	agentID, memberUserID := createEnterpriseIdentityHandlerFixture(t)
	handler := *testHandler
	handler.EnterpriseIdentity = &service.EnterpriseIdentityService{}

	request := newRequestAsUser(
		memberUserID,
		http.MethodPost,
		"/api/workspaces/"+testWorkspaceID+"/agent-identity/enterprise/test?agent_id="+agentID,
		nil,
	)
	request = withURLParams(request, "id", testWorkspaceID)
	response := httptest.NewRecorder()
	handler.TestAgentEnterpriseIdentity(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", response.Code, response.Body.String())
	}
}
