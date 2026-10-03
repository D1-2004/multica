package handler

import (
	"context"
	"encoding/json"
	"time"

	"github.com/multica-ai/multica/server/internal/employeeentry"
)

// recentConversation builds a temporary, read-only snapshot for a new wake.
// Its fixed watermark is the admitted job's time; restored input snapshots must
// reuse their saved value rather than reading newer history during journal replay.
func (w *EmployeeSceneWorker) recentConversation(ctx context.Context, job employeeentry.Job) (string, error) {
	if w == nil || w.handler == nil || w.store == nil || job.CreatedAt.IsZero() || len(job.Items) == 0 {
		return "", employeeentry.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if _, err := employeeSceneFence(ctx, w.handler, job); err != nil {
		return "", err
	}
	if err := employeePrincipalAllowed(ctx, w.handler, job.Scope, job.PrincipalID); err != nil {
		return "", err
	}
	request := employeeentry.RecentConversationRequest{Scope: job.Scope, PrincipalID: job.PrincipalID, Before: job.CreatedAt}
	for _, item := range job.Items {
		var envelope employeeDispatchEnvelope
		if json.Unmarshal(item.Payload, &envelope) != nil || item.PrincipalID != job.PrincipalID || envelope.PrincipalID != job.PrincipalID || envelope.Command.EventReceiptID != item.ReceiptID || dispatchSceneID(envelope.Command) != job.Scope.SceneID || dispatchRecordedOrg(envelope.Command) != job.Scope.TenantOrgID {
			return "", employeeentry.ErrInvalid
		}
		request.ExcludeReceipts = append(request.ExcludeReceipts, item.ReceiptID)
		for _, message := range envelope.Command.Event.Data.Messages {
			if message.OpenMsgID != "" {
				request.ExcludeMessages = append(request.ExcludeMessages, message.OpenMsgID)
			}
		}
	}
	history, err := w.store.RecentConversation(ctx, request)
	if err != nil {
		return "", err
	}
	for i := range history.Messages {
		history.Messages[i].Text = employeeConfigLinksInText(history.Messages[i].Text)
		history.Messages[i].Speaker = employeeConfigLinksInText(history.Messages[i].Speaker)
		history.Messages[i].SpeakerRef = employeeConfigLinksInText(history.Messages[i].SpeakerRef)
	}
	raw, err := json.Marshal(history)
	return string(raw), err
}
