package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service/inboundcoord"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestShouldEnqueueInboundCoordinatorJobHonorsOwnerSwitch(t *testing.T) {
	ctx := context.Background()
	agentID := createHandlerTestAgent(t, "coord-switch-defer", nil)
	command := DispatchCommand{
		Event:              DispatchEvent{Domain: "channel", Type: "message.created"},
		CompletionCallback: &DispatchCompletionCallback{URL: "/api/v1/dispatch-tasks/x/execution-result"},
	}
	plan := agentDispatchExecutionPlan{MaterializerType: protocol.DispatchSurfaceTypeChat}
	id := parseUUID(agentID)
	if err := testHandler.Queries.UpdateAgentInboundCoordinator(ctx, id, false); err != nil {
		t.Fatal(err)
	}
	if shouldEnqueueInboundCoordinatorJob(ctx, testHandler, command, plan, id) {
		t.Fatal("inbound coordinator off must send auto IM to the sandbox instead of the coordinator queue")
	}
	if err := testHandler.Queries.UpdateAgentInboundCoordinator(ctx, id, true); err != nil {
		t.Fatal(err)
	}
	if !shouldEnqueueInboundCoordinatorJob(ctx, testHandler, command, plan, id) {
		t.Fatal("inbound coordinator on must still use the durable coordinator queue")
	}
}

func TestCoordinatorCollectDeadline(t *testing.T) {
	start := time.Now().UTC()
	for _, tc := range []struct{ elapsed, want time.Duration }{
		{0, 4 * time.Second}, {3 * time.Second, 7 * time.Second},
		{10 * time.Second, 12 * time.Second}, {20 * time.Second, 12 * time.Second},
	} {
		if got := coordinatorCollectDeadline(start, start.Add(tc.elapsed)); !got.Equal(start.Add(tc.want)) {
			t.Fatalf("elapsed=%v deadline=%v, want %v", tc.elapsed, got.Sub(start), tc.want)
		}
	}
}

