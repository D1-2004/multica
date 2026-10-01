package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/multica-ai/multica/server/internal/scene"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/assoc"
	"github.com/multica-ai/multica/server/internal/service/inboundcoord"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	openai "github.com/openai/openai-go/v3"
)

type coordinatorPlanFixture struct {
	h       *Handler
	agent   db.Agent
	dc      agentDispatchContext
	command DispatchCommand
	scene   db.AgentScene
	job     db.InboundCoordinatorJob
	baseKey string
}

func newCoordinatorPlanFixture(t *testing.T, texts ...string) coordinatorPlanFixture {
	t.Helper()
	if testPool == nil || testHandler == nil {
		t.Fatal("this integration suite requires an isolated database; do not count a skip as validation")
	}
	ctx := context.Background()
	agentID := createHandlerTestAgent(t, "coordinator-plan-"+uuid.NewString(), nil)
	agent, err := testHandler.Queries.GetAgent(ctx, parseUUID(agentID))
	if err != nil {
		t.Fatal(err)
	}
	namespace := parseUUID(uuid.NewString())
	f := coordinatorPlanFixture{
		h:       &Handler{Queries: testHandler.Queries, TxStarter: testHandler.TxStarter, IssueService: testHandler.IssueService, IssueCommentService: testHandler.IssueCommentService, TaskService: testHandler.TaskService, InboundCoordinator: &inboundcoord.Coordinator{}, Assoc: assoc.NewService(assoc.NewSQLStore(testPool))},
		agent:   agent,
		dc:      agentDispatchContext{WorkspaceID: parseUUID(testWorkspaceID), UserID: parseUUID(testUserID), AgentID: agent.ID, EndpointNamespaceID: namespace, EndpointID: uuidToString(namespace)},
		baseKey: uuid.NewString(),
	}
	f.command = DispatchCommand{SchemaVersion: "2.0", AgentID: agentID, DispatchEndpointID: uuidToString(namespace), Source: DispatchSource{Platform: "dingtalk", Type: "digital_employee"}, Event: DispatchEvent{Domain: "channel", Type: "message.created", Data: DispatchEventData{Conversation: DispatchConversation{OpenConversationID: "cid-plan-" + uuid.NewString(), Type: "group"}, Sender: DispatchSender{DisplayName: "甲", UID: "uid-a"}}}}
	f.scene = registerTestScene(t, agentID, "org-plan", scene.KindGroup, f.command.Event.Data.Conversation.OpenConversationID)
	f.command.AgentScene = testSceneRef(f.scene)
	for i, text := range texts {
		name := "甲"
		uid := "uid-a"
		if i > 0 {
			name = fmt.Sprintf("同事%d", i+1)
			uid = fmt.Sprintf("uid-%d", i+1)
		}
		f.command.Event.Data.Messages = append(f.command.Event.Data.Messages, DispatchMessage{Text: text, OpenMsgID: uuid.NewString(), SenderDisplayName: name, SenderUID: uid})
	}
	raw, _ := json.Marshal(f.command)
	f.job, err = f.h.Queries.CreateInboundCoordinatorJob(ctx, db.CreateInboundCoordinatorJobParams{AcceptanceID: parseUUID(uuid.NewString()), WorkspaceID: f.dc.WorkspaceID, AgentID: agent.ID, UserID: f.dc.UserID, EndpointNamespaceID: namespace, DispatchEndpointID: f.dc.EndpointID, IdempotencyKey: f.baseKey, Command: raw, ChatSessionID: parseUUID(uuid.NewString()), UserMessageID: parseUUID(uuid.NewString()), AvailableAt: pgtype.Timestamptz{Time: time.Now().Add(-time.Second), Valid: true}})
	if err != nil {
		t.Fatal(err)
	}
	f.job.LeaseToken = parseUUID(uuid.NewString())
	if _, err := testPool.Exec(ctx, `UPDATE inbound_coordinator_job SET status='running',lease_token=$2,lease_expires_at=now()+interval '10 minutes' WHERE id=$1`, f.job.ID, f.job.LeaseToken); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM inbound_coordinator_job WHERE id=$1`, f.job.ID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM assoc_edge WHERE agent_id=$1`, agent.ID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM assoc_event WHERE agent_id=$1`, agent.ID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM assoc_task WHERE agent_id=$1`, agent.ID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE agent_id=$1`, agent.ID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE assignee_id=$1`, agent.ID)
	})
	return f
}

