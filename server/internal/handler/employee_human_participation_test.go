package handler

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/dwsclient"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/humanquestion"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service/dingtalkresponse"
	"github.com/multica-ai/multica/server/internal/service/employeeloop"
)

func humanPause(t *testing.T, q humanquestion.Question, requester string) {
	t.Helper()
	ctx := context.Background()
	_, err := testPool.Exec(ctx, `INSERT INTO employee_scene_participation(workspace_id,agent_id,tenant_org_id,scene_id,mode,requester_ref,source_job_id,source_ref,instruction_quote,revision) VALUES($1::uuid,$2::uuid,$3,$4::uuid,'quiet',$5,$6::uuid,$7,'保持安静',1) ON CONFLICT(workspace_id,agent_id,tenant_org_id,scene_id) DO UPDATE SET mode='quiet',requester_ref=EXCLUDED.requester_ref,revision=employee_scene_participation.revision+1`, q.Scope.WorkspaceID, q.Scope.AgentID, q.Scope.TenantOrgID, q.Scope.SceneID, requester, q.SourceJobID, q.SourceRef)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM employee_scene_participation WHERE agent_id=$1::uuid`, q.Scope.AgentID)
	})
}
func humanClick(t *testing.T, f *dingTalkResponseFixture, q humanquestion.Question) {
	t.Helper()
	var uid string
	if err := testPool.QueryRow(context.Background(), `SELECT sender_uid FROM a2ui_interaction WHERE id=$1::uuid`, q.ID).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	if err := f.h.HandleDWSNativeCardAction(context.Background(), dwsclient.Identity{AgentID: f.agentID, UID: uid, OrgID: q.Scope.TenantOrgID}, nativeQuestionLine(q, uuid.NewString(), q.OperatorOpenID, "o1"), nil); err != nil {
		t.Fatal(err)
	}
}
func humanAction(t *testing.T, action string) dingtalkresponse.ActionInput {
	t.Helper()
	var raw []byte
	if err := testPool.QueryRow(context.Background(), `SELECT input FROM response_action WHERE id=$1`, action).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var in dingtalkresponse.ActionInput
	if err := json.Unmarshal(raw, &in); err != nil {
		t.Fatal(err)
	}
	in.ActionID = action
	return in
}

// A click is authenticated input, but an old question is never current
// authority to restore either its owner's pause or another person's pause.
func TestEmployeeHumanPauseFencesForegroundCardAndOldClick(t *testing.T) {
	for _, pauser := range []string{"question_owner", "other_person"} {
		t.Run(pauser, func(t *testing.T) {
			f, _, q := humanCardFixture(t)
			requester := q.RequesterRef
			if pauser == "other_person" {
				requester = "other-current-requester"
			}
			humanPause(t, q, requester)
			var suppressed *dingtalkresponse.SuppressSendError
			if err := f.h.BeforeEmployeeHumanQuestionSend(context.Background(), humanAction(t, q.ActionID)); !errors.As(err, &suppressed) {
				t.Fatal("foreground card bypassed pause", err)
			}
			humanClick(t, f, q)
			model := &humanQuestionModel{name: "continue_question_work", args: map[string]any{"prompt": "整理资料", "reply": "我来整理"}}
			f.h.EmployeeSceneWorker.model = model
			if worked, err := f.h.EmployeeSceneWorker.ProcessNext(context.Background()); !worked || err != nil {
				t.Fatal(worked, err)
			}
			var tasks, notices int
			var state string
			err := testPool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM employee_task WHERE agent_id=$1),(SELECT count(*) FROM employee_host_notice WHERE agent_id=$1 AND source_kind='human_response'),state FROM employee_scene_job WHERE agent_id=$1 AND kind='human_response'`, f.agentID).Scan(&tasks, &notices, &state)
			if err != nil || tasks != 0 || notices != 0 || state != "completed" || model.calls != 0 {
				t.Fatal("old click started work or replied", tasks, notices, state, model.calls, err)
			}
			p, err := employeeReadParticipation(context.Background(), testPool, q.Scope)
			if err != nil || p.Mode != "quiet" || p.RequesterRef != requester {
				t.Fatal("old source restored participation", p, err)
			}
		})
	}
}