// Exercise the real acceptance, job, message, and completion-outbox writes.
// No Router request is sent: the test only inspects its durable outbox.
func TestCoordinatorCollectWindowAndReceiptBoundary(t *testing.T) {
	ctx := context.Background()
	agentID := createHandlerTestAgent(t, "collect-window-receipts", nil)
	namespace := parseUUID(uuid.NewString())
	cid := "cid-collect-" + uuid.NewString()
	dc := agentDispatchContext{AgentID: parseUUID(agentID), WorkspaceID: parseUUID(testWorkspaceID), UserID: parseUUID(testUserID), EndpointNamespaceID: namespace, EndpointID: uuidToString(namespace)}
	var callbacks []string
	enqueue := func(text string) db.InboundCoordinatorJob {
		t.Helper()
		key := uuid.NewString()
		callback := "/api/v1/dispatch-tasks/" + key + "/execution-result"
		callbacks = append(callbacks, callback)
		command := DispatchCommand{Source: DispatchSource{Type: "digital_employee"}, CompletionCallback: &DispatchCompletionCallback{URL: callback, Target: testRouterTargetIdentity}, Event: DispatchEvent{Data: DispatchEventData{Conversation: DispatchConversation{OpenConversationID: cid, Type: "single"}, Sender: DispatchSender{DisplayName: "测试委托人"}, Messages: []DispatchMessage{{OpenMsgID: key, Text: text}}}}}
		acceptance, err := testHandler.Queries.ClaimAgentDispatchAcceptance(ctx, db.ClaimAgentDispatchAcceptanceParams{EndpointID: namespace, AgentID: dc.AgentID, TargetIdentity: testRouterTargetIdentity, IdempotencyKey: key, RequestFingerprint: "sha256:" + strings.Repeat("a", 64)})
		if err != nil {
			t.Fatal(err)
		}
		_, job, err := testHandler.enqueueInboundCoordinatorJob(ctx, acceptance, command, dc, key, text)
		if err != nil {
			t.Fatal(err)
		}
		return job
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM inbound_coordinator_job WHERE agent_id=$1`, agentID)
		_, _ = testPool.Exec(ctx, `DELETE FROM agent_dispatch_acceptance WHERE endpoint_id=$1`, namespace)
		for _, callback := range callbacks {
			_, _ = testPool.Exec(ctx, `DELETE FROM task_completion_outbox WHERE callback_url=$1`, callback)
		}
	})
	first := enqueue("你")
	merged := enqueue("说话")
	if first.ID != merged.ID {
		t.Fatal("messages within the typing window must merge")
	}
	command, err := restoreInboundCoordinatorCommand(merged.Command, namespace, testRouterTargetIdentity)
	if err != nil {
		t.Fatal(err)
	}
	if len(command.Event.Data.Messages) != 2 || len(command.ExtraCompletionCallbacks) != 1 {
		t.Fatalf("lost messages/callbacks: %#v", command)
	}
	var completed int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM task_completion_outbox WHERE callback_url=ANY($1::text[])`, callbacks).Scan(&completed); err != nil {
		t.Fatal(err)
	}
	if completed != 0 {
		t.Fatal("collect must not claim execution completed before Decide")
	}
	// Simulate the quiet deadline passing without relying on a slow test sleep.
	if _, err := testPool.Exec(ctx, `UPDATE inbound_coordinator_job SET available_at=now()-interval '1 second' WHERE id=$1`, merged.ID); err != nil {
		t.Fatal(err)
	}
	late := enqueue("你好")
	if late.ID == merged.ID {
		t.Fatal("expired pending window absorbed a new message")
	}
	// A parked job remains pending with a future retry time, but is sealed.
	if _, err := testPool.Exec(ctx, `UPDATE inbound_coordinator_job SET available_at=now()+interval '5 seconds', last_error='scene already has two in-flight matters' WHERE id=$1`, late.ID); err != nil {
		t.Fatal(err)
	}
	newer := enqueue("你没干活啊")
	if newer.ID == late.ID || newer.ID == merged.ID {
		t.Fatal("parked window reopened collect")
	}
	response := httptest.NewRecorder()
	if !writeDispatchCoordinatorTerminal(response, ctx, testHandler, command, dc, inboundcoord.Decision{Action: inboundcoord.ActionReply, UserText: "在，你说。"}) {
		t.Fatal("missing terminal callback")
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM task_completion_outbox WHERE callback_url=ANY($1::text[])`, callbacks[:2]).Scan(&completed); err != nil {
		t.Fatal(err)
	}
	if completed != 2 {
		t.Fatalf("terminal window must settle both callbacks, got %d", completed)
	}
	// A continuous burst still closes at the absolute deadline.
	if _, err := testPool.Exec(ctx, `UPDATE inbound_coordinator_job SET created_at=now()-interval '11 seconds',available_at=now()+interval '4 seconds' WHERE id=$1`, newer.ID); err != nil {
		t.Fatal(err)
	}
	capped := enqueue("还有一句")
	if capped.ID != newer.ID || capped.AvailableAt.Time.Sub(capped.CreatedAt.Time) > inboundCoordinatorCollectMaxWait {
		t.Fatal("continuous arrivals extended collect beyond the absolute maximum")
	}
	// Terminal errors must settle the retained extra callbacks too.
	capped.LeaseToken = parseUUID(uuid.NewString())
	if _, err := testPool.Exec(ctx, `UPDATE inbound_coordinator_job SET status='running',lease_token=$2,lease_expires_at=now()+interval '1 minute' WHERE id=$1`, capped.ID, capped.LeaseToken); err != nil {
		t.Fatal(err)
	}
	failedCommand, err := restoreInboundCoordinatorCommand(capped.Command, namespace, testRouterTargetIdentity)
	if err != nil {
		t.Fatal(err)
	}
	if err := NewInboundCoordinatorJobWorker(testHandler).fail(ctx, capped, failedCommand, "test terminal failure"); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM task_completion_outbox WHERE callback_url=ANY($1::text[])`, callbacks[len(callbacks)-2:]).Scan(&completed); err != nil {
		t.Fatal(err)
	}
	if completed != 2 {
		t.Fatalf("failed window stranded collected callbacks: %d", completed)
	}
}

