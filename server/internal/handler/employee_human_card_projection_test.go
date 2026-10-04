package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/dwsclient"
	"github.com/multica-ai/multica/server/internal/humanquestion"
	"github.com/multica-ai/multica/server/internal/service/dingtalkresponse"
)

type humanCardUpdateProbe struct {
	mu       sync.Mutex
	calls    int
	sends    int
	biz      []string
	messages [][]string
	err      error
	started  chan struct{}
	release  chan struct{}
}

func (p *humanCardUpdateProbe) Send(context.Context, dingtalkresponse.ActionInput, string) (dwsclient.SendResult, error) {
	p.mu.Lock()
	p.sends++
	p.mu.Unlock()
	return dwsclient.SendResult{}, errors.New("projection must never send a new message")
}

func (p *humanCardUpdateProbe) Query(context.Context, dingtalkresponse.ActionInput, string) (dwsclient.SendStatus, error) {
	return dwsclient.SendStatus{}, errors.New("projection must not query a synthetic send")
}

func (p *humanCardUpdateProbe) UpdateQuestionCard(ctx context.Context, _ dingtalkresponse.ActionInput, biz string, messages []string) error {
	p.mu.Lock()
	p.calls++
	p.biz = append(p.biz, biz)
	p.messages = append(p.messages, append([]string(nil), messages...))
	err := p.err
	p.mu.Unlock()
	if p.started != nil {
		select {
		case p.started <- struct{}{}:
		default:
		}
	}
	if p.release != nil {
		select {
		case <-p.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}

func humanProjectionFixture(t *testing.T) (*dingTalkResponseFixture, agentDispatchContext, humanquestion.Question, *humanCardUpdateProbe) {
	t.Helper()
	f, dc, q := humanCardFixture(t)
	if _, err := testPool.Exec(context.Background(), `INSERT INTO agent_dingtalk_identity(agent_id,workspace_id,dws_uid,org_id,bound_by) VALUES($1::uuid,$2::uuid,$3,$4,$5::uuid)`, f.agentID, q.Scope.WorkspaceID, f.command.ExternalIdentity.DWS.UID, q.Scope.TenantOrgID, q.PrincipalID); err != nil {
		t.Fatal(err)
	}
	p := &humanCardUpdateProbe{}
	f.h.DingTalkResponses = dingtalkresponse.NewService(testPool, p, nil)
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM employee_human_card_projection WHERE agent_id=$1::uuid`, f.agentID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_dingtalk_identity WHERE agent_id=$1::uuid`, f.agentID)
	})
	return f, dc, q, p
}

func humanProjectionReceipt(t *testing.T, f *dingTalkResponseFixture, q humanquestion.Question) {
	t.Helper()
	in := humanAction(t, q.ActionID)
	if err := f.h.OnEmployeeHumanCardAccepted(context.Background(), in, dwsclient.A2UIReceipt{BizID: "actual-provider-biz-" + q.ID, ConversationID: in.ConversationID, MessageID: "actual-card-message"}); err != nil {
		t.Fatal(err)
	}
}

func humanProjectionDue(t *testing.T, q humanquestion.Question) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(), `UPDATE employee_human_card_projection SET available_at=now() WHERE question_id=$1::uuid`, q.ID); err != nil {
		t.Fatal(err)
	}
}

func humanProjectionState(t *testing.T, q humanquestion.Question) (string, string, int) {
	t.Helper()
	var state, reason string
	var attempts int
	if err := testPool.QueryRow(context.Background(), `SELECT state,last_error,attempts FROM employee_human_card_projection WHERE question_id=$1::uuid`, q.ID).Scan(&state, &reason, &attempts); err != nil {
		t.Fatal(err)
	}
	return state, reason, attempts
}

