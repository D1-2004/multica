package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func transferOwnerFixture(t *testing.T, agentName string) (agentID, currentOwnerID string) {
	t.Helper()
	currentOwnerID = createPermissionTestMember(t, agentName+"-owner@multica.test")
	agentID = createHandlerTestAgent(t, agentName, nil)
	if _, err := testPool.Exec(context.Background(), `
		UPDATE agent SET owner_id = $1 WHERE id = $2
	`, currentOwnerID, agentID); err != nil {
		t.Fatalf("assign agent owner: %v", err)
	}
	return agentID, currentOwnerID
}

func TestTransferAgentOwner_CurrentOwnerCanTransfer(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentID, currentOwnerID := transferOwnerFixture(t, "transfer-by-owner")
	targetID := createPermissionTestMember(t, "transfer-by-owner-target@multica.test")

	req := withURLParam(
		newRequestAs(currentOwnerID, http.MethodPut, "/api/agents/"+agentID+"/owner", map[string]any{
			"owner_id": targetID,
		}),
		"id", agentID,
	)
	w := httptest.NewRecorder()
	testHandler.TransferAgentOwner(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("TransferAgentOwner as current owner: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp AgentResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.OwnerID == nil || *resp.OwnerID != targetID {
		t.Fatalf("owner_id = %v, want %s", resp.OwnerID, targetID)
	}

	var stored string
	if err := testPool.QueryRow(context.Background(), `SELECT owner_id::text FROM agent WHERE id = $1`, agentID).Scan(&stored); err != nil {
		t.Fatalf("read owner_id: %v", err)
	}
	if stored != targetID {
		t.Fatalf("stored owner_id = %s, want %s", stored, targetID)
	}
}

func TestTransferAgentOwner_WorkspaceAdminCanTransferAfterOwnerLeft(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentID, currentOwnerID := transferOwnerFixture(t, "transfer-after-leave")
	targetID := createPermissionTestMember(t, "transfer-after-leave-target@multica.test")

	if _, err := testPool.Exec(context.Background(), `
		DELETE FROM member WHERE workspace_id = $1 AND user_id = $2
	`, testWorkspaceID, currentOwnerID); err != nil {
		t.Fatalf("remove previous owner membership: %v", err)
	}

	req := withURLParam(
		newRequest(http.MethodPut, "/api/agents/"+agentID+"/owner", map[string]any{
			"owner_id": targetID,
		}),
		"id", agentID,
	)
	w := httptest.NewRecorder()
	testHandler.TransferAgentOwner(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("TransferAgentOwner as workspace owner after leave: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var stored string
	if err := testPool.QueryRow(context.Background(), `SELECT owner_id::text FROM agent WHERE id = $1`, agentID).Scan(&stored); err != nil {
		t.Fatalf("read owner_id: %v", err)
	}
	if stored != targetID {
		t.Fatalf("stored owner_id = %s, want %s", stored, targetID)
	}
}

func TestTransferAgentOwner_PlainMemberCannotTransferOthers(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentID, _ := transferOwnerFixture(t, "transfer-denied-member")
	strangerID := createPermissionTestMember(t, "transfer-denied-stranger@multica.test")
	targetID := createPermissionTestMember(t, "transfer-denied-target@multica.test")

	req := withURLParam(
		newRequestAs(strangerID, http.MethodPut, "/api/agents/"+agentID+"/owner", map[string]any{
			"owner_id": targetID,
		}),
		"id", agentID,
	)
	w := httptest.NewRecorder()
	testHandler.TransferAgentOwner(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("TransferAgentOwner as unrelated member: expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

func TestTransferAgentOwner_TargetMustBeWorkspaceMember(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentID, currentOwnerID := transferOwnerFixture(t, "transfer-missing-target")

	var outsiderID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO "user" (name, email) VALUES ($1, $1) RETURNING id
	`, "transfer-outsider@multica.test").Scan(&outsiderID); err != nil {
		t.Fatalf("create outsider: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, outsiderID) })

	req := withURLParam(
		newRequestAs(currentOwnerID, http.MethodPut, "/api/agents/"+agentID+"/owner", map[string]any{
			"owner_id": outsiderID,
		}),
		"id", agentID,
	)
	w := httptest.NewRecorder()
	testHandler.TransferAgentOwner(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("TransferAgentOwner to non-member: expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestTransferAgentOwner_SameOwnerIsNoop(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentID, currentOwnerID := transferOwnerFixture(t, "transfer-same-owner")

	req := withURLParam(
		newRequestAs(currentOwnerID, http.MethodPut, "/api/agents/"+agentID+"/owner", map[string]any{
			"owner_id": currentOwnerID,
		}),
		"id", agentID,
	)
	w := httptest.NewRecorder()
	testHandler.TransferAgentOwner(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("TransferAgentOwner same owner: expected 200, got %d: %s", w.Code, w.Body.String())
	}
}