func TestEmployeeHumanPauseFencesEffectAndQueuedAcknowledgement(t *testing.T) {
	t.Run("effect_boundary", func(t *testing.T) {
		f, _, q := humanCardFixture(t)
		humanClick(t, f, q)
		job, err := f.h.EmployeeSceneWorker.store.Claim(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if job.Scope.AgentID != f.agentID || job.Kind != employeeentry.KindHumanResponse {
			t.Fatal("claimed another fixture's job", job.ID, job.Scope.AgentID, job.Kind)
		}
		t.Cleanup(func() {
			// This test manually claims a job; never leave its lease outstanding.
			if err := f.h.EmployeeSceneWorker.store.Hold(context.Background(), job, "test_effect_boundary_finished"); err != nil {
				t.Error(err)
			}
		})
		// Pause after claim/model input, before the transaction writes an effect.
		humanPause(t, q, q.RequesterRef)
		host := &employeeHumanResponseHost{worker: f.h.EmployeeSceneWorker, job: job}
		identity := employeeloop.Identity{WorkspaceID: job.Scope.WorkspaceID, AgentID: job.Scope.AgentID, TenantOrgID: job.Scope.TenantOrgID, Scene: scene.Ref{SceneID: job.Scope.SceneID}, ReceiptID: job.Items[0].ReceiptID}
		_, err = host.Execute(context.Background(), identity, employeeloop.ToolCall{Name: "reply", NativeToolCallID: uuid.NewString(), Arguments: map[string]any{"reply": "我来处理"}})
		if !errors.Is(err, employeeloop.ErrToolRefused) {
			t.Fatal("late pause did not fence reply effect", err)
		}
	})
	t.Run("queued_send", func(t *testing.T) {
		f, _, q := humanCardFixture(t)
		// The minimal question fixture has no native employee binding. Text
		// delivery deliberately requires the current binding, so provide it.
		if _, err := testPool.Exec(context.Background(), `INSERT INTO agent_dingtalk_identity(agent_id,workspace_id,dws_uid,org_id,bound_by) VALUES($1::uuid,$2::uuid,$3,$4,$5::uuid)`, f.agentID, q.Scope.WorkspaceID, f.command.ExternalIdentity.DWS.UID, q.Scope.TenantOrgID, testUserID); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_dingtalk_identity WHERE agent_id=$1::uuid`, f.agentID)
		})
		humanClick(t, f, q)
		f.h.EmployeeSceneWorker.model = &humanQuestionModel{name: "reply", args: map[string]any{"reply": "请再补充范围"}}
		if worked, err := f.h.EmployeeSceneWorker.ProcessNext(context.Background()); !worked || err != nil {
			t.Fatal(worked, err)
		}
		var action string
		if err := testPool.QueryRow(context.Background(), `SELECT action_id FROM employee_host_notice WHERE agent_id=$1 AND source_kind='human_response'`, f.agentID).Scan(&action); err != nil {
			var state, reason string
			var outcome []byte
			_ = testPool.QueryRow(context.Background(), `SELECT state,last_error,outcome FROM employee_scene_job WHERE agent_id=$1::uuid AND kind='human_response'`, f.agentID).Scan(&state, &reason, &outcome)
			t.Fatal("expected this response's host notice", err, state, reason, string(outcome))
		}
		in := humanAction(t, action)
		if handled, err := f.h.beforeEmployeeHumanResponseSend(context.Background(), in); !handled || err != nil {
			t.Fatal("valid queued acknowledgement rejected", handled, err)
		}
		humanPause(t, q, q.RequesterRef)
		var suppressed *dingtalkresponse.SuppressSendError
		if handled, err := f.h.beforeEmployeeHumanResponseSend(context.Background(), in); !handled || !errors.As(err, &suppressed) {
			t.Fatal("queued acknowledgement bypassed pause", handled, err)
		}
	})
}

func TestEmployeeHumanPausePreservesAuthorizedRoundEndCard(t *testing.T) {
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("requires explicit isolated DATABASE_URL")
	}
	f := employeeNoticeDatabase(t, "succeeded", false, false, func(f *dingTalkResponseFixture, _ *employeeTestModel) {
		f.h.EmployeeSceneWorker.HumanQuestionsReady = func(context.Context) (bool, error) { return true, nil }
	})
	setNoticeExecutionOutput(t, f, `{"version":"tag-round-result/v1","summary":"资料已经整理。","choice":{"intent":"clarify","kind":"single","question":"需要哪个版本？","options":[{"id":"short","label":"简版"},{"id":"full","label":"详版"}]}}`)
	if created, err := f.h.enqueueEmployeeRunNotice(context.Background(), testWorkspaceID, f.runID); !created || err != nil {
		t.Fatal(created, err)
	}
	var scope employeeentry.Scope
	var qid, action string
	if err := testPool.QueryRow(context.Background(), `SELECT workspace_id::text,agent_id::text,tenant_org_id,scene_id::text,id::text,action_id FROM employee_human_question WHERE run_id=$1::uuid`, f.runID).Scan(&scope.WorkspaceID, &scope.AgentID, &scope.TenantOrgID, &scope.SceneID, &qid, &action); err != nil {
		t.Fatal(err)
	}
	database, _ := employeeEntryDB(f.h)
	q, err := humanquestion.NewStore(database).Get(context.Background(), scope, qid)
	if err != nil {
		t.Fatal(err)
	}
	humanPause(t, q, q.RequesterRef)
	if err := f.h.BeforeEmployeeHumanQuestionSend(context.Background(), humanAction(t, action)); err != nil {
		t.Fatal("existing authorized background card suppressed", err)
	}
	if handled, err := f.h.beforeEmployeeHumanResponseSend(context.Background(), humanAction(t, action)); handled || err != nil {
		t.Fatal("background card misclassified as foreground response", handled, err)
	}
}
