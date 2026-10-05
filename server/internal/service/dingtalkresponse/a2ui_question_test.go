package dingtalkresponse

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/dwsclient"
	"github.com/multica-ai/multica/server/internal/service/a2ui"
)

func a2uiQuestionFixture() ActionInput {
	in := inputFixture()
	id := uuid.NewString()
	in.SceneID = uuid.NewString()
	in.SceneNoticeID = id
	in.RequestID = "a2ui-question:" + id
	in.CallbackURL = ""
	in.CallbackTarget = "a2ui-question"
	in.A2UICard = &A2UIQuestionCard{QuestionID: id, PublicID: "ask:" + id, Messages: []string{
		fmt.Sprintf(`{"version":"v1.0","createSurface":{"surfaceId":"s-%s","dataModel":{"clarification":{"sourceTurnId":"ask:%s","sourceProjectionVersion":%q}}}}`, id, id, a2ui.Version),
		fmt.Sprintf(`{"version":"v1.0","updateComponents":{"surfaceId":"s-%s","components":[]}}`, id),
	}}
	return in
}

func TestA2UIQuestionProjectionIsBoundToQuestion(t *testing.T) {
	in := a2uiQuestionFixture()
	if err := validateInput(in); err != nil {
		t.Fatal(err)
	}
	mutations := []func(*ActionInput){
		func(in *ActionInput) { in.A2UICard.PublicID = "ask:" + uuid.NewString() },
		func(in *ActionInput) {
			in.A2UICard.Messages[1] = strings.ReplaceAll(in.A2UICard.Messages[1], "s-"+in.A2UICard.QuestionID, "other")
		},
		func(in *ActionInput) {
			in.A2UICard.Messages[0] = strings.ReplaceAll(in.A2UICard.Messages[0], in.A2UICard.PublicID, "ask:"+uuid.NewString())
		},
		func(in *ActionInput) {
			in.CallbackURL = "https://router.example/api/v1/dispatch-tasks/task/response-receipt"
		},
		func(in *ActionInput) { in.TaskID = uuid.NewString() },
		func(in *ActionInput) { in.CloseState = "silent" },
		func(in *ActionInput) { in.Text = "" },
	}
	for index, change := range mutations {
		t.Run(fmt.Sprint(index), func(t *testing.T) {
			broken := a2uiQuestionFixture()
			change(&broken)
			if validateInput(broken) == nil {
				t.Fatal("accepted mismatched Host projection")
			}
		})
	}
	var frozen ActionInput
	raw, _ := json.Marshal(in)
	if err := json.Unmarshal(raw, &frozen); err != nil || validateInput(frozen) != nil {
		t.Fatalf("frozen projection invalid: %v", err)
	}
}

func TestA2UIAndFirstFeedbackRejectMixedFrozenActions(t *testing.T) {
	card := a2uiQuestionFixture()
	card.EmployeeFirstFeedbackJobID = uuid.NewString()
	card.EmployeeFirstFeedbackReceiptID = uuid.NewString()
	card.EmployeeFirstFeedbackSourceRef = "source"
	if err := validateInput(card); err == nil {
		t.Fatal("card accepted first-feedback routing fields")
	}
	feedback := inputFixture()
	feedback.EmployeeFirstFeedbackJobID = uuid.NewString()
	feedback.EmployeeFirstFeedbackReceiptID = uuid.NewString()
	feedback.EmployeeFirstFeedbackSourceRef = "source"
	feedback.RequestID = FirstFeedbackRequestID(feedback.EmployeeFirstFeedbackJobID, feedback.EmployeeFirstFeedbackReceiptID)
	feedback.CallbackTarget, feedback.CallbackURL = firstFeedbackTarget, ""
	if err := validateInput(feedback); err != nil {
		t.Fatal(err)
	}
	feedback.A2UICard = a2uiQuestionFixture().A2UICard
	if err := validateFirstFeedbackInput(feedback); err == nil {
		t.Fatal("first feedback accepted card payload")
	}
}

