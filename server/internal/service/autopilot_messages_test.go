package service

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestValidateMessageMergeInterval(t *testing.T) {
	for _, n := range []int32{1, 5, 60, 1440} {
		if err := ValidateMessageMergeInterval(n); err != nil {
			t.Fatal(n, err)
		}
	}
	for _, n := range []int32{-1, 0, 1441} {
		if err := ValidateMessageMergeInterval(n); err == nil {
			t.Fatal("accepted", n)
		}
	}
}

func messageFixture(t *testing.T, mode string) (*MessageAutomationService, db.Agent, pgtype.UUID, pgtype.UUID) {
	t.Helper()
	old, a := eventFixture(t)
	s := &MessageAutomationService{Pool: old.Pool, Autopilot: old.Autopilot}
	ctx := context.Background()
	var apID, triggerID pgtype.UUID
	err := s.Pool.QueryRow(ctx, `INSERT INTO autopilot(workspace_id,title,description,assignee_id,status,execution_mode,created_by_type,created_by_id)
        VALUES($1,'Message automation test','Report only the supplied counts',$2,'active',$3,'member',$4) RETURNING id`, a.WorkspaceID, a.ID, mode, a.OwnerID).Scan(&apID)
	if err != nil {
		t.Fatal(err)
	}
	ap, err := s.Autopilot.Queries.GetAutopilot(ctx, apID)
	if err != nil {
		t.Fatal(err)
	}
	if err = RecordAutopilotRuleVersion(ctx, s.Autopilot.Queries, ap, "member", a.OwnerID); err != nil {
		t.Fatal(err)
	}
	err = s.Pool.QueryRow(ctx, `INSERT INTO autopilot_trigger(autopilot_id,kind,merge_interval_minutes,published_by_type,published_by_id)
        VALUES($1,'dingtalk_message',1,'member',$2) RETURNING id`, apID, a.OwnerID).Scan(&triggerID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, `INSERT INTO channel_installation(workspace_id,agent_id,channel_type,status,installer_user_id,config)
        VALUES($1,$2,'dingtalk_account','active',$3,'{"router_source_id":"source-test"}')`, a.WorkspaceID, a.ID, a.OwnerID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.Pool.Exec(ctx, `DELETE FROM autopilot_message_event WHERE trigger_id=$1`, triggerID)
		s.Pool.Exec(ctx, `DELETE FROM autopilot_message_window WHERE autopilot_id=$1`, apID)
		s.Pool.Exec(ctx, `DELETE FROM agent_task_queue WHERE agent_id=$1`, a.ID)
		s.Pool.Exec(ctx, `DELETE FROM issue WHERE origin_id=$1`, apID)
		s.Pool.Exec(ctx, `DELETE FROM autopilot_rule_version WHERE autopilot_id=$1`, apID)
		s.Pool.Exec(ctx, `DELETE FROM autopilot_trigger WHERE autopilot_id=$1`, apID)
		s.Pool.Exec(ctx, `DELETE FROM autopilot_run WHERE autopilot_id=$1`, apID)
		s.Pool.Exec(ctx, `DELETE FROM autopilot WHERE id=$1`, apID)
		s.Pool.Exec(ctx, `DELETE FROM channel_installation WHERE agent_id=$1`, a.ID)
	})
	return s, a, apID, triggerID
}

func messageInput(id, conversation string, mentioned bool) MessageObservation {
	return MessageObservation{EventID: id, SourceID: "source-test", ReceivedAt: time.Now().UTC(), OccurredAt: time.Now().UTC(), ConversationID: conversation, ConversationCID: "12345", ConversationTitle: conversation, ConversationType: "group", Mentioned: mentioned}
}

var messageRuntime = []byte(`{"external_identity":{"dws":{"uid":"123","orgId":"456"}}}`)

func messageSQL(t *testing.T, s *MessageAutomationService, sql string, args ...any) {
	t.Helper()
	if _, err := s.Pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}
