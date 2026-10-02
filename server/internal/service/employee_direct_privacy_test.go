package service

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestEmployeeDirectRealtimeUsesOnlyVerifiedCurrentOriginator(t *testing.T) {
	f := directDatabase(t)
	ctx := context.Background()
	admitted, err := f.service.EnqueueDirectTask(ctx, f.request)
	if err != nil {
		t.Fatal(err)
	}
	var sent []events.Event
	f.service.Bus.SubscribeAll(func(e events.Event) { sent = append(sent, e) })
	runtime := &terminalRecordingLauncher{}
	f.service.RuntimeLauncher = runtime
	f.service.ReportProgress(ctx, admitted.Task, f.request.Task.Scope.WorkspaceID, "PRIVATE_CONNECTOR_PROGRESS", 1, 2)
	f.service.broadcastTaskFailedEvent(ctx, admitted.Task, "PRIVATE_CONNECTOR_ERROR", "agent_error", false)
	f.service.broadcastTaskEvent(ctx, protocol.EventTaskCompleted, admitted.Task)
	if len(sent) != 3 {
		t.Fatalf("originator events = %d", len(sent))
	}
	for _, e := range sent {
		if e.RecipientUserID != util.UUIDToString(admitted.Task.OriginatorUserID) || e.WorkspaceID != f.request.Task.Scope.WorkspaceID {
			t.Errorf("private Direct event used workspace broadcast: %+v", e)
		}
	}
	if len(runtime.terminal) != 2 {
		t.Fatalf("runtime terminal observer lost: %d", len(runtime.terminal))
	}
	sent = nil
	external := admitted.Task
	external.OriginatorUserID = pgtype.UUID{}
	f.service.ReportProgress(ctx, external, f.request.Task.Scope.WorkspaceID, "PRIVATE_EXTERNAL_PROGRESS", 1, 2)
	f.service.broadcastTaskFailedEvent(ctx, external, "PRIVATE_EXTERNAL_ERROR", "agent_error", false)
	f.service.broadcastTaskEvent(ctx, protocol.EventTaskCompleted, external)
	if len(sent) != 0 {
		t.Fatalf("accountable principal got unattributed private events: %+v", sent)
	}
	if len(runtime.terminal) != 4 {
		t.Fatalf("suppression dropped runtime observer: %d", len(runtime.terminal))
	}
	if _, err := f.pool.Exec(ctx, `DELETE FROM member WHERE workspace_id=$1::uuid AND user_id=$2`, f.request.Task.Scope.WorkspaceID, admitted.Task.OriginatorUserID); err != nil {
		t.Fatal(err)
	}
	f.service.ReportProgress(ctx, admitted.Task, f.request.Task.Scope.WorkspaceID, "DEPARTED_USER", 1, 2)
	if len(sent) != 0 {
		t.Fatal("departed originator still received Direct events")
	}
	legacy := admitted.Task
	legacy.Context = nil
	legacy.TriggerEvidenceKind = pgtype.Text{}
	f.service.ReportProgress(ctx, legacy, f.request.Task.Scope.WorkspaceID, "PUBLIC_ISSUE_PROGRESS", 1, 2)
	if len(sent) != 1 || sent[0].RecipientUserID != "" {
		t.Fatalf("ordinary task routing changed: %+v", sent)
	}
}