func TestA2UIAcknowledgementIsNotMessageDelivery(t *testing.T) {
	in := a2uiQuestionFixture()
	ack := &dwsclient.A2UIReceipt{BizID: "provider-card", CardInstanceID: 42}
	cases := []struct {
		name        string
		result      dwsclient.SendResult
		state, code string
	}{
		{"card-only", dwsclient.SendResult{A2UIReceipt: ack}, "provider_accepted", "a2ui_delivery_unconfirmed"},
		{"real-task", dwsclient.SendResult{A2UIReceipt: ack, OpenTaskID: "real-task"}, "provider_accepted", ""},
		{"real-message", dwsclient.SendResult{A2UIReceipt: ack, OpenConversationID: in.ConversationID, OpenMessageID: "message"}, "delivered", ""},
		{"identity-only", dwsclient.SendResult{A2UIReceipt: &dwsclient.A2UIReceipt{BizID: "provider-card", CardInstanceID: 42, DeliveryUnconfirmed: true}, OpenConversationID: in.ConversationID, OpenMessageID: "message"}, "provider_accepted", "a2ui_delivery_unconfirmed"},
		{"wrong-target", dwsclient.SendResult{A2UIReceipt: ack, OpenConversationID: "other", OpenMessageID: "message"}, "unknown", "delivery_target_mismatch"},
		{"missing-card-receipt", dwsclient.SendResult{OpenTaskID: "real-task"}, "unknown", "a2ui_receipt_missing"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state, code := a2uiSendState(in, tc.result)
			if state != tc.state || code != tc.code {
				t.Fatalf("%s/%s", state, code)
			}
		})
	}
	if !nextAttempt(&action{Input: in, State: "provider_accepted"}, time.Now()).IsZero() {
		t.Fatal("card-only acknowledgement schedules fake query")
	}
}

// These tests require an explicitly named isolated database, never ambient
// production/pre-release credentials or the legacy localhost fallback.
func TestA2UIQuestionOutboxDoesNotResubmitAndPreservesEarlyCallback(t *testing.T) {
	dsn := os.Getenv("A2UI_RESPONSE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set A2UI_RESPONSE_TEST_DATABASE_URL to an isolated test database")
	}
	t.Setenv("DATABASE_URL", dsn)
	for _, early := range []bool{false, true} {
		t.Run(fmt.Sprint(early), func(t *testing.T) {
			pool := responsePool(t)
			in := a2uiQuestionFixture()
			var id string
			provider := &fakeProvider{send: func(ctx context.Context, _ ActionInput, _ string) (dwsclient.SendResult, error) {
				if early {
					if _, err := pool.Exec(ctx, `UPDATE response_action SET state='delivered',provider_conversation_id='cid',provider_message_id='callback-message' WHERE id=$1`, id); err != nil {
						return dwsclient.SendResult{}, err
					}
				}
				return dwsclient.SendResult{A2UIReceipt: &dwsclient.A2UIReceipt{BizID: "provider-card", CardInstanceID: 42}}, nil
			}}
			svc := NewService(pool, provider, nil)
			calls := 0
			svc.OnA2UIAccepted = func(_ context.Context, got ActionInput, receipt dwsclient.A2UIReceipt) error {
				calls++
				if got.A2UICard.QuestionID != in.A2UICard.QuestionID || receipt.BizID != "provider-card" {
					t.Fatal("lost card receipt")
				}
				return nil
			}
			var err error
			id, err = svc.EnqueueA2UIQuestion(context.Background(), pool, in, in.A2UICard.QuestionID, in.A2UICard.PublicID, in.A2UICard.Messages)
			if err != nil {
				t.Fatal(err)
			}
			process(t, svc)
			dueNow(t, pool, id)
			process(t, svc)
			if provider.sends.Load() != 1 || provider.queries.Load() != 0 || calls != 1 {
				t.Fatalf("sends=%d queries=%d receipt=%d", provider.sends.Load(), provider.queries.Load(), calls)
			}
			var state, task, message string
			if err := pool.QueryRow(context.Background(), `SELECT state,provider_task_id,provider_message_id FROM response_action WHERE id=$1`, id).Scan(&state, &task, &message); err != nil {
				t.Fatal(err)
			}
			if early {
				if state != "delivered" || message != "callback-message" {
					t.Fatal("send result overwrote early callback", state, message)
				}
			} else if state != "provider_accepted" || task != "" {
				t.Fatal("ACK forged a message task", state, task)
			}
		})
	}
}