func (f coordinatorPlanFixture) plan(t *testing.T, issueIDs ...string) inboundcoord.Decision {
	t.Helper()
	d := inboundcoord.Decision{Action: inboundcoord.ActionIssue, UserText: "这几件我分别接着办。", PlanVersion: "window-plan-v1"}
	for i, msg := range f.command.Event.Data.Messages {
		item := inboundcoord.WindowItem{Delegator: msg.SenderDisplayName, Purpose: msg.SenderDisplayName + "委托：" + msg.Text, Intent: "other", LookInto: msg.Text + "\nscene_cid=" + f.command.Event.Data.Conversation.OpenConversationID, SourceRefs: []string{fmt.Sprintf("u%d", i+1)}, Content: msg.SenderDisplayName + " 在钉钉会话中的消息：\n\n" + msg.Text, Basis: "new_request", ActionKey: fmt.Sprintf("item-%d", i+1)}
		if i < len(issueIDs) && issueIDs[i] != "" {
			item.IssueID = issueIDs[i]
			item.Basis = "change"
		}
		d.Items = append(d.Items, item)
	}
	if len(d.Items) > 0 {
		d.Purpose = d.Items[0].Purpose
		d.LookInto = d.Items[0].LookInto
		d.Intent = d.Items[0].Intent
	}
	ctx, err := f.h.coordinatorCheckpointContext(context.Background(), f.job)
	if err != nil {
		t.Fatal(err)
	}
	if err := inboundcoord.SavePlan(ctx, d); err != nil {
		t.Fatal(err)
	}
	return d
}

func (f coordinatorPlanFixture) stored(t *testing.T) (db.InboundCoordinatorJob, inboundcoord.Decision) {
	t.Helper()
	job, err := f.h.Queries.GetInboundCoordinatorJobByAcceptance(context.Background(), f.job.AcceptanceID)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := coordinatorJobCheckpoint(job.Command)
	if err != nil || plan == nil {
		t.Fatalf("checkpoint=%#v err=%v", plan, err)
	}
	return job, *plan
}

func (f coordinatorPlanFixture) dispatch(t *testing.T) *httptest.ResponseRecorder {
	return f.dispatchObserved(t, nil)
}

func (f coordinatorPlanFixture) dispatchObserved(t *testing.T, observer func(inboundcoord.Decision)) *httptest.ResponseRecorder {
	t.Helper()
	job, _ := f.stored(t)
	ctx, err := f.h.coordinatorCheckpointContext(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}
	if observer != nil {
		ctx = inboundcoord.WithDecisionObserver(ctx, observer)
	}
	req := newRequest(http.MethodPost, "/test/coordinator-plan", nil).WithContext(ctx)
	req.Header.Set("Idempotency-Key", f.baseKey)
	w := httptest.NewRecorder()
	f.h.createAgentDispatchIssueV2(w, req, f.command, DispatchPrompt{DisplayContent: "整窗原文：" + f.allText()}, f.dc, f.agent)
	return w
}

func (f coordinatorPlanFixture) allText() string {
	var parts []string
	for _, m := range f.command.Event.Data.Messages {
		parts = append(parts, m.Text)
	}
	return strings.Join(parts, "\n")
}

