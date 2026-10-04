package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/contextcap"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type sourceCaptureDB struct {
	bound                       bool
	calls                       int
	runTaskID, runID, requester string
}

func (d *sourceCaptureDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	panic("unexpected write")
}
func (d *sourceCaptureDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	panic("unexpected read")
}
func (d *sourceCaptureDB) QueryRow(_ context.Context, query string, args ...any) pgx.Row {
	d.calls++
	if strings.Contains(query, "employee_task_run") {
		if len(args) != 6 {
			panic("missing run fence")
		}
		return sourceCaptureRunRow{d.runTaskID, d.runID, d.requester}
	}
	if !strings.Contains(query, "scene_kind") || len(args) != 4 {
		panic("missing complete scene fence")
	}
	return sourceCaptureRow{d.bound}
}

type sourceCaptureRow struct{ bound bool }

func (r sourceCaptureRow) Scan(dest ...any) error { *(dest[0].(*bool)) = r.bound; return nil }

func TestCaptureRoutineSourceAllowlistAndFence(t *testing.T) {
	agent, workspace, sceneID, queueID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	queueUUID := pgtype.UUID{Bytes: uuid.MustParse(queueID), Valid: true}
	agentUUID := pgtype.UUID{Bytes: uuid.MustParse(agent), Valid: true}
	payload := map[string]any{
		"agent_scene":                  map[string]string{"scene_id": sceneID},
		"dispatch_context_prompt":      "Reference customer summary and earlier result",
		"agent_identity_context_token": "DO_NOT_PERSIST", "runtime_mcp_overlay": map[string]string{"token": "SECRET"},
		"dispatch_event_data": map[string]any{"sender": map[string]string{"staffId": "staff-1", "displayName": "冬翔"}, "messages": []map[string]any{{"openMsgId": "m1", "occurredAt": 1234, "text": "15 分钟后提醒我", "senderStaffId": "staff-1", "attachments": []map[string]string{{"downloadUrl": "SIGNED_SECRET"}}}}},
	}
	raw, _ := json.Marshal(payload)
	task := db.AgentTaskQueue{ID: queueUUID, AgentID: agentUUID, Context: raw}
	store := &sourceCaptureDB{bound: true}
	result, err := CaptureRoutineSource(context.Background(), store, task, workspace, agent, "org1", sceneID)
	if err != nil {
		t.Fatal(err)
	}
	if result.RequesterRef != "dingtalk:staff:staff-1" || result.Messages[0].OccurredAt != 1234 || result.Messages[0].Text != "15 分钟后提醒我" || result.OriginalWorkPacket != "Reference customer summary and earlier result" {
		t.Fatalf("source lost: %#v", result)
	}
	serialized, _ := json.Marshal(result)
	for _, forbidden := range []string{"DO_NOT_PERSIST", "SECRET", "runtime_mcp", "attachments"} {
		if strings.Contains(string(serialized), forbidden) {
			t.Fatalf("persisted credential field %s", forbidden)
		}
	}
	_, err = CaptureRoutineSource(context.Background(), store, task, workspace, agent, "org1", uuid.NewString())
	if !errors.Is(err, contextcap.ErrInvalidInput) {
		t.Fatalf("cross scene accepted: %v", err)
	}
	store.bound = false
	_, err = CaptureRoutineSource(context.Background(), store, task, workspace, agent, "other-org", sceneID)
	if !errors.Is(err, contextcap.ErrInvalidInput) {
		t.Fatalf("cross tenant accepted: %v", err)
	}
	payload["dispatch_context_prompt"] = strings.Repeat("x", contextcap.MaxRoutineSourceBytes)
	task.Context, _ = json.Marshal(payload)
	store.bound = true
	_, err = CaptureRoutineSource(context.Background(), store, task, workspace, agent, "org1", sceneID)
	if !errors.Is(err, contextcap.ErrInvalidInput) {
		t.Fatalf("oversize snapshot accepted: %v", err)
	}
}

type sourceCaptureRunRow struct{ taskID, runID, requester string }

func (r sourceCaptureRunRow) Scan(dest ...any) error {
	*(dest[0].(*string)) = r.taskID
	*(dest[1].(*string)) = r.runID
	*(dest[2].(*string)) = r.requester
	return nil
}
func TestCaptureRoutineSourceDirectPacket(t *testing.T) {
	agent, workspace, sceneID, queueID, taskID, runID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	raw, _ := json.Marshal(map[string]any{"type": "employee_direct", "workspace_id": workspace, "employee_task_id": taskID, "direct_principal_id": agent, "agent_scene": map[string]string{"scene_id": sceneID}, "direct_task_prompt": "Work packet: original context, prior history and selected results", "employee_source_ref": "message-1"})
	task := db.AgentTaskQueue{ID: pgtype.UUID{Bytes: uuid.MustParse(queueID), Valid: true}, AgentID: pgtype.UUID{Bytes: uuid.MustParse(agent), Valid: true}, Context: raw}
	store := &sourceCaptureDB{bound: true, runTaskID: taskID, runID: runID, requester: "dingtalk:human-1"}
	result, err := CaptureRoutineSource(context.Background(), store, task, workspace, agent, "org1", sceneID)
	if err != nil {
		t.Fatal(err)
	}
	if result.EmployeeTaskID != taskID || result.EmployeeRunID != runID || result.RequesterRef != "dingtalk:human-1" || result.SourceRef != "message-1" || !strings.Contains(result.OriginalWorkPacket, "prior history") || store.calls != 2 {
		t.Fatalf("lost direct provenance: %#v", result)
	}
}
