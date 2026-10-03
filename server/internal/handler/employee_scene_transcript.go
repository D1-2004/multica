package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/dwsclient"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/langfuse"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service/employeeloop"
	"github.com/multica-ai/multica/server/internal/service/inboundcoord"
)

// employeeTranscriptReadTimeout bounds the wake-time group read. A slow or
// failed read marks the transcript unavailable; it never fails the wake.
const employeeTranscriptReadTimeout = 2500 * time.Millisecond

// employeeSceneTranscriptLoader is the provider port. The Coordinator's DWS
// history loader implements it (inboundcoord.DingTalkHistoryRangeLoader).
type employeeSceneTranscriptLoader interface {
	LoadRange(context.Context, inboundcoord.RangeRequest) (inboundcoord.RangePage, error)
}

// employeeTranscriptRef freezes what a g<N> label in the snapshot referred to.
// Text is exactly the line body the model saw after its label.
type employeeTranscriptRef struct {
	MessageID   string    `json:"message_id"`
	SenderRef   string    `json:"sender_ref"`
	SenderName  string    `json:"sender_name"`
	SenderClass string    `json:"sender_class"`
	SaidAt      time.Time `json:"said_at"`
	Text        string    `json:"text"`
}

// employeeRecentSnapshot is the frozen history plus the facts the input
// snapshot keeps beside it (labels) and hands to storage after it is saved.
type employeeRecentSnapshot struct {
	Raw            string
	TranscriptRefs map[string]employeeTranscriptRef
	SceneMessages  []employeeentry.SceneMessageInput
}

// employeeTranscriptRead is one in-flight group read. Only its goroutine
// writes page/err before closing done.
type employeeTranscriptRead struct {
	started   time.Time
	done      chan struct{}
	cancel    context.CancelFunc
	page      inboundcoord.RangePage
	err       error
	elapsed   time.Duration
	reason    string
	uid       string
	selfOpen  map[string]bool
	windowIDs map[string]bool
}

func (w *EmployeeSceneWorker) sceneTranscriptLoader() employeeSceneTranscriptLoader {
	if w.SceneTranscript != nil {
		return w.SceneTranscript
	}
	if w.handler != nil && w.handler.InboundCoordinator != nil {
		if loader, ok := w.handler.InboundCoordinator.DWSHistory.(employeeSceneTranscriptLoader); ok {
			return loader
		}
	}
	return nil
}

// startSceneTranscript begins the bounded read of a group scene's recent
// provider history as the employee's own DWS identity, in a 2.5 s child
// context. It returns nil for any scene that is not a group chat window.
func (w *EmployeeSceneWorker) startSceneTranscript(ctx context.Context, job employeeentry.Job) *employeeTranscriptRead {
	if w == nil || w.handler == nil || job.Kind != employeeentry.KindMessage || job.CreatedAt.IsZero() || len(job.Items) == 0 {
		return nil
	}
	registered, err := employeeSceneFence(ctx, w.handler, job)
	if err != nil || registered.SceneKind != scene.KindGroup {
		return nil
	}
	read := &employeeTranscriptRead{started: time.Now(), done: make(chan struct{}), selfOpen: map[string]bool{}, windowIDs: map[string]bool{}}
	uid := ""
	for _, item := range job.Items {
		var envelope employeeDispatchEnvelope
		if json.Unmarshal(item.Payload, &envelope) != nil {
			continue
		}
		if dws := envelope.Command.ExternalIdentity.DWS; dws != nil && uid == "" {
			uid = strings.TrimSpace(dws.UID)
		}
		for _, message := range envelope.Command.Event.Data.Messages {
			if message.OpenMsgID != "" {
				read.windowIDs[message.OpenMsgID] = true
			}
		}
	}
	for _, item := range job.Items {
		var envelope employeeDispatchEnvelope
		if json.Unmarshal(item.Payload, &envelope) != nil {
			continue
		}
		for _, message := range envelope.Command.Event.Data.Messages {
			for _, mention := range message.Mentions {
				if uid != "" && mention.UID == uid && mention.OpenDingTalkID != "" {
					read.selfOpen[mention.OpenDingTalkID] = true
				}
			}
		}
	}
	read.uid = uid
	loader := w.sceneTranscriptLoader()
	switch {
	case uid == "":
		read.reason = "identity_unbound"
	case strings.TrimSpace(registered.ExternalSceneID) == "":
		read.reason = "conversation_unknown"
	case loader == nil:
		read.reason = "loader_unavailable"
	}
	if read.reason != "" {
		close(read.done)
		return read
	}
	child, cancel := context.WithTimeout(ctx, employeeTranscriptReadTimeout)
	read.cancel = cancel
	request := inboundcoord.RangeRequest{AgentID: parseUUID(job.Scope.AgentID), UID: uid, OrgID: job.Scope.TenantOrgID, ConversationID: registered.ExternalSceneID, Before: job.CreatedAt, Limit: employeeentry.TranscriptReadLimit}
	go func() {
		defer close(read.done)
		defer cancel()
		defer func() {
			if recovered := recover(); recovered != nil {
				read.err = fmt.Errorf("transcript read panic: %v", recovered)
			}
		}()
		read.page, read.err = loader.LoadRange(child, request)
		read.elapsed = time.Since(read.started)
	}()
	return read
}

