package handler

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestProactiveAdmissionDeduplicatesAndReleasesConversation(t *testing.T) {
	if testPool == nil {
		t.Skip("database unavailable")
	}
	ctx := context.Background()
	agentID := createHandlerTestAgent(t, "proactive-admission", nil)
	ns := parseUUID(uuid.NewString())
	cid := "cid-" + uuid.NewString()
	dc := agentDispatchContext{AgentID: parseUUID(agentID), WorkspaceID: parseUUID(testWorkspaceID), UserID: parseUUID(testUserID), EndpointNamespaceID: ns, EndpointID: uuidToString(ns)}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM coordinator_observed_message WHERE agent_id=$1`, dc.AgentID)
		_, _ = testPool.Exec(ctx, `DELETE FROM inbound_coordinator_job WHERE agent_id=$1`, dc.AgentID)
		_, _ = testPool.Exec(ctx, `DELETE FROM agent_dispatch_acceptance WHERE endpoint_id=$1`, ns)
	})
	enqueue := func(id, text string) db.InboundCoordinatorJob {
		t.Helper()
		key := uuid.NewString()
		c := DispatchCommand{ProactiveConversation: true, Source: DispatchSource{Platform: "dingtalk", Type: "digital_employee"}, ExternalIdentity: AgentDispatchExternalIdentity{DWS: &AgentDispatchDWSIdentity{UID: "employee", OrgID: "org"}}, Event: DispatchEvent{Type: "message.created", Domain: "channel", Data: DispatchEventData{Conversation: DispatchConversation{OpenConversationID: cid, Type: "group"}, Sender: DispatchSender{UID: "sender", DisplayName: "同事"}, Messages: []DispatchMessage{{OpenMsgID: id, Text: text}}}}}
		a, err := testHandler.Queries.ClaimAgentDispatchAcceptance(ctx, db.ClaimAgentDispatchAcceptanceParams{EndpointID: ns, AgentID: dc.AgentID, TargetIdentity: testRouterTargetIdentity, IdempotencyKey: key, RequestFingerprint: "sha256:" + strings.Repeat("a", 64)})
		if err != nil {
			t.Fatal(err)
		}
		_, job, err := testHandler.enqueueInboundCoordinatorJob(ctx, a, c, dc, key, text)
		if err != nil {
			t.Fatal(err)
		}
		return job
	}
	first := enqueue("m1", "在吗")
	dup := enqueue("m1", "在吗")
	if dup.ID.Valid {
		t.Fatal("duplicate Router key created another decision")
	}
	combined := enqueue("m2", "补充问题")
	if combined.ID != first.ID {
		t.Fatal("typing burst not merged")
	}
	c, err := restoreJobCommand(combined)
	if err != nil || len(c.Event.Data.Messages) != 2 || !c.ProactiveConversation {
		t.Fatalf("merged message coverage: %v", err)
	}
	// A completed Coordinator decision immediately permits the next window,
	// regardless of background sandbox tasks or a former 30-second interval.
	if _, err = testPool.Exec(ctx, `UPDATE inbound_coordinator_job SET status='completed' WHERE id=$1`, first.ID); err != nil {
		t.Fatal(err)
	}
	next := enqueue("m3", "再补充一个问题")
	if next.ID == first.ID || next.AvailableAt.Time.Sub(time.Now()) > 5*time.Second {
		t.Fatal("next decision waits for the old automation interval")
	}
	var tasks, aps int
	if err = testPool.QueryRow(ctx, `SELECT (SELECT count(*) FROM agent_task_queue WHERE agent_id=$1),(SELECT count(*) FROM autopilot WHERE assignee_id=$1)`, dc.AgentID).Scan(&tasks, &aps); err != nil || tasks != 0 || aps != 0 {
		t.Fatalf("admission started sandbox or automation: %d/%d %v", tasks, aps, err)
	}
}

func TestProactiveCollectKeepsAcknowledgmentsAndBoundsWindow(t *testing.T) {
	base := DispatchCommand{ProactiveConversation: true, Event: DispatchEvent{Data: DispatchEventData{Messages: []DispatchMessage{{Text: "帮我查一下"}}}}}
	extra := base
	extra.Event.Data.Messages = []DispatchMessage{{Text: "不用回复了"}}
	if !sameCoordinatorCollectKind(base, extra) {
		t.Fatal("proactive supplement bypassed semantic interpretation")
	}
	extra.Event.Data.Messages = make([]DispatchMessage, 100)
	if sameCoordinatorCollectKind(base, extra) {
		t.Fatal("oversized window was not split")
	}
}
