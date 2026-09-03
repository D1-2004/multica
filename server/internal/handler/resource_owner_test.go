package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTransferSkillOwner_CreatorCanTransfer(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	creatorID := createPermissionTestMember(t, "skill-owner-creator@multica.test")
	targetID := createPermissionTestMember(t, "skill-owner-target@multica.test")
	var skillID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO skill (workspace_id, name, description, content, config, created_by)
		VALUES ($1, $2, '', '', '{}'::jsonb, $3)
		RETURNING id
	`, testWorkspaceID, "transfer-skill-owner", creatorID).Scan(&skillID); err != nil {
		t.Fatalf("create skill: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM skill WHERE id = $1`, skillID) })

	req := withURLParam(
		newRequestAs(creatorID, http.MethodPut, "/api/skills/"+skillID+"/owner", map[string]any{
			"owner_id": targetID,
		}),
		"id", skillID,
	)
	w := httptest.NewRecorder()
	testHandler.TransferSkillOwner(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("TransferSkillOwner: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp SkillResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.CreatedBy == nil || *resp.CreatedBy != targetID {
		t.Fatalf("created_by = %v, want %s", resp.CreatedBy, targetID)
	}
}

func TestTransferRuntimeOwner_AdminCanTransfer(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	currentOwner := createPermissionTestMember(t, "runtime-owner-current@multica.test")
	targetID := createPermissionTestMember(t, "runtime-owner-target@multica.test")
	runtimeID := handlerTestRuntimeID(t)
	var previousOwner *string
	if err := testPool.QueryRow(context.Background(), `
		SELECT owner_id::text FROM agent_runtime WHERE id = $1
	`, runtimeID).Scan(&previousOwner); err != nil {
		t.Fatalf("read runtime owner: %v", err)
	}
	t.Cleanup(func() {
		if previousOwner == nil {
			testPool.Exec(context.Background(), `UPDATE agent_runtime SET owner_id = NULL WHERE id = $1`, runtimeID)
			return
		}
		testPool.Exec(context.Background(), `UPDATE agent_runtime SET owner_id = $1 WHERE id = $2`, *previousOwner, runtimeID)
	})
	if _, err := testPool.Exec(context.Background(), `
		UPDATE agent_runtime SET owner_id = $1 WHERE id = $2
	`, currentOwner, runtimeID); err != nil {
		t.Fatalf("assign runtime owner: %v", err)
	}

	req := withURLParam(
		newRequest(http.MethodPut, "/api/runtimes/"+runtimeID+"/owner", map[string]any{
			"owner_id": targetID,
		}),
		"runtimeId", runtimeID,
	)
	w := httptest.NewRecorder()
	testHandler.TransferRuntimeOwner(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("TransferRuntimeOwner: expected 200, got %d: %s", w.Code, w.Body.String())
	}
}