// wait joins the read. A provider call that ignores its context cannot hold
// the wake beyond the read budget; its late result is discarded.
func (r *employeeTranscriptRead) wait() (inboundcoord.RangePage, string) {
	if r.reason != "" {
		return inboundcoord.RangePage{}, r.reason
	}
	remaining := employeeTranscriptReadTimeout - time.Since(r.started) + 250*time.Millisecond
	if remaining < 0 {
		remaining = 0
	}
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	select {
	case <-r.done:
	case <-timer.C:
		r.stop()
		return inboundcoord.RangePage{}, "timeout"
	}
	switch {
	case r.err == nil:
		return r.page, ""
	case errors.Is(r.err, context.DeadlineExceeded):
		return inboundcoord.RangePage{}, "timeout"
	case dwsclient.IsCrossOrgPermissionDenied(r.err):
		return inboundcoord.RangePage{}, "cross_org_denied"
	default:
		return inboundcoord.RangePage{}, "provider_error"
	}
}

func (r *employeeTranscriptRead) stop() {
	if r != nil && r.cancel != nil {
		r.cancel()
	}
}

// recentConversationSnapshot freezes the history for a new wake: the admitted
// dialogue, unlinked scene Host sends and, in a group, the transcript read,
// merged into the v1 presentation and proven replayable before it is used.
func (w *EmployeeSceneWorker) recentConversationSnapshot(ctx context.Context, job employeeentry.Job, read *employeeTranscriptRead) (employeeRecentSnapshot, error) {
	history, query, err := w.recentHistory(ctx, job)
	if err != nil {
		read.stop()
		return employeeRecentSnapshot{}, err
	}
	validate := func(raw string) error {
		return employeeloop.ValidateRecentConversation(employeeloop.HistoryPresentationConversationTurnsV1, raw)
	}
	merge := employeeentry.HistoryMerge{History: history, WindowAt: job.CreatedAt, Query: query, Validate: validate}
	out := employeeRecentSnapshot{}
	var transcript *employeeentry.SceneTranscript
	status, reason := "", ""
	var span *langfuse.Observation
	if read != nil {
		span = langfuse.TraceFromContext(ctx).StartObservation(langfuse.ObservationOptions{Type: langfuse.TypeSpan, Name: "employee_scene_transcript", StartTime: read.started})
		var lowerBound time.Time
		transcript, lowerBound, out.SceneMessages, reason = w.finishSceneTranscript(ctx, job, read, history)
		status = "loaded"
		if reason != "" {
			status = "unavailable:" + reason
		}
		merge.Transcript, merge.TranscriptStatus, merge.TranscriptSince = transcript, status, lowerBound
	}
	merged, err := employeeentry.MergeRecentConversation(merge)
	if err != nil && merge.Transcript != nil {
		// The transcript is optional material: never trade the dialogue for it.
		reason, status = "budget", "unavailable:budget"
		merge.Transcript, merge.TranscriptStatus = nil, status
		merged, err = employeeentry.MergeRecentConversation(merge)
	}
	if err != nil {
		span.End(langfuse.EndOptions{Err: err})
		return employeeRecentSnapshot{}, err
	}
	out.Raw = merged.Raw
	if len(merged.Refs) > 0 {
		out.TranscriptRefs = make(map[string]employeeTranscriptRef, len(merged.Refs))
		for _, ref := range merged.Refs {
			out.TranscriptRefs[ref.Label] = employeeTranscriptRef{MessageID: ref.Line.MessageID, SenderRef: ref.Line.SenderRef, SenderName: ref.Line.Speaker, SenderClass: string(ref.Line.Class), SaidAt: ref.Line.SentAt, Text: ref.Line.Text}
		}
	}
	metadata := map[string]any{"history_lower_bound": history.Since, "history_segments": merged.Stats.Segments, "history_collapsed": merged.Stats.Collapsed, "history_dropped": merged.Stats.Dropped}
	if read != nil {
		metadata["transcript_status"] = status
		metadata["transcript_reason"] = reason
		metadata["transcript_lines"] = merged.Stats.TranscriptLines
		metadata["transcript_bytes"] = merged.Stats.TranscriptBytes
		metadata["transcript_elapsed_ms"] = read.elapsed.Milliseconds()
		if transcript != nil {
			metadata["transcript_read"] = transcript.Read
			metadata["transcript_omitted"] = transcript.Omitted
		}
		span.End(langfuse.EndOptions{Metadata: metadata})
	}
	langfuse.TraceFromContext(ctx).AddMetadata(metadata)
	return out, nil
}

