package handler

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/employeeentry"
)

// recentConversation builds a temporary, read-only snapshot for a new wake.
// Its fixed watermark is the admitted job's time; restored input snapshots must
// reuse their saved value rather than reading newer history during journal replay.
// A group window also reads the scene's bounded transcript (see
// recentConversationSnapshot); callers that can start that read earlier use
// startSceneTranscript and recentConversationSnapshot directly.
func (w *EmployeeSceneWorker) recentConversation(ctx context.Context, job employeeentry.Job) (string, error) {
	snapshot, err := w.recentConversationSnapshot(ctx, job, w.startSceneTranscript(ctx, job))
	return snapshot.Raw, err
}

// recentHistory reads the admitted dialogue within its own one-second budget
// and returns the current window's text for segment relevance.
func (w *EmployeeSceneWorker) recentHistory(ctx context.Context, job employeeentry.Job) (employeeentry.RecentConversation, string, error) {
	if w == nil || w.handler == nil || w.store == nil || job.CreatedAt.IsZero() || len(job.Items) == 0 {
		return employeeentry.RecentConversation{}, "", employeeentry.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	registered, err := employeeSceneFence(ctx, w.handler, job)
	if err != nil {
		return employeeentry.RecentConversation{}, "", err
	}
	if err := employeePrincipalAllowed(ctx, w.handler, job.Scope, job.PrincipalID); err != nil {
		return employeeentry.RecentConversation{}, "", err
	}
	request := employeeentry.RecentConversationRequest{Scope: job.Scope, PrincipalID: job.PrincipalID, Before: job.CreatedAt}
	messages := []employeeSourceMessage{}
	query := []string{}
	for _, item := range job.Items {
		var envelope employeeDispatchEnvelope
		if json.Unmarshal(item.Payload, &envelope) != nil || item.PrincipalID != job.PrincipalID || envelope.PrincipalID != job.PrincipalID || envelope.Command.EventReceiptID != item.ReceiptID || dispatchSceneID(envelope.Command) != job.Scope.SceneID || dispatchRecordedOrg(envelope.Command) != job.Scope.TenantOrgID {
			return employeeentry.RecentConversation{}, "", employeeentry.ErrInvalid
		}
		request.ExcludeReceipts = append(request.ExcludeReceipts, item.ReceiptID)
		for _, message := range envelope.Command.Event.Data.Messages {
			if message.OpenMsgID != "" {
				request.ExcludeMessages = append(request.ExcludeMessages, message.OpenMsgID)
			}
			query = append(query, message.Text)
		}
		messages = append(messages, employeeSourceMessages(item, envelope)...)
	}
	// In a DM with one requester, that requester's private reset also starts
	// a new conversation. A personal reset never hides group dialogue.
	if requester, unique := employeeAutomaticPrivateRequester(registered, messages); unique {
		request.MemoryPrincipal = requester
	}
	history, err := w.store.RecentConversation(ctx, request)
	if err != nil {
		return employeeentry.RecentConversation{}, "", err
	}
	for i := range history.Messages {
		history.Messages[i].Text = employeeHistoryConfigLinks(history.Messages[i].Text)
		history.Messages[i].Speaker = employeeConfigLinksInText(history.Messages[i].Speaker)
		history.Messages[i].SpeakerRef = employeeConfigLinksInText(history.Messages[i].SpeakerRef)
	}
	text := employeeConfigLinksInText(strings.Join(query, "\n"))
	if len(text) > 512 {
		text = text[:512]
	}
	return history, text, nil
}
