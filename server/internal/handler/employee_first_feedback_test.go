package handler

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/service/dingtalkresponse"
	"github.com/multica-ai/multica/server/internal/service/employeeloop"
)

func firstFeedbackFixture(t *testing.T) (employeeNoticeFixture, *employeeSceneHost, employeeloop.Identity, employeeSourceMessage) {
	t.Helper()
	f, host, id, source := employeeCurrentTaskHost(t, "succeeded", "查一下这个事项现在怎么样了")
	// Persist the actual ordinal-zero request boundary, leaving its response pending.
	raw := json.RawMessage(`{"messages":[{"role":"user","content":"查事项"}],"tools":[{"type":"function","function":{"name":"first_feedback"}},{"type":"function","function":{"name":"read_task"}}]}`)
	if _, err := host.worker.store.BeginModel(context.Background(), host.job, 0, raw); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM employee_first_feedback WHERE agent_id=$1`, f.agentID)
	})
	return f, host, id, source
}
func firstFeedbackCall(s employeeSourceMessage) employeeloop.ToolCall {
	return employeeloop.ToolCall{Name: "first_feedback", NativeToolCallID: "feedback-first", Arguments: map[string]any{"source_ref": s.SourceRef, "text": "我先核对这项工作的最新进度。", "intent": "lookup"}}
}
func firstFeedbackAction(t *testing.T, jobID string) dingtalkresponse.ActionInput {
	t.Helper()
	var raw []byte
	if err := testPool.QueryRow(context.Background(), `SELECT a.input FROM response_action a JOIN employee_first_feedback f ON f.action_id=a.id WHERE f.job_id=$1`, jobID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var in dingtalkresponse.ActionInput
	if err := json.Unmarshal(raw, &in); err != nil {
		t.Fatal(err)
	}
	return in
}
func TestEmployeeFirstFeedbackEarlyFrameAndJournalReplay(t *testing.T) {
	f, h, id, s := firstFeedbackFixture(t)
	ctx := context.Background()
	call := firstFeedbackCall(s)
	result, err := h.Execute(ctx, id, call)
	if err != nil || !strings.HasPrefix(result.Receipt, "first-feedback:") || result.Terminal != nil {
		t.Fatal(result, err)
	}
	again, err := h.Execute(ctx, id, call)
	if err != nil || again.Receipt != result.Receipt {
		t.Fatal(again, err)
	}
	var n int
	var state string
	var pending bool
	if err = testPool.QueryRow(ctx, `SELECT (SELECT count(*) FROM employee_first_feedback WHERE job_id=j.id),state,model_journal->0->'response' IS NULL FROM employee_scene_job j WHERE id=$1`, h.job.ID).Scan(&n, &state, &pending); err != nil || n != 1 || state != "running" || !pending {
		t.Fatal(n, state, pending, err)
	}
	in := firstFeedbackAction(t, h.job.ID)
	if in.CallbackURL != "" || in.TaskID != "" || in.SceneNoticeID != "" || in.CloseState != "" || in.EmployeeMessageJobID != "" {
		t.Fatal("feedback claimed completion", in)
	}
	if err = f.h.BeforeEmployeeRunNoticeSend(ctx, in); err != nil {
		t.Fatal("running feedback rejected", err)
	}
	call.Arguments["text"] = "另一个首句"
	if _, err = h.Execute(ctx, id, call); !errors.Is(err, employeeentry.ErrConflict) {
		t.Fatal("changed native replay accepted", err)
	}
	call.NativeToolCallID = "different-native-id"
	if _, err = h.Execute(ctx, id, call); !errors.Is(err, employeeloop.ErrToolRefused) {
		t.Fatal("second first feedback accepted", err)
	}
}
func TestEmployeeFirstFeedbackFinalSupersedesPendingOnly(t *testing.T) {
	for _, state := range []string{"pending", "unknown", "provider_accepted"} {
		t.Run(state, func(t *testing.T) {
			f, h, id, s := firstFeedbackFixture(t)
			ctx := context.Background()
			if _, err := h.Execute(ctx, id, firstFeedbackCall(s)); err != nil {
				t.Fatal(err)
			}
			in := firstFeedbackAction(t, h.job.ID)
			if _, err := testPool.Exec(ctx, `UPDATE response_action SET state=$2 WHERE id=$1`, in.ActionID, state); err != nil {
				t.Fatal(err)
			}
			participationComplete(t, h, employeeloop.Outcome{Decision: employeeloop.Decision{Kind: employeeloop.Reply, Reply: "当前进度是已完成。"}})
			var actual string
			if err := testPool.QueryRow(ctx, `SELECT state FROM response_action WHERE id=$1`, in.ActionID).Scan(&actual); err != nil {
				t.Fatal(err)
			}
			expected := state
			if state == "pending" {
				expected = "cancelled"
			}
			if actual != expected {
				t.Fatal(actual, expected)
			}
			var final int
			if err := testPool.QueryRow(ctx, `SELECT count(*) FROM response_action WHERE agent_id=$1 AND input->>'employee_message_job_id'=$2`, f.agentID, h.job.ID).Scan(&final); err != nil || final != 1 {
				t.Fatal("final blocked", final, err)
			}
			var suppressed *dingtalkresponse.SuppressSendError
			if err := f.h.BeforeEmployeeRunNoticeSend(ctx, in); !errors.As(err, &suppressed) {
				t.Fatal("terminal feedback allowed", err)
			}
		})
	}
}
func TestEmployeeFirstFeedbackSourceAndQuietBoundaries(t *testing.T) {
	for _, change := range []string{"quiet", "principal", "scope", "reaction", "multi-source", "second-request", "not-offered", "task-wake"} {
		t.Run(change, func(t *testing.T) {
			f, h, id, s := firstFeedbackFixture(t)
			ctx := context.Background()
			call := firstFeedbackCall(s)
			switch change {
			case "quiet":
				if _, err := testPool.Exec(ctx, `INSERT INTO employee_scene_participation(workspace_id,agent_id,tenant_org_id,scene_id,mode,requester_ref,source_job_id,source_ref,instruction_quote,revision) VALUES($1,$2,$3,$4,'quiet',$5,$6,$7,'quiet',1)`, h.job.Scope.WorkspaceID, h.job.Scope.AgentID, h.job.Scope.TenantOrgID, h.job.Scope.SceneID, s.RequesterRef, h.job.ID, s.SourceRef); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					_, _ = testPool.Exec(ctx, `DELETE FROM employee_scene_participation WHERE agent_id=$1`, f.agentID)
				})
			case "principal":
				h.envelopes[0].PrincipalID = uuid.NewString()
			case "scope":
				call.Arguments["source_ref"] = "other-source"
			case "reaction":
				h.envelopes[0].Command.Event.Data.Messages[0].Reaction = &DispatchMessageReaction{} // It no longer equals admitted source.
			case "multi-source":
				h.envelopes[0].Command.Event.Data.Messages = append(h.envelopes[0].Command.Event.Data.Messages, h.envelopes[0].Command.Event.Data.Messages[0])
			case "second-request":
				if _, err := testPool.Exec(ctx, `UPDATE employee_scene_job SET model_journal=model_journal||model_journal WHERE id=$1`, h.job.ID); err != nil {
					t.Fatal(err)
				}
			case "not-offered":
				if _, err := testPool.Exec(ctx, `UPDATE employee_scene_job SET model_journal='[{"request":{"tools":[]}}]' WHERE id=$1`, h.job.ID); err != nil {
					t.Fatal(err)
				}
			case "task-wake":
				h.job.Kind = employeeentry.KindTaskWake
			}
			if _, err := h.Execute(ctx, id, call); err == nil {
				t.Fatal("unsafe feedback accepted", change)
			}
			var n int
			if err := testPool.QueryRow(ctx, `SELECT count(*) FROM employee_first_feedback WHERE job_id=$1`, h.job.ID).Scan(&n); err != nil || n != 0 {
				t.Fatal(n, err)
			}
		})
	}
}
func TestEmployeeFirstFeedbackSendRechecksQuietAndFrozenAction(t *testing.T) {
	f, h, id, s := firstFeedbackFixture(t)
	ctx := context.Background()
	if _, err := h.Execute(ctx, id, firstFeedbackCall(s)); err != nil {
		t.Fatal(err)
	}
	in := firstFeedbackAction(t, h.job.ID)
	forged := in
	forged.Text = "不同的正文"
	var suppressed *dingtalkresponse.SuppressSendError
	if err := f.h.BeforeEmployeeRunNoticeSend(ctx, forged); !errors.As(err, &suppressed) {
		t.Fatal("mutated action accepted", err)
	}
	if _, err := testPool.Exec(ctx, `INSERT INTO employee_scene_participation(workspace_id,agent_id,tenant_org_id,scene_id,mode,requester_ref,source_job_id,source_ref,instruction_quote,revision) VALUES($1,$2,$3,$4,'quiet',$5,$6,$7,'quiet',1)`, h.job.Scope.WorkspaceID, h.job.Scope.AgentID, h.job.Scope.TenantOrgID, h.job.Scope.SceneID, s.RequesterRef, h.job.ID, s.SourceRef); err != nil {
		t.Fatal(err)
	}
	if err := f.h.BeforeEmployeeRunNoticeSend(ctx, in); !errors.As(err, &suppressed) {
		t.Fatal("paused pending feedback sent", err)
	}
	if _, err := testPool.Exec(ctx, `DELETE FROM employee_scene_participation WHERE agent_id=$1`, f.agentID); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `DELETE FROM member WHERE workspace_id=$1 AND user_id=$2`, h.job.Scope.WorkspaceID, testUserID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `INSERT INTO member(workspace_id,user_id,role) VALUES($1,$2,'admin') ON CONFLICT DO NOTHING`, h.job.Scope.WorkspaceID, testUserID)
	})
	if err := f.h.BeforeEmployeeRunNoticeSend(ctx, in); !errors.As(err, &suppressed) {
		t.Fatal("revoked principal accepted", err)
	}
}
func TestEmployeeFirstFeedbackPublicTextBounds(t *testing.T) {
	for _, text := range []string{"", strings.Repeat("中", 81), "{\"tool\":\"read_task\"}", "```code```", "第一句\n第二句"} {
		if _, err := firstFeedbackText(map[string]any{"source_ref": "x", "intent": "lookup", "text": text}); err == nil {
			t.Fatal(text)
		}
	}
	if _, err := firstFeedbackText(map[string]any{"source_ref": "x", "intent": "lookup", "text": strings.Repeat("中", 80)}); err != nil {
		t.Fatal(err)
	}
}

func TestEmployeeFirstFeedbackNoticeFailureRollsBackOutbox(t *testing.T) {
	f, h, id, s := firstFeedbackFixture(t)
	ctx := context.Background()
	name := "feedback_fail_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := testPool.Exec(ctx, `CREATE FUNCTION `+name+`() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected feedback persistence failure'; END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `CREATE TRIGGER `+name+` BEFORE INSERT ON employee_first_feedback FOR EACH ROW WHEN (NEW.agent_id='`+f.agentID+`'::uuid) EXECUTE FUNCTION `+name+`() `); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DROP TRIGGER IF EXISTS `+name+` ON employee_first_feedback`)
		_, _ = testPool.Exec(ctx, `DROP FUNCTION IF EXISTS `+name+`()`)
	})
	aborted := false
	h.abort = func() { aborted = true }
	if _, err := h.Execute(ctx, id, firstFeedbackCall(s)); !errors.Is(err, employeeloop.ErrToolRefused) {
		t.Fatal("optional notice failure broke business loop", err)
	}
	if aborted {
		t.Fatal("optional notice failure aborted loop")
	}
	if _, err := h.Execute(ctx, id, employeeReadCall(s)); err != nil {
		t.Fatal("read after optional notice failure failed", err)
	}
	var notice, outbox int
	if err := testPool.QueryRow(ctx, `SELECT (SELECT count(*) FROM employee_first_feedback WHERE job_id=$1),(SELECT count(*) FROM response_action WHERE input->>'employee_first_feedback_job_id'=$1::text)`, h.job.ID).Scan(&notice, &outbox); err != nil || notice != 0 || outbox != 0 {
		t.Fatal("feedback and outbox not atomic", notice, outbox, err)
	}
}
