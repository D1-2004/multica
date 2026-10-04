package dingtalkresponse

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/service/a2ui"
)

// A2UIQuestionCard is a frozen projection, never an actor or routing authority.
type A2UIQuestionCard struct {
	QuestionID string   `json:"question_id"`
	PublicID   string   `json:"public_id"`
	Messages   []string `json:"messages"`
}

// EnqueueA2UIQuestion stages a card in the existing response outbox. It neither
// performs an external write nor notifies workers before the caller commits.
func (s *Service) EnqueueA2UIQuestion(ctx context.Context, tx DBTX, in ActionInput, questionID, publicID string, messages []string) (string, error) {
	in.A2UICard = &A2UIQuestionCard{QuestionID: questionID, PublicID: publicID, Messages: append([]string(nil), messages...)}
	in.ActionID = ""
	in.RequestID = "a2ui-question:" + questionID
	in.SceneNoticeID = questionID
	in.RoutineRunID, in.CoordinatorWaitJobID, in.InvitationActionID, in.EmployeeRunNoticeID = "", "", "", ""
	in.TaskID, in.IssueID, in.CallbackURL, in.CloseState, in.ReplyToOpenMsgID = "", "", "", "", ""
	in.CallbackTarget = "a2ui-question"
	return s.enqueue(ctx, tx, in)
}

func validateA2UIQuestion(in ActionInput) error {
	card := in.A2UICard
	if strings.TrimSpace(in.Text) == "" {
		return errors.New("A2UI question summary is required")
	}
	id, err := uuid.Parse(card.QuestionID)
	ref, ok := a2ui.ParseRef(card.PublicID)
	if err != nil || id == uuid.Nil || !ok || ref.ID != id || (ref.Family != "ask" && ref.Family != "appr") || in.SceneNoticeID != card.QuestionID || in.RequestID != "a2ui-question:"+card.QuestionID || in.CallbackTarget != "a2ui-question" {
		return errors.New("A2UI question identity is invalid")
	}
	if _, err := uuid.Parse(in.SceneID); err != nil {
		return errors.New("A2UI question scene is invalid")
	}
	if in.CallbackURL != "" || in.CloseState != "" || in.TaskID != "" || in.IssueID != "" || in.ReplyToOpenMsgID != "" || in.CoordinatorWaitJobID != "" || in.RoutineRunID != "" || in.InvitationActionID != "" || in.EmployeeRunNoticeID != "" {
		return errors.New("A2UI question cannot close a dispatch or claim another notice")
	}
	if len(card.Messages) != 2 {
		return errors.New("A2UI question requires its complete projection")
	}
	var created, updated bool
	for _, message := range card.Messages {
		if len(message) > 1<<20 {
			return errors.New("A2UI question projection exceeds limit")
		}
		var envelope struct {
			Version string          `json:"version"`
			Create  json.RawMessage `json:"createSurface"`
			Update  json.RawMessage `json:"updateComponents"`
		}
		var keys map[string]json.RawMessage
		if json.Unmarshal([]byte(message), &envelope) != nil || json.Unmarshal([]byte(message), &keys) != nil || len(keys) != 2 || envelope.Version != "v1.0" {
			return errors.New("A2UI question projection is invalid")
		}
		var surface struct {
			SurfaceID string `json:"surfaceId"`
			DataModel struct {
				Clarification struct {
					SourceTurnID string `json:"sourceTurnId"`
					Version      string `json:"sourceProjectionVersion"`
				} `json:"clarification"`
			} `json:"dataModel"`
		}
		switch {
		case len(envelope.Create) > 0 && !created && len(envelope.Update) == 0:
			if json.Unmarshal(envelope.Create, &surface) != nil || surface.DataModel.Clarification.SourceTurnID != card.PublicID || surface.DataModel.Clarification.Version != a2ui.Version {
				return errors.New("A2UI question source reference is invalid")
			}
			created = true
		case len(envelope.Update) > 0 && !updated && len(envelope.Create) == 0:
			if json.Unmarshal(envelope.Update, &surface) != nil {
				return errors.New("A2UI question components are invalid")
			}
			updated = true
		default:
			return errors.New("A2UI question projection is incomplete")
		}
		if surface.SurfaceID != ref.SurfaceID() {
			return errors.New("A2UI question surface differs from its reference")
		}
	}
	if !created || !updated {
		return errors.New("A2UI question projection is incomplete")
	}
	return nil
}