func TestEmployeeHumanCardProjectionEarlyAnswerWaitsForRealReceiptAndLocksOnce(t *testing.T) {
	f, _, q, p := humanProjectionFixture(t)
	ctx := context.Background()
	humanClick(t, f, q)
	if n, err := f.h.ReconcileEmployeeHumanCardProjections(ctx, 1); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if state, reason, _ := humanProjectionState(t, q); state != "pending" || reason != "send_receipt_pending" || p.calls != 0 || p.sends != 0 {
		t.Fatal("early answer used a guessed provider id", state, reason, p.calls, p.sends)
	}
	humanProjectionReceipt(t, f, q)
	humanProjectionDue(t, q)
	if n, err := f.h.ReconcileEmployeeHumanCardProjections(ctx, 1); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	for range 2 {
		humanClick(t, f, q)
		if n, err := f.h.ReconcileEmployeeHumanCardProjections(ctx, 1); err != nil || n != 0 {
			t.Fatal("duplicate click reopened the card", n, err)
		}
	}
	var responses, jobs, intents int
	if err := testPool.QueryRow(ctx, `SELECT (SELECT count(*) FROM employee_human_response WHERE question_id=$1::uuid),(SELECT count(*) FROM employee_scene_job WHERE agent_id=$2::uuid AND kind='human_response'),(SELECT count(*) FROM employee_human_card_projection WHERE question_id=$1::uuid)`, q.ID, f.agentID).Scan(&responses, &jobs, &intents); err != nil {
		t.Fatal(err)
	}
	if responses != 1 || jobs != 1 || intents != 1 || p.calls != 1 || p.sends != 0 || p.biz[0] != "actual-provider-biz-"+q.ID {
		t.Fatal("accepted answer or effect duplicated", responses, jobs, intents, p.calls, p.sends, p.biz)
	}
	closed := strings.Join(p.messages[0], "")
	if strings.Contains(closed, "员工手册") || !strings.Contains(closed, "办公流程") || !strings.Contains(closed, "✓") || strings.Contains(closed, `"action"`) || strings.Contains(closed, "ChoicePicker") || strings.Contains(closed, "TextField") {
		t.Fatal("card was not replaced by a closed selected-only tree", closed)
	}
}

func TestEmployeeHumanCardProjectionNativeAndTextRaceKeepsOneWinner(t *testing.T) {
	f, _, q, p := humanProjectionFixture(t)
	ctx := context.Background()
	humanProjectionReceipt(t, f, q)
	responses := []humanquestion.Response{
		{ID: uuid.NewString(), QuestionID: q.ID, EventID: "race-native", Surface: "a2ui_action", RequesterRef: q.RequesterRef, Intent: "answer", Selected: []string{"handbook"}},
		{ID: uuid.NewString(), QuestionID: q.ID, EventID: "race-typed", Surface: "chat_text", RequesterRef: q.RequesterRef, Intent: "answer", Selected: []string{"process"}, RawText: "办公流程，先别发给别人", EvidenceQuote: "办公流程"},
	}
	start := make(chan struct{})
	errs := make(chan error, len(responses))
	for _, response := range responses {
		go func(r humanquestion.Response) {
			<-start
			tx, err := testPool.Begin(ctx)
			if err != nil {
				errs <- err
				return
			}
			defer tx.Rollback(ctx)
			_, _, err = humanquestion.AcceptTx(ctx, tx, q.Scope, r, f.h.admitEmployeeHumanResponseTx)
			if err == nil {
				err = tx.Commit(ctx)
			}
			errs <- err
		}(response)
	}
	close(start)
	wins, losses := 0, 0
	for range responses {
		switch err := <-errs; {
		case err == nil:
			wins++
		case errors.Is(err, humanquestion.ErrStale):
			losses++
		default:
			t.Fatal("unexpected answer race result", err)
		}
	}
	var responseCount, jobCount, intentCount int
	var acceptedID, projectionID string
	if err := testPool.QueryRow(ctx, `SELECT q.response_id::text,p.response_id::text,
 (SELECT count(*) FROM employee_human_response WHERE question_id=q.id),
 (SELECT count(*) FROM employee_scene_job WHERE agent_id=q.agent_id AND kind='human_response'),
 (SELECT count(*) FROM employee_human_card_projection WHERE question_id=q.id)
 FROM employee_human_question q JOIN employee_human_card_projection p ON p.question_id=q.id WHERE q.id=$1::uuid`, q.ID).Scan(&acceptedID, &projectionID, &responseCount, &jobCount, &intentCount); err != nil {
		t.Fatal(err)
	}
	if wins != 1 || losses != 1 || acceptedID != projectionID || responseCount != 1 || jobCount != 1 || intentCount != 1 {
		t.Fatal("simultaneous answer created competing winners", wins, losses, acceptedID, projectionID, responseCount, jobCount, intentCount)
	}
	if n, err := f.h.ReconcileEmployeeHumanCardProjections(ctx, 1); err != nil || n != 1 || p.calls != 1 || p.sends != 0 {
		t.Fatal("answer race did not produce exactly one final card", n, err, p.calls, p.sends)
	}
}