// finishSceneTranscript joins the read and filters it against the Host's own
// facts. Any failure leaves the transcript out; the reason is returned.
func (w *EmployeeSceneWorker) finishSceneTranscript(ctx context.Context, job employeeentry.Job, read *employeeTranscriptRead, history employeeentry.RecentConversation) (*employeeentry.SceneTranscript, time.Time, []employeeentry.SceneMessageInput, string) {
	page, reason := read.wait()
	if reason != "" {
		return nil, time.Time{}, nil, reason
	}
	before := job.CreatedAt.UTC()
	since := before.Add(-employeeentry.TranscriptWindow)
	evidenceCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	evidence, err := w.store.SceneTranscriptEvidence(evidenceCtx, job.Scope, since, before)
	if err != nil {
		return nil, time.Time{}, nil, "evidence_error"
	}
	reader := employeeentry.TranscriptReader{UID: read.uid, OpenIDs: read.selfOpen, OtherAgentUIDs: map[string]bool{}}
	if identities, e := w.handler.Queries.ListAgentDingTalkIdentities(evidenceCtx, parseUUID(job.Scope.WorkspaceID)); e == nil {
		for _, identity := range identities {
			if uuidToString(identity.AgentID) == job.Scope.AgentID {
				reader.DisplayName = strings.TrimSpace(identity.AccountDisplayName)
			} else if uid := strings.TrimSpace(identity.DwsUid); uid != "" && uid != read.uid {
				reader.OtherAgentUIDs[uid] = true
			}
		}
	}
	sources := make([]employeeentry.TranscriptSource, 0, len(page.Messages))
	for _, m := range page.Messages {
		source := employeeentry.TranscriptSource{ID: m.ID, SentAt: m.SentAt, Sender: m.Sender, SenderUID: m.SenderUID, SenderID: m.SenderID, SenderOpenID: m.SenderOpenID,
			SendType: m.SendType, Content: employeeHistoryConfigLinks(m.Content), Truncated: m.Truncated}
		if m.Quoted != nil {
			source.QuotedID, source.QuotedSender, source.QuotedContent, source.QuotedTruncated = m.Quoted.ID, m.Quoted.Sender, employeeHistoryConfigLinks(m.Quoted.Content), m.Quoted.Truncated
		}
		sources = append(sources, source)
	}
	historyIDs := map[string]bool{}
	for _, message := range history.Messages {
		if message.MessageID != "" {
			historyIDs[message.MessageID] = true
		}
	}
	lowerBound := since
	if evidence.ResetAt.After(lowerBound) {
		lowerBound = evidence.ResetAt
	}
	transcript := employeeentry.BuildSceneTranscript(sources, reader, employeeentry.TranscriptBounds{Org: job.Scope.TenantOrgID, Before: before, Since: since, WindowMessageIDs: read.windowIDs, HistoryMessageIDs: historyIDs, Evidence: evidence})
	return &transcript, lowerBound, employeeentry.SceneMessagesFromPage(job.Scope.TenantOrgID, sources, reader), ""
}