func messageCount(t *testing.T, s *MessageAutomationService, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := s.Pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func admitMessage(t *testing.T, s *MessageAutomationService, a db.Agent, e MessageObservation, want int) {
	t.Helper()
	n, err := s.Admit(context.Background(), a.WorkspaceID, a.ID, e, messageRuntime)
	if err != nil || n != want {
		t.Fatalf("admit=%d want=%d err=%v", n, want, err)
	}
}
func dueMessages(t *testing.T, s *MessageAutomationService, trigger pgtype.UUID) {
	messageSQL(t, s, `UPDATE autopilot_message_window SET due_at=now()-interval '1 second' WHERE trigger_id=$1 AND status='collecting'`, trigger)
}

func TestMessageAutomationMergeConcurrencyRecoveryAndEmpty(t *testing.T) {
	s, a, apID, trigger := messageFixture(t, "run_only")
	ctx := context.Background()
	if worked, err := s.ProcessNext(ctx); err != nil || worked {
		t.Fatalf("empty period worked=%v err=%v", worked, err)
	}
	e := messageInput("one", "group-one", true)
	var wg sync.WaitGroup
	failures := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.Admit(ctx, a.WorkspaceID, a.ID, e, messageRuntime)
			failures <- err
		}()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := messageCount(t, s, `SELECT count(*) FROM autopilot_message_event WHERE trigger_id=$1`, trigger); n != 1 {
		t.Fatalf("duplicate count=%d", n)
	}
	admitMessage(t, s, a, messageInput("two", "group-one", false), 1)
	admitMessage(t, s, a, messageInput("three", "direct-two", false), 1)
	if n := messageCount(t, s, `SELECT count(*) FROM autopilot_message_window WHERE trigger_id=$1`, trigger); n != 1 {
		t.Fatalf("windows=%d", n)
	}
	if worked, err := s.ProcessNext(ctx); err != nil || worked {
		t.Fatalf("early dispatch=%v %v", worked, err)
	}
	dueMessages(t, s, trigger)
	failures = make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := s.ProcessNext(ctx); failures <- err }()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := messageCount(t, s, `SELECT count(*) FROM autopilot_run WHERE autopilot_id=$1`, apID); n != 1 {
		t.Fatalf("runs=%d", n)
	}
	if n := messageCount(t, s, `SELECT count(*) FROM agent_task_queue WHERE agent_id=$1`, a.ID); n != 1 {
		t.Fatalf("tasks=%d", n)
	}
	var raw, taskContext []byte
	if err := s.Pool.QueryRow(ctx, `SELECT r.trigger_payload,t.context FROM autopilot_run r JOIN agent_task_queue t ON t.id=r.task_id WHERE r.autopilot_id=$1`, apID).Scan(&raw, &taskContext); err != nil {
		t.Fatal(err)
	}
	var summary MessageStatisticsPayload
	if err := json.Unmarshal(raw, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.MessageCount != 3 || summary.ConversationCount != 2 || summary.MentionCount != 1 {
		t.Fatalf("wrong counts: %+v", summary)
	}
	identity, ok, err := fcE2BExternalDWSIdentity(db.AgentTaskQueue{Context: taskContext})
	if err != nil || !ok || identity.UID != "123" {
		t.Fatalf("runtime identity=%+v present=%v err=%v", identity, ok, err)
	}
	// Simulate the crash after dispatch, before its window acknowledgment.
	messageSQL(t, s, `UPDATE autopilot_message_window SET status='ready',run_id=NULL WHERE trigger_id=$1`, trigger)
	if worked, err := s.ProcessNext(ctx); err != nil || !worked {
		t.Fatalf("recovery=%v %v", worked, err)
	}
	if n := messageCount(t, s, `SELECT count(*) FROM autopilot_run WHERE autopilot_id=$1`, apID); n != 1 {
		t.Fatalf("recovery duplicated run: %d", n)
	}
	if worked, err := s.ProcessNext(ctx); err != nil || worked {
		t.Fatalf("idle generated work=%v %v", worked, err)
	}
	admitMessage(t, s, a, e, 0)
	admitMessage(t, s, a, messageInput("later", "group-one", false), 1)
	if n := messageCount(t, s, `SELECT count(*) FROM autopilot_message_window WHERE trigger_id=$1`, trigger); n != 2 {
		t.Fatalf("next window=%d", n)
	}
}

func TestMessageAutomationBindingPauseRevisionAndSourceIsolation(t *testing.T) {
	for _, change := range []string{"pause", "trigger_off", "revision", "unbind", "delete_trigger", "reassign"} {
		t.Run(change, func(t *testing.T) {
			s, a, apID, trigger := messageFixture(t, "run_only")
			wrong := messageInput("wrong", "group", false)
			wrong.SourceID = "another-source"
			admitMessage(t, s, a, wrong, 0)
			old := messageInput("old", "group", false)
			old.ReceivedAt = time.Now().Add(-time.Hour)
			admitMessage(t, s, a, old, 0)
			admitMessage(t, s, a, messageInput("one", "group", false), 1)
			dueMessages(t, s, trigger)
			switch change {
			case "pause":
				messageSQL(t, s, `UPDATE autopilot SET status='paused' WHERE id=$1`, apID)
			case "trigger_off":
				messageSQL(t, s, `UPDATE autopilot_trigger SET enabled=false WHERE id=$1`, trigger)
			case "revision":
				messageSQL(t, s, `UPDATE autopilot_trigger SET message_revision=message_revision+1 WHERE id=$1`, trigger)
			case "unbind":
				messageSQL(t, s, `UPDATE channel_installation SET status='revoked' WHERE agent_id=$1`, a.ID)
			case "delete_trigger":
				messageSQL(t, s, `DELETE FROM autopilot_trigger WHERE id=$1`, trigger)
			case "reassign":
				messageSQL(t, s, `UPDATE autopilot SET assignee_type='squad' WHERE id=$1`, apID)
			}
			if _, err := s.ProcessNext(context.Background()); err != nil {
				t.Fatal(err)
			}
			if n := messageCount(t, s, `SELECT count(*) FROM autopilot_run WHERE autopilot_id=$1`, apID); n != 0 {
				t.Fatalf("invalidated window created %d runs", n)
			}
		})
	}
}

func TestMessageAutomationCreateIssueIncludesStatisticsAndIdentity(t *testing.T) {
	s, a, apID, trigger := messageFixture(t, "create_issue")
	admitMessage(t, s, a, messageInput("one", "group", true), 1)
	dueMessages(t, s, trigger)
	if _, err := s.ProcessNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	var description string
	var taskContext []byte
	if err := s.Pool.QueryRow(context.Background(), `SELECT i.description,t.context FROM autopilot_run r JOIN issue i ON i.id=r.issue_id JOIN agent_task_queue t ON t.issue_id=i.id WHERE r.autopilot_id=$1 LIMIT 1`, apID).Scan(&description, &taskContext); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(description, `"message_count": 1`) || !strings.Contains(description, "Report only the supplied counts") {
		t.Fatal("issue omitted instructions or statistics")
	}
	if _, ok, err := fcE2BExternalDWSIdentity(db.AgentTaskQueue{Context: taskContext}); err != nil || !ok {
		t.Fatalf("issue runtime identity=%v %v", ok, err)
	}
}