func (f coordinatorPlanFixture) existingIssue(t *testing.T) string {
	t.Helper()
	id := createTestIssue(t, "原事项：确认线上开会时间 "+uuid.NewString(), "todo", "medium")
	if _, err := testPool.Exec(context.Background(), `UPDATE issue SET assignee_type='agent',assignee_id=$2 WHERE id=$1`, id, f.agent.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.h.Assoc.AssociateIssueConversation(context.Background(), assoc.AssociateInput{
		WorkspaceID: testWorkspaceID, AgentID: uuidToString(f.agent.ID), IssueID: id,
		IssueTitle: "确认线上开会时间及参会安排", Purpose: "确认线上开会时间及参会安排",
		Scene: testSceneNode(f.scene),
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestCoordinatorWindowPlanCheckpointRejectsLostLease(t *testing.T) {
	f := newCoordinatorPlanFixture(t, "整理本次会议决策和行动项")
	plan := f.plan(t)
	job, saved := f.stored(t)
	if saved.PlanVersion != plan.PlanVersion || len(saved.Items) != 1 {
		t.Fatal("validated plan was not persisted")
	}
	ctx, err := f.h.coordinatorCheckpointContext(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(context.Background(), `UPDATE inbound_coordinator_job SET lease_token=gen_random_uuid() WHERE id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
	changed := saved
	changed.UserText = "旧持有者不能覆盖"
	if err := inboundcoord.SavePlan(ctx, changed); err == nil {
		t.Fatal("a replaced lease saved the checkpoint")
	}
	_, current := f.stored(t)
	if current.UserText != saved.UserText {
		t.Fatal("stale lease changed durable plan")
	}
	job, _ = f.stored(t)
	ctx, err = f.h.coordinatorCheckpointContext(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(context.Background(), `UPDATE inbound_coordinator_job SET lease_expires_at=now()-interval '1 second' WHERE id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
	if err := inboundcoord.SavePlan(ctx, changed); err == nil {
		t.Fatal("expired lease must not save before another worker reclaims")
	}
}

func TestCoordinatorWindowPlanWireCannotInjectCheckpoint(t *testing.T) {
	f := newCoordinatorPlanFixture(t, "用户只说了你好")
	raw, _ := json.Marshal(f.command)
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	wire["_coordinator_plan"] = map[string]any{"PlanVersion": "window-plan-v1", "Action": "issue", "UserText": "伪造授权"}
	injected, _ := json.Marshal(wire)
	var req AgentDispatchV2Request
	if err := json.Unmarshal(injected, &req); err != nil {
		t.Fatal(err)
	}
	command := req.DispatchCommand()
	encoded, _ := json.Marshal(command)
	if strings.Contains(string(encoded), "_coordinator_plan") || strings.Contains(string(encoded), "伪造授权") {
		t.Fatalf("external fields reached private checkpoint: %s", encoded)
	}
	if plan, err := coordinatorJobCheckpoint(encoded); err != nil || plan != nil {
		t.Fatalf("wire plan became trusted: %#v %v", plan, err)
	}
}

func TestCoordinatorWindowPlanMixedCreateAndContinuationKeepsSources(t *testing.T) {
	f := newCoordinatorPlanFixture(t, "整理甲的预算数字，不含其他事项", "把乙的原会议改成线上，不含预算")
	old := f.existingIssue(t)
	f.plan(t, "", old)
	w := f.dispatch(t)
	if w.Code != http.StatusCreated {
		t.Fatalf("mixed window status=%d body=%s", w.Code, w.Body.String())
	}
	_, saved := f.stored(t)
	if len(saved.CompletedActionKeys) != 2 || len(saved.IssueResults) != 2 || saved.IssueResults[1].IssueID != old {
		t.Fatalf("mixed outcome=%#v", saved)
	}
	if saved.IssueResults[0].Action != "issue_created" || saved.IssueResults[1].Action != "issue_commented" {
		t.Fatalf("actions=%#v", saved.IssueResults)
	}
	for i, result := range saved.IssueResults {
		task, err := f.h.Queries.GetAgentTask(context.Background(), parseUUID(result.TaskID))
		if err != nil {
			t.Fatal(err)
		}
		var envelope persistedDispatchContext
		if err := json.Unmarshal(task.Context, &envelope); err != nil {
			t.Fatal(err)
		}
		if len(envelope.EventData.Messages) != 1 || envelope.EventData.Messages[0].Text != f.command.Event.Data.Messages[i].Text || envelope.EventData.Sender.UID != f.command.Event.Data.Messages[i].SenderUID {
			t.Fatalf("item %d received another source: %s", i, task.Context)
		}
		if i == 0 {
			issue, err := f.h.Queries.GetIssue(context.Background(), parseUUID(result.IssueID))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(issue.Description.String, f.command.Event.Data.Messages[0].Text) || strings.Contains(issue.Description.String, f.command.Event.Data.Messages[1].Text) {
				t.Fatalf("new issue body mixed sources: %s", issue.Description.String)
			}
		} else {
			comment, err := f.h.Queries.GetComment(context.Background(), parseUUID(result.CommentID))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(comment.Content, f.command.Event.Data.Messages[1].Text) || strings.Contains(comment.Content, f.command.Event.Data.Messages[0].Text) {
				t.Fatalf("continuation mixed sources: %s", comment.Content)
			}
		}
	}
}

func TestCoordinatorWindowPlanThreeItemsResumeWithoutDuplicate(t *testing.T) {
	f := newCoordinatorPlanFixture(t, "整理甲的预算和约束说明", "整理乙的会议纪要和行动项", "整理丙的评审问题和建议")
	f.plan(t)
	first := f.dispatch(t)
	if first.Code != http.StatusConflict {
		t.Fatalf("first batch=%d %s", first.Code, first.Body.String())
	}
	_, partial := f.stored(t)
	if len(partial.Items) != 3 || len(partial.CompletedActionKeys) != 2 || len(partial.IssueResults) != 2 {
		t.Fatalf("remaining request lost: %#v", partial)
	}
	blocked := f.dispatch(t)
	if blocked.Code != http.StatusConflict {
		t.Fatalf("in-flight batch must keep remaining work queued: %d %s", blocked.Code, blocked.Body.String())
	}
	for _, result := range partial.IssueResults {
		if _, err := testPool.Exec(context.Background(), `UPDATE agent_task_queue SET status='completed',completed_at=now() WHERE id=$1`, parseUUID(result.TaskID)); err != nil {
			t.Fatal(err)
		}
	}
	last := f.dispatch(t)
	if last.Code != http.StatusCreated {
		t.Fatalf("remaining batch never progressed: %d %s", last.Code, last.Body.String())
	}
	_, done := f.stored(t)
	if len(done.CompletedActionKeys) != 3 || len(done.IssueResults) != 3 {
		t.Fatalf("final outcome=%#v", done)
	}
	again := f.dispatch(t)
	if again.Code != http.StatusCreated {
		t.Fatalf("completed plan could not recover: %d %s", again.Code, again.Body.String())
	}
	var tasks, issues int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*),count(DISTINCT issue_id) FROM agent_task_queue WHERE agent_id=$1`, f.agent.ID).Scan(&tasks, &issues); err != nil {
		t.Fatal(err)
	}
	if tasks != 3 || issues != 3 {
		t.Fatalf("recovery duplicated effects: tasks=%d issues=%d", tasks, issues)
	}
}

func TestCoordinatorWindowPlanPartialFailureRecoversExistingContinuation(t *testing.T) {
	f := newCoordinatorPlanFixture(t, "更新甲的原会议时间为下午三点", "更新乙的原议题增加预算部分")
	a, b := f.existingIssue(t), f.existingIssue(t)
	f.plan(t, a, b)
	// Force the second item to fail its ownership guard after the first item
	// commits, without changing any existing service behavior or running tasks.
	if _, err := testPool.Exec(context.Background(), `UPDATE issue SET assignee_id=NULL,assignee_type=NULL WHERE id=$1`, b); err != nil {
		t.Fatal(err)
	}
	first := f.dispatch(t)
	if first.Code != http.StatusInternalServerError {
		t.Fatalf("expected partial failure: %d %s", first.Code, first.Body.String())
	}
	_, partial := f.stored(t)
	if len(partial.CompletedActionKeys) != 1 || len(partial.IssueResults) != 1 {
		t.Fatalf("first committed effect was erased: %#v", partial)
	}
	if _, err := testPool.Exec(context.Background(), `UPDATE issue SET assignee_id=$2,assignee_type='agent' WHERE id=$1`, b, f.agent.ID); err != nil {
		t.Fatal(err)
	}
	last := f.dispatch(t)
	if last.Code != http.StatusCreated {
		t.Fatalf("remaining continuation could not recover: %d %s", last.Code, last.Body.String())
	}
	_, done := f.stored(t)
	if len(done.IssueResults) != 2 || done.IssueResults[0].CommentID != partial.IssueResults[0].CommentID || done.IssueResults[0].TaskID != partial.IssueResults[0].TaskID {
		t.Fatalf("first effect changed on recovery: %#v", done)
	}
	var count int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM agent_task_queue WHERE agent_id=$1`, f.agent.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("recovery enqueued %d tasks for two original requests", count)
	}
}

func TestCoordinatorWindowPlanRecoversCommittedEffectWithoutCheckpoint(t *testing.T) {
	f := newCoordinatorPlanFixture(t, "整理本次预算决定和跟进责任人")
	f.plan(t)
	lostLease := false
	first := f.dispatchObserved(t, func(d inboundcoord.Decision) {
		if lostLease || len(d.CompletedActionKeys) != 1 {
			return
		}
		lostLease = true
		if _, err := testPool.Exec(context.Background(), `UPDATE inbound_coordinator_job SET lease_token=gen_random_uuid() WHERE id=$1`, f.job.ID); err != nil {
			t.Fatal(err)
		}
	})
	if !lostLease || first.Code != http.StatusInternalServerError {
		t.Fatalf("did not exercise committed effect before checkpoint failure: %d %s", first.Code, first.Body.String())
	}
	_, stale := f.stored(t)
	if len(stale.CompletedActionKeys) != 0 {
		t.Fatal("stale owner unexpectedly persisted a completed effect")
	}
	var beforeID string
	if err := testPool.QueryRow(context.Background(), `SELECT id FROM agent_task_queue WHERE agent_id=$1`, f.agent.ID).Scan(&beforeID); err != nil {
		t.Fatalf("effect must already exist despite checkpoint failure: %v", err)
	}
	resumed := f.dispatch(t)
	if resumed.Code != http.StatusCreated {
		t.Fatalf("recovery=%d %s", resumed.Code, resumed.Body.String())
	}
	_, done := f.stored(t)
	if len(done.CompletedActionKeys) != 1 || len(done.IssueResults) != 1 || done.IssueResults[0].TaskID != beforeID {
		t.Fatalf("recovery did not preserve the already committed effect: %#v", done)
	}
	var count int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM agent_task_queue WHERE agent_id=$1`, f.agent.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("crash recovery duplicated %d tasks", count)
	}
	active, err := f.h.Queries.CountActiveTasksForConversation(context.Background(), db.CountActiveTasksForConversationParams{WorkspaceID: f.dc.WorkspaceID, AgentID: f.agent.ID, SceneID: f.command.AgentScene.SceneID, StaleAfterSecs: sceneCapacityStaleAfter.Seconds()})
	if err != nil || active != 1 {
		t.Fatalf("recovered work must repair scene binding for recall and capacity: active=%d err=%v", active, err)
	}
}

func TestCoordinatorWindowPlanIdenticalTextKeepsUtteranceIdentity(t *testing.T) {
	command := DispatchCommand{Event: DispatchEvent{Data: DispatchEventData{
		Sender: DispatchSender{DisplayName: "甲", UID: "uid-a"},
		Messages: []DispatchMessage{
			{Text: "可以，三点没问题", SenderDisplayName: "甲", SenderUID: "uid-a"},
			{Text: "可以，三点没问题", SenderDisplayName: "乙", SenderUID: "uid-b"},
		},
	}}}
	got := windowItemCommand(command, inboundcoord.WindowItem{SourceRefs: []string{"u2"}, Delegator: "乙"})
	if len(got.Event.Data.Messages) != 1 || got.Event.Data.Messages[0].SenderUID != "uid-b" || got.Event.Data.Sender.UID != "uid-b" {
		t.Fatalf("source_refs identify an utterance even when text repeats and IDs are absent: %#v", got.Event.Data)
	}
	if len(command.Event.Data.Messages) != 2 || command.Event.Data.Messages[0].SenderUID != "uid-a" {
		t.Fatal("per-item selection mutated the sealed window")
	}
}

func TestCoordinatorWindowPlanContinuationAlsoConsumesSceneCapacity(t *testing.T) {
	f := newCoordinatorPlanFixture(t, "整理甲的新预算决定及依据", "把乙的原会议改成线上讨论")
	old, other := f.existingIssue(t), f.existingIssue(t)
	busyTask := createHandlerTestTaskForAgentOnIssue(t, uuidToString(f.agent.ID), other)
	if err := f.h.Assoc.AssociateIssueConversation(context.Background(), assoc.AssociateInput{
		WorkspaceID: testWorkspaceID, AgentID: uuidToString(f.agent.ID), IssueID: other,
		IssueTitle: "已有工作正在查询另一份资料", Purpose: "查询另一份资料供用户审阅", RunID: busyTask,
		Scene: testSceneNode(f.scene),
	}); err != nil {
		t.Fatal(err)
	}
	f.plan(t, "", old)
	first := f.dispatch(t)
	active, err := f.h.Queries.CountActiveTasksForConversation(context.Background(), db.CountActiveTasksForConversationParams{WorkspaceID: f.dc.WorkspaceID, AgentID: f.agent.ID, SceneID: f.command.AgentScene.SceneID, StaleAfterSecs: sceneCapacityStaleAfter.Seconds()})
	if first.Code != http.StatusConflict || err != nil || active > 2 {
		t.Fatalf("new work plus an idle-Issue continuation need two execution slots: status=%d active=%d err=%v body=%s", first.Code, active, err, first.Body.String())
	}
	if _, err := testPool.Exec(context.Background(), `UPDATE agent_task_queue SET status='completed',completed_at=now() WHERE id=$1`, busyTask); err != nil {
		t.Fatal(err)
	}
	last := f.dispatch(t)
	if last.Code != http.StatusCreated {
		t.Fatalf("the mixed batch did not progress after capacity freed: %d %s", last.Code, last.Body.String())
	}
	_, done := f.stored(t)
	if len(done.CompletedActionKeys) != 2 {
		t.Fatalf("one of the mixed requests was lost while waiting: %#v", done)
	}
}

func TestCoordinatorWindowPlanWorkerWaitsForRolloutBeforeClaim(t *testing.T) {
	f := newCoordinatorPlanFixture(t, "整理本次会议决策")
	if _, err := testPool.Exec(context.Background(), `UPDATE inbound_coordinator_job SET status='pending',attempt_count=0,lease_token=NULL,lease_expires_at=NULL,available_at=now()-interval '1 second' WHERE id=$1`, f.job.ID); err != nil {
		t.Fatal(err)
	}
	worker := NewInboundCoordinatorJobWorker(f.h)
	for _, gateErr := range []error{nil, fmt.Errorf("heartbeat read unavailable")} {
		f.h.InboundCoordinator.Ready = func(context.Context) (bool, error) { return false, gateErr }
		processed, err := worker.ProcessNext(context.Background())
		if processed || (err != nil) != (gateErr != nil) {
			t.Fatalf("rollout gate must return before claim: processed=%v err=%v", processed, err)
		}
		job, err := f.h.Queries.GetInboundCoordinatorJobByAcceptance(context.Background(), f.job.AcceptanceID)
		if err != nil || job.Status != "pending" || job.AttemptCount != 0 || job.LeaseToken.Valid {
			t.Fatalf("rollout wait consumed a job attempt: %#v err=%v", job, err)
		}
	}
}

type taskFinishedCompletionProbe struct {
	calls  int
	params []openai.ChatCompletionNewParams
	err    error
	reply  string
}

func (p *taskFinishedCompletionProbe) Chat(_ context.Context, params openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
	p.calls++
	p.params = append(p.params, params)
	if p.err != nil {
		return nil, p.err
	}
	encodedMessages, err := json.Marshal(params.Messages)
	if err != nil {
		return nil, err
	}
	var messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(encodedMessages, &messages); err != nil {
		return nil, err
	}
	name := "finish"
	var arguments any
	if len(params.Tools) == 1 && params.Tools[0].GetFunction() != nil && params.Tools[0].GetFunction().Name == "finish_check" {
		var proposal struct {
			CurrentWindow []struct {
				Text string `json:"text"`
			} `json:"current_window"`
			Candidate struct {
				Actions []struct {
					Kind  string `json:"kind"`
					Reply string `json:"reply"`
				} `json:"actions"`
			} `json:"candidate"`
		}
		if len(messages) == 0 {
			return nil, fmt.Errorf("finish review has no messages")
		}
		if err := json.Unmarshal([]byte(messages[len(messages)-1].Content), &proposal); err != nil {
			return nil, err
		}
		if len(proposal.CurrentWindow) != 1 || proposal.CurrentWindow[0].Text == "" || len(proposal.Candidate.Actions) != 1 || proposal.Candidate.Actions[0].Kind != "report_result" || proposal.Candidate.Actions[0].Reply == "" {
			return nil, fmt.Errorf("finish review lacks its actual request or candidate")
		}
		encodedSchema, err := json.Marshal(params.Tools[0].GetFunction().Parameters)
		if err != nil {
			return nil, err
		}
		var schema struct {
			Properties map[string]struct {
				Enum []string `json:"enum"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(encodedSchema, &schema); err != nil {
			return nil, err
		}
		requestRefs := schema.Properties["request_quote_ref"].Enum
		candidateRefs := schema.Properties["candidate_quote_ref"].Enum
		if len(requestRefs) == 0 || len(candidateRefs) == 0 {
			return nil, fmt.Errorf("finish review lacks Host quote reference options")
		}
		name = "finish_check"
		arguments = map[string]any{
			"request_quote_ref": requestRefs[0], "candidate_quote_ref": candidateRefs[0],
			"verdict": "allow", "reason": "The fixture reports only the supplied current result.", "missing_source_refs": []string{},
			"work_checks": []any{},
		}
	} else {
		resultRef := ""
		for _, message := range messages {
			if message.Role != "user" {
				continue
			}
			for _, line := range strings.Split(message.Content, "\n") {
				if strings.HasPrefix(line, "current_result_ref: ") {
					resultRef = strings.TrimPrefix(line, "current_result_ref: ")
				}
			}
		}
		if resultRef == "" {
			return nil, fmt.Errorf("completion routing lacks the Host current result ref")
		}
		reply := p.reply
		if reply == "" {
			reply = "这次没有完成，暂时没有可交付的结果。"
		}
		arguments = map[string]any{"actions": []map[string]string{{"kind": "report_result", "result_ref": resultRef, "reply": reply}}}
	}
	raw, err := json.Marshal(arguments)
	if err != nil {
		return nil, err
	}
	return &openai.ChatCompletion{Choices: []openai.ChatCompletionChoice{{Message: openai.ChatCompletionMessage{Role: "assistant", ToolCalls: []openai.ChatCompletionMessageToolCallUnion{{ID: "completion-" + name, Type: "function", Function: openai.ChatCompletionMessageFunctionToolCallFunction{Name: name, Arguments: string(raw)}}}}}}}, nil
}

func (f coordinatorPlanFixture) finishedTask(t *testing.T, status string) db.AgentTaskQueue {
	t.Helper()
	issue := f.existingIssue(t)
	id := createHandlerTestTaskForAgentOnIssue(t, uuidToString(f.agent.ID), issue)
	command := f.command
	command.CompletionCallback = &DispatchCompletionCallback{URL: "/api/v1/dispatch-tasks/" + uuid.NewString() + "/execution-result", Target: testRouterTargetIdentity}
	raw, err := inboundcoord.IndependentIssueTaskContext(dispatchRuntimeContext(command, uuid.NewString()), inboundcoord.CoordinatorIssueTriggerCreate)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(context.Background(), `UPDATE agent_task_queue SET status=$2,completed_at=now(),context=$3,result='{"output":"unverified-success-from-task-text"}'::jsonb WHERE id=$1`, id, status, raw); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(context.Background(), `UPDATE agent SET task_finished_loop_enabled=true WHERE id=$1`, f.agent.ID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM task_completion_outbox WHERE callback_url=$1`, command.CompletionCallback.URL)
	})
	task, err := f.h.Queries.GetAgentTask(context.Background(), parseUUID(id))
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func TestCoordinatorWindowPlanFailedTaskExplainsFailureWithoutNewWork(t *testing.T) {
	f := newCoordinatorPlanFixture(t, "整理本次会议决策")
	task := f.finishedTask(t, "failed")
	probe := &taskFinishedCompletionProbe{}
	f.h.InboundCoordinator.Chat = probe
	if err := f.h.runPersistedTaskFinishedLoop(context.Background(), uuidToString(task.ID)); err != nil {
		t.Fatal(err)
	}
	if probe.calls != 2 {
		t.Fatalf("a terminal failure needs one routing decision and one finish review: %d", probe.calls)
	}
	messages, _ := json.Marshal(probe.params[0].Messages)
	if !strings.Contains(string(messages), "本次执行已失败") || strings.Contains(string(messages), "unverified-success-from-task-text") {
		t.Fatalf("failed task text was promoted as a successful result: %s", messages)
	}
	for _, def := range probe.params[0].Tools {
		fn := def.GetFunction()
		if fn == nil || (fn.Name != "finish" && fn.Name != "issue_get" && fn.Name != "issue_comment_list") {
			t.Fatalf("failure completion exposed a work tool: %#v", fn)
		}
	}
	if len(probe.params[1].Tools) != 1 || probe.params[1].Tools[0].GetFunction() == nil || probe.params[1].Tools[0].GetFunction().Name != "finish_check" {
		t.Fatal("completion must pass the independent finish review before delivery")
	}
	var tasks int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM agent_task_queue WHERE agent_id=$1`, f.agent.ID).Scan(&tasks); err != nil {
		t.Fatal(err)
	}
	if tasks != 1 {
		t.Fatalf("failure explanation created %d tasks", tasks)
	}
	callback, _, _ := inboundcoord.WrapupCallback(task.Context)
	var outboxes int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM task_completion_outbox WHERE callback_url=$1`, callback).Scan(&outboxes); err != nil {
		t.Fatal(err)
	}
	if outboxes != 1 {
		t.Fatalf("failure explanation must persist one independent delivery: %d", outboxes)
	}
}

func TestCoordinatorWindowPlanTaskFinishedReportsCurrentResultWithoutNewWork(t *testing.T) {
	f := newCoordinatorPlanFixture(t, "整理本次会议决策")
	task := f.finishedTask(t, "completed")
	const result = "本次会议决策：下周一启动评审。"
	resultJSON, _ := json.Marshal(map[string]string{"output": result})
	if _, err := testPool.Exec(context.Background(), `UPDATE agent_task_queue SET result=$2 WHERE id=$1`, task.ID, resultJSON); err != nil {
		t.Fatal(err)
	}
	probe := &taskFinishedCompletionProbe{reply: result}
	f.h.InboundCoordinator.Chat = probe
	if err := f.h.runPersistedTaskFinishedLoop(context.Background(), uuidToString(task.ID)); err != nil {
		t.Fatal(err)
	}
	if probe.calls != 2 {
		t.Fatalf("current result reporting needs one routing decision and one finish review: %d", probe.calls)
	}
	messages, _ := json.Marshal(probe.params[0].Messages)
	if !strings.Contains(string(messages), result) {
		t.Fatal("completion routing did not receive the current task result")
	}
	var tasks int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM agent_task_queue WHERE agent_id=$1`, f.agent.ID).Scan(&tasks); err != nil {
		t.Fatal(err)
	}
	if tasks != 1 {
		t.Fatalf("result reporting created %d tasks", tasks)
	}
	callback, _, _ := inboundcoord.WrapupCallback(task.Context)
	var outboxes int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM task_completion_outbox WHERE callback_url=$1`, callback).Scan(&outboxes); err != nil {
		t.Fatal(err)
	}
	if outboxes != 1 {
		t.Fatalf("result reporting must persist one independent delivery: %d", outboxes)
	}
}

func TestCoordinatorWindowPlanTaskFinishedModelFailureKeepsJobRetryable(t *testing.T) {
	f := newCoordinatorPlanFixture(t, "整理本次会议决策")
	task := f.finishedTask(t, "failed")
	probe := &taskFinishedCompletionProbe{err: fmt.Errorf("temporary model outage")}
	f.h.InboundCoordinator.Chat = probe
	if err := f.h.runPersistedTaskFinishedLoop(context.Background(), uuidToString(task.ID)); err == nil {
		t.Fatal("model failure must propagate to the persisted worker")
	}
	if probe.calls != 1 {
		t.Fatalf("failed routing should not run the finish review: %d", probe.calls)
	}
	callback, _, _ := inboundcoord.WrapupCallback(task.Context)
	var count int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM task_completion_outbox WHERE callback_url=$1`, callback).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("a deferred decision must not enqueue a silence completion")
	}
	command := f.command
	command.TaskFinishedTaskID = uuidToString(task.ID)
	raw, _ := json.Marshal(command)
	if _, err := testPool.Exec(context.Background(), `UPDATE inbound_coordinator_job SET command=$2,status='pending',attempt_count=0,lease_token=NULL,lease_expires_at=NULL,available_at=now()-interval '1 second' WHERE id=$1`, f.job.ID, raw); err != nil {
		t.Fatal(err)
	}
	processed, err := NewInboundCoordinatorJobWorker(f.h).ProcessNext(context.Background())
	if !processed || err != nil {
		t.Fatalf("worker did not retain the failed attempt: processed=%v err=%v", processed, err)
	}
	job, err := f.h.Queries.GetInboundCoordinatorJobByAcceptance(context.Background(), f.job.AcceptanceID)
	if err != nil || job.Status != "pending" || job.AttemptCount != 1 || !job.LastError.Valid {
		t.Fatalf("completion failure was marked handled: %#v err=%v", job, err)
	}
}