func TestCoordinatorFreshWindowJudgedAtCapacity(t *testing.T) {
	ctx := context.Background()
	agentID := createHandlerTestAgent(t, "collect-capacity-pings", nil)
	cid := "cid-capacity-" + uuid.NewString()
	for i := 0; i < 2; i++ {
		issueID := createTestIssue(t, fmt.Sprintf("capacity fixture %d", i), "todo", "medium")
		t.Cleanup(func() { deleteTestIssue(t, issueID) })
		createHandlerTestTaskForAgentOnIssue(t, agentID, issueID)
		var assocID string
		if err := testPool.QueryRow(ctx, `INSERT INTO assoc_task (workspace_id,agent_id,issue_id,purpose) VALUES ($1,$2,$3,'bounded collect capacity fixture') RETURNING id`, testWorkspaceID, agentID, issueID).Scan(&assocID); err != nil {
			t.Fatal(err)
		}
		if _, err := testPool.Exec(ctx, `INSERT INTO assoc_edge (workspace_id,agent_id,src_type,src_id,dst_type,dst_id,rel) VALUES ($1,$2,'task',$3,'scene',$4,'task_scene')`, testWorkspaceID, agentID, assocID, cid); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = testPool.Exec(ctx, `DELETE FROM assoc_edge WHERE src_id=$1`, assocID)
			_, _ = testPool.Exec(ctx, `DELETE FROM assoc_task WHERE id=$1`, assocID)
		})
	}
	q := testHandler.Queries
	count, err := q.CountActiveTasksForConversation(ctx, db.CountActiveTasksForConversationParams{WorkspaceID: parseUUID(testWorkspaceID), AgentID: parseUUID(agentID), ConversationID: cid, StaleAfterSecs: sceneCapacityStaleAfter.Seconds()})
	if err != nil || count != 2 {
		t.Fatalf("fixture capacity=%d err=%v", count, err)
	}
	worker := NewInboundCoordinatorJobWorker(testHandler)
	for _, text := range []string{"你干了吗？", "你没干活啊", "你说话", "你", "说话", "你好", "一项真正的新任务"} {
		command := DispatchCommand{Event: DispatchEvent{Data: DispatchEventData{Conversation: DispatchConversation{OpenConversationID: cid, Type: "single"}, Messages: []DispatchMessage{{Text: text}}}}}
		raw, _ := json.Marshal(command)
		job, err := q.CreateInboundCoordinatorJob(ctx, db.CreateInboundCoordinatorJobParams{AcceptanceID: parseUUID(uuid.NewString()), WorkspaceID: parseUUID(testWorkspaceID), AgentID: parseUUID(agentID), UserID: parseUUID(testUserID), EndpointNamespaceID: parseUUID(uuid.NewString()), DispatchEndpointID: "test", IdempotencyKey: uuid.NewString(), Command: raw, ChatSessionID: parseUUID(uuid.NewString()), UserMessageID: parseUUID(uuid.NewString()), AvailableAt: pgtype.Timestamptz{Time: time.Now().Add(-time.Second), Valid: true}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = testPool.Exec(ctx, `DELETE FROM inbound_coordinator_job WHERE id=$1`, job.ID) })
		job.LeaseToken = parseUUID(uuid.NewString())
		job.AttemptCount = 1
		if _, err := testPool.Exec(ctx, `UPDATE inbound_coordinator_job SET status='running',attempt_count=1,lease_token=$2,lease_expires_at=now()+interval '1 minute' WHERE id=$1`, job.ID, job.LeaseToken); err != nil {
			t.Fatal(err)
		}
		parked, err := worker.parkIfSceneWindowBusy(ctx, job, command)
		if err != nil || parked {
			t.Fatalf("fresh %q must reach Decide at capacity: parked=%v err=%v", text, parked, err)
		}
		if text == "一项真正的新任务" {
			job.LeaseToken = parseUUID(uuid.NewString())
			job.LastError = pgtype.Text{String: "scene already has two in-flight matters", Valid: true}
			command.CompletionCallback = &DispatchCompletionCallback{URL: "/api/v1/dispatch-tasks/" + uuid.NewString() + "/execution-result", Target: testRouterTargetIdentity}
			if _, err := testPool.Exec(ctx, `UPDATE inbound_coordinator_job SET status='running',lease_token=$2,lease_expires_at=now()+interval '1 minute',last_error=$3 WHERE id=$1`, job.ID, job.LeaseToken, job.LastError); err != nil {
				t.Fatal(err)
			}
			parked, err = worker.parkIfSceneWindowBusy(ctx, job, command)
			if err != nil || !parked {
				t.Fatalf("classified work must wait at capacity: parked=%v err=%v", parked, err)
			}
			var completed int
			if err := testPool.QueryRow(ctx, `SELECT count(*) FROM task_completion_outbox WHERE callback_url=$1`, command.CompletionCallback.URL).Scan(&completed); err != nil {
				t.Fatal(err)
			}
			if completed != 0 {
				t.Fatal("waiting for capacity must not send a completed callback")
			}
		} else {
			if _, err := q.CompleteInboundCoordinatorJob(ctx, db.CompleteInboundCoordinatorJobParams{ID: job.ID, LeaseToken: job.LeaseToken}); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// Capacity is per scene AND per delegator: one member's in-flight matters must
// not silence the other members of a shared group, a matter that is scheduled
// for later holds no slot, and a window cannot wait for capacity forever.
func TestCoordinatorSceneCapacityIsPerDelegatorAndBounded(t *testing.T) {
	ctx := context.Background()
	agentID := createHandlerTestAgent(t, "scene-capacity-per-delegator", nil)
	cid := "cid-delegator-" + uuid.NewString()
	q := testHandler.Queries
	bindMatter := func(label, personKey, status string, fireAt any) {
		t.Helper()
		issueID := createTestIssue(t, label, "todo", "medium")
		t.Cleanup(func() { deleteTestIssue(t, issueID) })
		taskID := createHandlerTestTaskForAgentOnIssue(t, agentID, issueID)
		if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET status=$2,fire_at=$3 WHERE id=$1`, taskID, status, fireAt); err != nil {
			t.Fatal(err)
		}
		var assocID string
		if err := testPool.QueryRow(ctx, `INSERT INTO assoc_task (workspace_id,agent_id,issue_id,purpose) VALUES ($1,$2,$3,'per delegator capacity fixture') RETURNING id`, testWorkspaceID, agentID, issueID).Scan(&assocID); err != nil {
			t.Fatal(err)
		}
		for _, edge := range []struct{ dstType, dstID, rel string }{
			{"scene", cid, "task_scene"},
			{"person", personKey, "task_person"},
		} {
			if _, err := testPool.Exec(ctx, `INSERT INTO assoc_edge (workspace_id,agent_id,src_type,src_id,dst_type,dst_id,rel) VALUES ($1,$2,'task',$3,$4,$5,$6)`, testWorkspaceID, agentID, assocID, edge.dstType, edge.dstID, edge.rel); err != nil {
				t.Fatal(err)
			}
		}
		t.Cleanup(func() {
			_, _ = testPool.Exec(ctx, `DELETE FROM assoc_edge WHERE src_id=$1`, assocID)
			_, _ = testPool.Exec(ctx, `DELETE FROM assoc_task WHERE id=$1`, assocID)
		})
	}
	busy, idle := "staff-busy-"+uuid.NewString(), "staff-idle-"+uuid.NewString()
	bindMatter("delegator capacity busy 1", busy, "running", nil)
	bindMatter("delegator capacity busy 2", busy, "queued", nil)
	// Scheduled for later: real work, but not occupying the employee now.
	bindMatter("delegator capacity later", idle, "deferred", time.Now().Add(time.Hour))

	delegatorCount := func(key string) int64 {
		t.Helper()
		count, err := q.CountActiveDelegatorTasksForConversation(ctx, db.CountActiveDelegatorTasksForConversationParams{
			WorkspaceID: parseUUID(testWorkspaceID), AgentID: parseUUID(agentID), ConversationID: cid,
			StaleAfterSecs: sceneCapacityStaleAfter.Seconds(), PersonKeys: []string{key},
		})
		if err != nil {
			t.Fatal(err)
		}
		return count
	}
	if got := delegatorCount(busy); got != 2 {
		t.Fatalf("busy delegator in-flight=%d, want 2", got)
	}
	if got := delegatorCount(idle); got != 0 {
		t.Fatalf("a matter scheduled for later must not spend a slot: in-flight=%d", got)
	}

	worker := NewInboundCoordinatorJobWorker(testHandler)
	judged := func(staffID string) (db.InboundCoordinatorJob, DispatchCommand) {
		t.Helper()
		command := DispatchCommand{Event: DispatchEvent{Data: DispatchEventData{
			Conversation: DispatchConversation{OpenConversationID: cid, Type: "group"},
			Sender:       DispatchSender{DisplayName: staffID, StaffID: staffID},
			Messages:     []DispatchMessage{{Text: "一项真正的新任务", SenderDisplayName: staffID, SenderStaffID: staffID}},
		}}}
		raw, _ := json.Marshal(command)
		job, err := q.CreateInboundCoordinatorJob(ctx, db.CreateInboundCoordinatorJobParams{AcceptanceID: parseUUID(uuid.NewString()), WorkspaceID: parseUUID(testWorkspaceID), AgentID: parseUUID(agentID), UserID: parseUUID(testUserID), EndpointNamespaceID: parseUUID(uuid.NewString()), DispatchEndpointID: "test", IdempotencyKey: uuid.NewString(), Command: raw, ChatSessionID: parseUUID(uuid.NewString()), UserMessageID: parseUUID(uuid.NewString()), AvailableAt: pgtype.Timestamptz{Time: time.Now().Add(-time.Second), Valid: true}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = testPool.Exec(ctx, `DELETE FROM inbound_coordinator_job WHERE id=$1`, job.ID) })
		job.LeaseToken = parseUUID(uuid.NewString())
		job.AttemptCount = 1
		job.LastError = pgtype.Text{String: sceneCapacityRejectReason(), Valid: true}
		if _, err := testPool.Exec(ctx, `UPDATE inbound_coordinator_job SET status='running',attempt_count=1,lease_token=$2,lease_expires_at=now()+interval '1 minute',last_error=$3 WHERE id=$1`, job.ID, job.LeaseToken, job.LastError); err != nil {
			t.Fatal(err)
		}
		return job, command
	}

	busyJob, busyCommand := judged(busy)
	parked, err := worker.parkIfSceneWindowBusy(ctx, busyJob, busyCommand)
	if err != nil || !parked {
		t.Fatalf("the delegator who filled their own slots must wait: parked=%v err=%v", parked, err)
	}
	idleJob, idleCommand := judged(idle)
	parked, err = worker.parkIfSceneWindowBusy(ctx, idleJob, idleCommand)
	if err != nil || parked {
		t.Fatalf("another member of the same scene must not inherit the wait: parked=%v err=%v", parked, err)
	}

	// Past the deadline the window stops waiting and runs on the next claim.
	// Release the other window first: the one-window-per-scene mutex is a
	// separate rule and would answer before capacity is consulted.
	if _, err := q.CompleteInboundCoordinatorJob(ctx, db.CompleteInboundCoordinatorJobParams{ID: idleJob.ID, LeaseToken: idleJob.LeaseToken}); err != nil {
		t.Fatal(err)
	}
	busyJob.CreatedAt = pgtype.Timestamptz{Time: time.Now().Add(-sceneCapacityWaitDeadline - time.Minute), Valid: true}
	busyJob.LeaseToken = parseUUID(uuid.NewString())
	if _, err := testPool.Exec(ctx, `UPDATE inbound_coordinator_job SET status='running',lease_token=$2,lease_expires_at=now()+interval '1 minute' WHERE id=$1`, busyJob.ID, busyJob.LeaseToken); err != nil {
		t.Fatal(err)
	}
	parked, err = worker.parkIfSceneWindowBusy(ctx, busyJob, busyCommand)
	if err != nil || parked {
		t.Fatalf("a window past the capacity deadline must stop parking: parked=%v err=%v", parked, err)
	}
	if slots := sceneCapacitySlots(withSceneCapacityWaiver(ctx), testHandler, parseUUID(testWorkspaceID), parseUUID(agentID), dispatchSceneDelegator(busyCommand)); slots <= 0 {
		t.Fatalf("the waived run must be admitted by the dispatch check too: slots=%d", slots)
	}
}

// assoc chooses a canonical person key per event, so the same person can own an
// older matter under a different identifier. Capacity must still see one person.
func TestSceneCapacityFollowsThePersonAcrossIdentifiers(t *testing.T) {
	ctx := context.Background()
	agentID := createHandlerTestAgent(t, "scene-capacity-alias", nil)
	cid := "cid-alias-" + uuid.NewString()
	suffix := uuid.NewString()
	staffKey, uidKey := "staff-"+suffix, "8"+suffix[:6]
	// The historical matter was bound when only the staffId was known.
	issueID := createTestIssue(t, "alias capacity fixture", "todo", "medium")
	t.Cleanup(func() { deleteTestIssue(t, issueID) })
	createHandlerTestTaskForAgentOnIssue(t, agentID, issueID)
	var assocID string
	if err := testPool.QueryRow(ctx, `INSERT INTO assoc_task (workspace_id,agent_id,issue_id,purpose) VALUES ($1,$2,$3,'alias capacity fixture matter') RETURNING id`, testWorkspaceID, agentID, issueID).Scan(&assocID); err != nil {
		t.Fatal(err)
	}
	for _, edge := range []struct{ dstType, dstID, rel string }{
		{"scene", cid, "task_scene"},
		{"person", staffKey, "task_person"},
	} {
		if _, err := testPool.Exec(ctx, `INSERT INTO assoc_edge (workspace_id,agent_id,src_type,src_id,dst_type,dst_id,rel) VALUES ($1,$2,'task',$3,$4,$5,$6)`, testWorkspaceID, agentID, assocID, edge.dstType, edge.dstID, edge.rel); err != nil {
			t.Fatal(err)
		}
	}
	// A later event carried the decimal uid too, so assoc re-canonicalised the
	// person onto the uid and kept the staffId only as an alias.
	for _, alias := range []string{uidKey, staffKey} {
		if _, err := testPool.Exec(ctx, `INSERT INTO assoc_person_alias (workspace_id,agent_id,person_key,alias_key) VALUES ($1,$2,$3,$4) ON CONFLICT DO NOTHING`, testWorkspaceID, agentID, uidKey, alias); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM assoc_edge WHERE src_id=$1`, assocID)
		_, _ = testPool.Exec(ctx, `DELETE FROM assoc_task WHERE id=$1`, assocID)
		_, _ = testPool.Exec(ctx, `DELETE FROM assoc_person_alias WHERE agent_id=$1`, agentID)
	})

	count, err := testHandler.Queries.CountActiveDelegatorTasksForConversation(ctx, db.CountActiveDelegatorTasksForConversationParams{
		WorkspaceID: parseUUID(testWorkspaceID), AgentID: parseUUID(agentID), ConversationID: cid,
		StaleAfterSecs: sceneCapacityStaleAfter.Seconds(), PersonKeys: []string{uidKey},
	})
	if err != nil || count != 1 {
		t.Fatalf("the uid must still see the matter bound under the staffId: count=%d err=%v", count, err)
	}
	resolved := resolveSceneDelegator(ctx, testHandler, parseUUID(testWorkspaceID), parseUUID(agentID), sceneDelegator{ConversationID: cid, Keys: []string{uidKey}})
	if !resolved.resolved || len(resolved.Keys) < 2 {
		t.Fatalf("grouping must use the alias closure: %+v", resolved)
	}
	fromStaff := resolveSceneDelegator(ctx, testHandler, parseUUID(testWorkspaceID), parseUUID(agentID), sceneDelegator{ConversationID: cid, Keys: []string{staffKey}})
	if fromStaff.key() != resolved.key() {
		t.Fatalf("both identifiers must group onto one budget: %q vs %q", fromStaff.key(), resolved.key())
	}
	stranger, err := testHandler.Queries.CountActiveDelegatorTasksForConversation(ctx, db.CountActiveDelegatorTasksForConversationParams{
		WorkspaceID: parseUUID(testWorkspaceID), AgentID: parseUUID(agentID), ConversationID: cid,
		StaleAfterSecs: sceneCapacityStaleAfter.Seconds(), PersonKeys: []string{"staff-other-" + suffix},
	})
	if err != nil || stranger != 0 {
		t.Fatalf("another person must not inherit this matter: count=%d err=%v", stranger, err)
	}
}