func TestEmployeeHumanCardProjectionOrdinaryTextAlsoClosesAndRepairsOldReader(t *testing.T) {
	f, dc, q, p := humanProjectionFixture(t)
	ctx := context.Background()
	humanProjectionReceipt(t, f, q)
	f.command.Event.Data.Messages[0].OpenMsgID = "projection-typed-answer"
	f.command.Event.Data.Messages[0].Text = "办公流程，先别发给别人。"
	if response := employeeHTTP(t, f, dc, uuid.NewString()); response.Code != http.StatusAccepted {
		t.Fatal(response.Code, response.Body.String())
	}
	var receipt string
	if err := testPool.QueryRow(ctx, `SELECT c.receipt_id::text FROM employee_event_consumption c JOIN employee_scene_job j ON j.id=c.job_id WHERE c.agent_id=$1 AND j.state='pending' AND j.kind='message'`, f.agentID).Scan(&receipt); err != nil {
		t.Fatal(err)
	}
	f.h.EmployeeSceneWorker.model = &humanQuestionModel{name: "accept_human_response", args: map[string]any{"source_ref": receipt + "/projection-typed-answer", "question_ref": q.ID, "intent": "answer", "selected": []string{"process"}, "answer_quote": "办公流程"}}
	if worked, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil || !worked {
		t.Fatal(worked, err)
	}
	// Model an old reader that committed the ordinary-text answer without the
	// new projection hook. Generic interaction status may still be open.
	if _, err := testPool.Exec(ctx, `DELETE FROM employee_human_card_projection WHERE question_id=$1::uuid`, q.ID); err != nil {
		t.Fatal(err)
	}
	if n, err := f.h.ReconcileEmployeeHumanCardProjections(ctx, 1); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	var status string
	var result []byte
	if err := testPool.QueryRow(ctx, `SELECT status,result FROM a2ui_interaction WHERE id=$1::uuid`, q.ID).Scan(&status, &result); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Selected []string `json:"selected"`
		Custom   string   `json:"custom"`
	}
	if json.Unmarshal(result, &got) != nil || status != "answered" || !reflect.DeepEqual(got.Selected, []string{"o1"}) || got.Custom != f.command.Event.Data.Messages[0].Text || p.calls != 1 || p.sends != 0 {
		t.Fatal("ordinary text left the card open or lost its evidence", status, string(result), p.calls, p.sends)
	}
}

func TestEmployeeHumanCardProjectionUnknownUpdateRetriesSameFinalVersion(t *testing.T) {
	f, _, q, p := humanProjectionFixture(t)
	ctx := context.Background()
	humanProjectionReceipt(t, f, q)
	humanClick(t, f, q)
	p.err = errors.New("provider timeout after applying replacement")
	if n, err := f.h.ReconcileEmployeeHumanCardProjections(ctx, 1); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if state, reason, attempts := humanProjectionState(t, q); state != "pending" || reason != "update_unconfirmed" || attempts != 1 {
		t.Fatal(state, reason, attempts)
	}
	p.err = nil
	f.h.DingTalkResponses = dingtalkresponse.NewService(testPool, p, nil)
	humanProjectionDue(t, q)
	if n, err := f.h.ReconcileEmployeeHumanCardProjections(ctx, 1); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if state, _, attempts := humanProjectionState(t, q); state != "completed" || attempts != 2 || p.calls != 2 || !reflect.DeepEqual(p.messages[0], p.messages[1]) || p.biz[0] != p.biz[1] || p.sends != 0 {
		t.Fatal("retry created another card or another final version", state, attempts, p.calls, p.biz)
	}
}

func TestEmployeeHumanCardProjectionSuppressedOriginalDoesNotWaitOrSend(t *testing.T) {
	f, _, q, p := humanProjectionFixture(t)
	ctx := context.Background()
	humanClick(t, f, q)
	// The response outbox has proved it did not submit a card. An empty biz
	// id for an unknown send must continue waiting, but this known suppression
	// requires no provider operation and can finish the projection intent.
	if _, err := testPool.Exec(ctx, `UPDATE response_action SET state='cancelled',error_code='host_send_suppressed:human_question_closed' WHERE id=$1`, q.ActionID); err != nil {
		t.Fatal(err)
	}
	if n, err := f.h.ReconcileEmployeeHumanCardProjections(ctx, 1); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if state, reason, _ := humanProjectionState(t, q); state != "completed" || reason != "card_send_suppressed" || p.calls != 0 || p.sends != 0 {
		t.Fatal("suppressed card retried or never settled", state, reason, p.calls, p.sends)
	}
}