func TestCoordinatorWindowPlanTaskFinishedDeliveryFailurePropagates(t *testing.T) {
	f := newCoordinatorPlanFixture(t, "整理本次会议决策")
	task := f.finishedTask(t, "failed")
	f.h.InboundCoordinator.Chat = &taskFinishedCompletionProbe{}
	f.h.TaskService = nil
	if err := f.h.runPersistedTaskFinishedLoop(context.Background(), uuidToString(task.ID)); err == nil {
		t.Fatal("an unavailable delivery service must not mark a required reply complete")
	}
	for _, action := range []inboundcoord.Action{inboundcoord.ActionReply, inboundcoord.ActionSilence} {
		if err := f.h.deliverTaskFinishedDecision(context.Background(), &task, inboundcoord.Decision{Action: action, UserText: "这次没有完成。"}); err == nil {
			t.Fatalf("%s delivery failure was swallowed", action)
		}
	}
	// Exercise an actual enqueue error as well as a missing service. The
	// malformed callback is rejected locally; no Router call is made.
	f.h.TaskService = testHandler.TaskService
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(task.Context, &payload); err != nil {
		t.Fatal(err)
	}
	callbackBody, _ := json.Marshal(map[string]string{"url": "/invalid-router-callback", "target": testRouterTargetIdentity})
	payload["coordinator_wrapup_callback"] = callbackBody
	badContext, _ := json.Marshal(payload)
	if _, err := testPool.Exec(context.Background(), `UPDATE agent_task_queue SET context=$2 WHERE id=$1`, task.ID, badContext); err != nil {
		t.Fatal(err)
	}
	if err := f.h.runPersistedTaskFinishedLoop(context.Background(), uuidToString(task.ID)); err == nil || !strings.Contains(err.Error(), "enqueue task finished reply") {
		t.Fatalf("enqueue error was not propagated by the persisted completion: %v", err)
	}
	callback, _, _ := inboundcoord.WrapupCallback(task.Context)
	var count int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM task_completion_outbox WHERE callback_url=$1`, callback).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("failed delivery left a false completion receipt")
	}
}

func TestCoordinatorWindowPlanTaskFinishedRetryOrCancelKeepsQuiet(t *testing.T) {
	for _, status := range []string{"failed", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			f := newCoordinatorPlanFixture(t, "整理本次会议决策")
			task := f.finishedTask(t, status)
			wantTasks := 1
			if status == "failed" {
				createHandlerTestTaskForAgentOnIssue(t, uuidToString(f.agent.ID), uuidToString(task.IssueID))
				wantTasks = 2
			}
			probe := &taskFinishedCompletionProbe{}
			f.h.InboundCoordinator.Chat = probe
			if err := f.h.runPersistedTaskFinishedLoop(context.Background(), uuidToString(task.ID)); err != nil {
				t.Fatal(err)
			}
			if probe.calls != 0 {
				t.Fatalf("active retry or explicit cancellation should not trigger another model reply: %d", probe.calls)
			}
			var count int
			if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM agent_task_queue WHERE agent_id=$1`, f.agent.ID).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != wantTasks {
				t.Fatalf("quiet completion created work: got=%d want=%d", count, wantTasks)
			}
		})
	}
}

