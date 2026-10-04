package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/humanquestion"
	"github.com/multica-ai/multica/server/internal/service/employeeloop"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// employeeHumanQuote freezes provider-verified authority for one outer source.
// A missing provider message id is never replaced by a timestamp or latest Task.
type employeeHumanQuote struct {
	SourceRef      string `json:"source_ref"`
	Requester      string `json:"requester_ref"`
	MessageID      string `json:"quoted_message_id"`
	ConversationID string `json:"conversation_id"`
	Outcome        string `json:"outcome"`
	QuestionRef    string `json:"question_ref,omitempty"`
	Reason         string `json:"reason,omitempty"`
}

func employeeHumanQuoteCandidates(ctx context.Context, database employeeQueryer, scope employeeentry.Scope, requester, conversation, message string) ([]string, error) {
	rows, err := database.Query(ctx, `SELECT DISTINCT q.id::text FROM employee_human_question q
 LEFT JOIN response_action a ON a.id=q.action_id AND a.workspace_id=q.workspace_id AND a.agent_id=q.agent_id
  AND a.input->>'dws_org_id'=q.tenant_org_id AND a.input->>'scene_id'=q.scene_id::text AND a.state<>'failed'
 LEFT JOIN a2ui_interaction i ON i.id=q.id AND i.workspace_id=q.workspace_id AND i.agent_id=q.agent_id
  AND i.sender_org_id=q.tenant_org_id AND i.scene_id=q.scene_id::text
 WHERE q.workspace_id=$1::uuid AND q.agent_id=$2::uuid AND q.tenant_org_id=$3 AND q.scene_id=$4::uuid
  AND q.requester_ref=$5 AND q.state IN ('open','deferred')
  AND ((a.provider_conversation_id=$6 AND a.provider_message_id=$7)
   OR (i.conversation_id=$6 AND i.message_id=$7)) ORDER BY q.id::text LIMIT 2`, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, scope.SceneID, requester, conversation, message)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func employeeQuotedCardPlaceholder(source employeeSourceMessage) bool {
	return source.Message.ReferencedMessage != nil && strings.TrimSpace(source.Message.ReferencedMessage.Text) == "[互动卡片]"
}

func (w *EmployeeSceneWorker) humanQuoteBindings(ctx context.Context, job employeeentry.Job, envelopes []employeeDispatchEnvelope) ([]employeeHumanQuote, error) {
	database, ok := employeeEntryDB(w.handler)
	if !ok {
		return nil, humanquestion.ErrInvalid
	}
	provider := w.ResourceProvider
	if provider == nil && w.handler.DingTalkResponses != nil {
		provider = w.handler.DingTalkResponses
	}
	var conversation string
	registered, err := employeeSceneFence(ctx, w.handler, job)
	if err != nil {
		return nil, err
	}
	conversation = registered.ExternalSceneID
	self := ""
	if s, e := w.handler.Queries.GetAgentDWSNativeSubscription(ctx, db.GetAgentDWSNativeSubscriptionParams{WorkspaceID: parseUUID(job.Scope.WorkspaceID), AgentID: parseUUID(job.Scope.AgentID)}); e == nil {
		self = strings.TrimSpace(s.SelfOpenDingtalkID)
	}
	out := []employeeHumanQuote{}
	for i, item := range job.Items {
		for _, source := range employeeSourceMessages(item, envelopes[i]) {
			ids := employeeQuotedIDs(source.Message)
			if len(ids) == 0 || source.Message.Reaction != nil {
				continue
			}
			b := employeeHumanQuote{SourceRef: source.SourceRef, Requester: source.RequesterRef, MessageID: ids[0], ConversationID: conversation, Outcome: "not_question"}
			candidates, err := employeeHumanQuoteCandidates(ctx, database, job.Scope, source.RequesterRef, conversation, b.MessageID)
			if err != nil {
				return nil, err
			}
			if len(candidates) == 0 {
				if employeeQuotedCardPlaceholder(source) {
					b.Outcome = "unresolved"
					b.Reason = "card_message_identity_missing"
				}
				out = append(out, b)
				continue
			}
			b.Outcome = "unresolved"
			b.Reason = "provider_unavailable"
			if len(candidates) > 1 {
				b.Outcome = "ambiguous"
				b.Reason = "multiple_questions"
				out = append(out, b)
				continue
			}
			if provider != nil {
				reader := newEmployeeMessageResourceReader(w, provider)
				target, _, e := reader.bind(ctx, job, envelopes)
				if e == nil {
					verifyCtx, cancel := context.WithTimeout(ctx, employeeResourceReadTimeout)
					byEmployee, reason := w.verifyQuote(verifyCtx, provider, target, source, b.MessageID, self)
					cancel()
					b.Reason = reason
					if reason == "" && byEmployee {
						b.Outcome = "exact"
						b.QuestionRef = candidates[0]
					} else if reason == "" {
						b.Reason = "quoted_not_employee"
					}
				} else {
					b.Reason = "source_binding_unavailable"
				}
			}
			out = append(out, b)
		}
	}
	return out, nil
}

// requireHumanQuote never acquires new quote authority during replay. Current
// question and source authority is rechecked inside the effect transaction.
func (h *employeeSceneHost) requireHumanQuote(ctx context.Context, tx employeeQueryer, source employeeSourceMessage, question string) error {
	if len(employeeQuotedIDs(source.Message)) == 0 {
		return nil
	}
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT input_snapshot FROM employee_scene_job WHERE id=$1::uuid`, h.job.ID).Scan(&raw); err != nil {
		return err
	}
	var saved employeeSavedInput
	if json.Unmarshal(raw, &saved) != nil {
		return employeeloop.ErrToolRefused
	}
	for _, b := range saved.HumanQuotes {
		if b.SourceRef != source.SourceRef || b.Requester != source.RequesterRef {
			continue
		}
		if b.Outcome == "not_question" {
			return nil
		}
		if b.Outcome != "exact" || b.QuestionRef != question || b.MessageID != employeeQuotedIDs(source.Message)[0] {
			return fmt.Errorf("%w: quoted card has no exact binding to this question", employeeloop.ErrToolRefused)
		}
		ids, err := employeeHumanQuoteCandidates(ctx, tx, h.job.Scope, source.RequesterRef, b.ConversationID, b.MessageID)
		if err != nil {
			return err
		}
		if len(ids) != 1 || ids[0] != question {
			return fmt.Errorf("%w: quoted card binding changed", employeeloop.ErrToolRefused)
		}
		return nil
	}
	if employeeQuotedCardPlaceholder(source) {
		return fmt.Errorf("%w: frozen quoted card binding unavailable", employeeloop.ErrToolRefused)
	}
	return nil
}
