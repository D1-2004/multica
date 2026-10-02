package handler

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestEmployeeDirectClaimSkipsReplyQuickwinAndKeepsOrdinaryTask(t *testing.T) {
	f, model, dc := employeeFixture(t)
	ctx := context.Background()
	if _, err := testPool.Exec(ctx, `UPDATE agent SET max_concurrent_tasks=2 WHERE id=$1`, f.agentID); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE agent_runtime SET metadata='{"client_capabilities":["employee-direct-v1","dws_message_policy_v1"]}' WHERE id=(SELECT runtime_id FROM agent WHERE id=$1)`, f.agentID); err != nil {
		t.Fatal(err)
	}
	f.command.CompletionCallback = nil
	f.command.ResponsePolicy = nil
	model.dispatch = true
	if response := employeeHTTP(t, f, dc, uuid.NewString()); response.Code != http.StatusAccepted {
		t.Fatal(response.Body.String())
	}
	var receipt string
	if err := testPool.QueryRow(ctx, `SELECT receipt_id::text FROM employee_event_consumption WHERE agent_id=$1`, f.agentID).Scan(&receipt); err != nil {
		t.Fatal(err)
	}
	model.sourceRef = receipt + "/message-1"
	if _, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil {
		t.Fatal(err)
	}
	agent, err := f.h.Queries.GetAgent(ctx, dc.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := f.h.Queries.GetAgentRuntime(ctx, agent.RuntimeID)
	if err != nil {
		t.Fatal(err)
	}
	f.h.TaskService.RuntimeStartRecoveryConfig = func() service.RuntimeStartRecoveryConfig {
		return service.RuntimeStartRecoveryConfig{Scoped: true, RolloutEnabled: true, RolloutAgentIDs: []string{f.agentID}, DingTalkReplyCommand: true}
	}
	claimed, err := f.h.TaskService.ClaimTaskForRuntime(ctx, runtime.ID, service.TaskClaimAuthorization{EmployeeDirectRuntimeIDs: []pgtype.UUID{runtime.ID}})
	if err != nil || claimed == nil {
		t.Fatal("Direct queue was not claimed", err)
	}
	req := newDaemonTokenRequest(http.MethodPost, "/", nil, testWorkspaceID, runtime.DaemonID.String)
	req.Header.Set("X-Client-Capabilities", protocol.DaemonCapabilityEmployeeDirectV1+","+protocol.DaemonCapabilityTaskInstructionV1+","+protocol.DWSMessagePolicyCapability)
	direct, _, _, _, failure := f.h.buildClaimedTaskResponse(req, claimed, runtime, "", uuidToString(runtime.ID), testWorkspaceID)
	if failure != nil || direct.DirectTaskPrompt == "" {
		t.Fatal("Direct claim failed", failure)
	}
	if strings.Contains(direct.Instruction, "dws chat +messages-reply") {
		t.Fatal("Direct claim received a second source-delivery owner from quickwin")
	}
	ordinary, err := f.h.Queries.GetAgentTask(ctx, parseUUID(f.taskID))
	if err != nil {
		t.Fatal(err)
	}
	normal, _, _, _, failure := f.h.buildClaimedTaskResponse(req, &ordinary, runtime, "", uuidToString(runtime.ID), testWorkspaceID)
	if failure != nil {
		t.Fatal("ordinary claim failed", failure)
	}
	if normal.DirectTaskPrompt != "" || !strings.Contains(normal.Instruction, "dws chat +messages-reply") {
		t.Fatal("ordinary task lost its enabled reply quickwin")
	}
}