func TestCoordinatorWindowPlanContinuationPreservesTokenAndSingleDeliverable(t *testing.T) {
	f := newCoordinatorPlanFixture(t, "原会议改到三点，另外查一下明天天气。")
	f.command.ExternalIdentity.ContextToken = "fixture-private-identity-token"
	old := f.existingIssue(t)
	plan := f.plan(t, old)
	plan.Items[0].Purpose = "甲委托：把原会议改到三点"
	plan.Items[0].LookInto = "仅调整原会议时间"
	other := plan.Items[0]
	other.IssueID = ""
	other.Purpose = "甲委托：查询明天天气"
	other.LookInto = "仅查询天气"
	other.Basis = "new_request"
	other.ActionKey = "item-2"
	plan.Items = append(plan.Items, other)
	ctx, err := f.h.coordinatorCheckpointContext(context.Background(), f.job)
	if err != nil {
		t.Fatal(err)
	}
	if err := inboundcoord.SavePlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	w := f.dispatch(t)
	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	_, saved := f.stored(t)
	if len(saved.IssueResults) != 2 {
		t.Fatalf("outcomes=%d", len(saved.IssueResults))
	}
	result := saved.IssueResults[0]
	task, err := f.h.Queries.GetAgentTask(context.Background(), parseUUID(result.TaskID))
	if err != nil {
		t.Fatal(err)
	}
	var private map[string]any
	if err := json.Unmarshal(task.Context, &private); err != nil {
		t.Fatal(err)
	}
	if private["agent_identity_context_token"] != "fixture-private-identity-token" {
		t.Fatal("continuation lost its private execution identity")
	}
	comment, err := f.h.Queries.GetComment(context.Background(), parseUUID(result.CommentID))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(comment.Content, "本次续接只推进这一个交付物：仅调整原会议时间\n") || !strings.Contains(comment.Content, "其它已分派工作不属于本次执行范围") || !strings.Contains(comment.Content, f.command.Event.Data.Messages[0].Text) {
		t.Fatal("continuation lost its single scope or original evidence")
	}
}

func TestCoordinatorJobFinishSchemaComesOnlyFromTheJob(t *testing.T) {
	stored := []byte(`{"_finish_schema_experiment":{"arm":"expanded","salt_digest":"ab12","config_sha256":"cfg","config_generation":4}}`)
	if record := coordinatorJobFinishSchema(stored); record == nil || record.Arm != "expanded" || record.SaltDigest != "ab12" || record.ConfigSHA256 != "cfg" || record.ConfigGeneration != 4 {
		t.Fatalf("stored group not read: %#v", record)
	}
	for _, raw := range []string{`{}`, `not json`, `{"_finish_schema_experiment":"x"}`, `{"_finish_schema_experiment":{"arm":""}}`} {
		if record := coordinatorJobFinishSchema([]byte(raw)); record != nil {
			t.Fatalf("%s produced a group: %#v", raw, record)
		}
	}
	var req AgentDispatchV2Request
	if err := json.Unmarshal([]byte(`{"_finish_schema_experiment":{"arm":"pruned","salt_digest":"ab12"}}`), &req); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(req.DispatchCommand())
	if record := coordinatorJobFinishSchema(encoded); record != nil {
		t.Fatalf("a wire field became a trusted group: %s", encoded)
	}
}