func TestEmployeeHumanCardProjectionLeaseExcludesConcurrentWorkerAndRecoversExpiry(t *testing.T) {
	f, _, q, p := humanProjectionFixture(t)
	ctx := context.Background()
	humanProjectionReceipt(t, f, q)
	humanClick(t, f, q)
	p.started, p.release = make(chan struct{}, 1), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := f.h.ReconcileEmployeeHumanCardProjections(ctx, 1)
		done <- err
	}()
	select {
	case <-p.started:
	case err := <-done:
		t.Fatal("first worker never reached provider", err)
	case <-time.After(5 * time.Second):
		t.Fatal("first worker did not reach provider")
	}
	if n, err := f.h.ReconcileEmployeeHumanCardProjections(ctx, 1); err != nil || n != 0 {
		t.Fatal("another replica claimed a live update lease", n, err)
	}
	close(p.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// Simulate death after provider completion but before the durable ack.
	if _, err := testPool.Exec(ctx, `UPDATE employee_human_card_projection SET state='pending',available_at=now(),lease_token=$2::uuid,lease_until=now()-interval '1 second' WHERE question_id=$1::uuid`, q.ID, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	p.started, p.release = nil, nil
	if n, err := f.h.ReconcileEmployeeHumanCardProjections(ctx, 1); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if p.calls != 2 || p.sends != 0 || !reflect.DeepEqual(p.messages[0], p.messages[1]) {
		t.Fatal("crash recovery duplicated a message or changed the winner", p.calls, p.sends)
	}
}

func TestEmployeeHumanCardProjectionRevokedSourceCannotWrite(t *testing.T) {
	for _, statement := range []string{
		`DELETE FROM agent_dispatch_endpoint WHERE agent_id=$1::uuid`,
		`DELETE FROM agent_dingtalk_identity WHERE agent_id=$1::uuid`,
		`UPDATE agent_dingtalk_identity SET dws_uid='replacement-employee' WHERE agent_id=$1::uuid`,
		`UPDATE agent_scene SET tenant_org_id='different-current-tenant' WHERE agent_id=$1::uuid`,
	} {
		t.Run(statement, func(t *testing.T) {
			f, _, q, p := humanProjectionFixture(t)
			ctx := context.Background()
			humanProjectionReceipt(t, f, q)
			humanClick(t, f, q)
			// Accepted evidence survives revocation; it grants no authority to
			// mutate a card with a retired endpoint, sender or tenant binding.
			if _, err := testPool.Exec(ctx, statement, f.agentID); err != nil {
				t.Fatal(err)
			}
			if n, err := f.h.ReconcileEmployeeHumanCardProjections(ctx, 1); err != nil || n != 1 {
				t.Fatal(n, err)
			}
			if state, reason, _ := humanProjectionState(t, q); state != "blocked" || reason != "binding_or_authority_changed" || p.calls != 0 || p.sends != 0 {
				t.Fatal("revoked source still performed an external write", state, reason, p.calls, p.sends)
			}
		})
	}
}

func TestEmployeeHumanCardResultMapsFrozenIDsAndPreservesTypedWords(t *testing.T) {
	q := humanquestion.Question{ID: uuid.NewString(), RequesterRef: "source-requester", Choice: humanquestion.Choice{Intent: "clarify", Kind: "single", Question: "选哪类资料？", Options: []humanquestion.Option{{ID: "handbook", Label: "员工手册"}, {ID: "process", Label: "办公流程"}}, AllowCustom: true}}
	r := humanquestion.Response{ID: uuid.NewString(), QuestionID: q.ID, EventID: "typed-message", Surface: "chat_text", RequesterRef: q.RequesterRef, Intent: "answer", Selected: []string{"process"}, RawText: "办公流程，先别发", EvidenceQuote: "办公流程"}
	result, err := employeeHumanCardResult(q, r)
	if err != nil || !reflect.DeepEqual(result.Selected, []string{"o1"}) || !reflect.DeepEqual(result.Labels, []string{"办公流程"}) || result.Custom != r.RawText {
		t.Fatal(result, err)
	}
	r.Selected = []string{"forged-person-id"}
	if _, err := employeeHumanCardResult(q, r); !errors.Is(err, humanquestion.ErrInvalid) {
		t.Fatal("unknown selection entered resolved projection", err)
	}
}
