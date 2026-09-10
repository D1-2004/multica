package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const DingTalkMessageTrigger = "dingtalk_message"

// MessageObservation contains metadata only. The authenticated Router owns source
// and subscription checks; the receiving workspace and Agent never come from it.
type MessageObservation struct {
	EventID           string    `json:"eventId"`
	SourceID          string    `json:"sourceId"`
	ReceivedAt        time.Time `json:"receivedAt"`
	OccurredAt        time.Time `json:"occurredAt"`
	ConversationCID   string    `json:"conversationCid"`
	Mentioned         bool      `json:"mentioned"`
	ConversationID    string    `json:"-"`
	ConversationTitle string    `json:"-"`
	ConversationType  string    `json:"-"`
}

type MessageAutomationService struct {
	Pool      *pgxpool.Pool
	Autopilot *AutopilotService
}

func ValidateMessageMergeInterval(minutes int32) error {
	if minutes < 1 || minutes > 1440 {
		return errors.New("merge_interval_minutes must be between 1 and 1440")
	}
	return nil
}

// HasBinding is scoped to the actual assignee and workspace, never a supplied
// profile name. The same check runs again on admission and before dispatch.
func (s *MessageAutomationService) HasBinding(ctx context.Context, workspaceID, agentID pgtype.UUID) (bool, error) {
	var bound bool
	err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent a
        JOIN channel_installation ci ON ci.agent_id=a.id AND ci.workspace_id=a.workspace_id
        WHERE a.id=$1 AND a.workspace_id=$2 AND a.archived_at IS NULL
        AND ci.channel_type='dingtalk_account' AND ci.status='active'
        AND COALESCE(ci.config->>'router_source_id','')<>'')`, agentID, workspaceID).Scan(&bound)
	return bound, err
}

func (s *MessageAutomationService) Admit(ctx context.Context, workspaceID, agentID pgtype.UUID, event MessageObservation, runtimeContext []byte) (int, error) {
	if event.EventID == "" || event.SourceID == "" || event.ConversationID == "" || event.ReceivedAt.IsZero() || event.OccurredAt.IsZero() || !json.Valid(runtimeContext) {
		return 0, errors.New("invalid message observation")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT t.id,t.autopilot_id,t.merge_interval_minutes,t.message_revision
        FROM autopilot_trigger t JOIN autopilot a ON a.id=t.autopilot_id
        JOIN agent ag ON ag.id=a.assignee_id AND ag.workspace_id=a.workspace_id
        WHERE a.workspace_id=$1 AND a.assignee_type='agent' AND a.assignee_id=$2
        AND a.status='active' AND ag.archived_at IS NULL AND t.enabled AND t.kind='dingtalk_message'
        AND t.message_accept_after<=$4
        AND EXISTS(SELECT 1 FROM channel_installation ci WHERE ci.agent_id=$2 AND ci.workspace_id=$1
            AND ci.channel_type='dingtalk_account' AND ci.status='active' AND ci.config->>'router_source_id'=$3)
        ORDER BY t.id FOR UPDATE OF t`, workspaceID, agentID, event.SourceID, event.ReceivedAt)
	if err != nil {
		return 0, err
	}
	type trigger struct {
		id, ap   pgtype.UUID
		minutes  int32
		revision int64
	}
	var triggers []trigger
	for rows.Next() {
		var t trigger
		if err = rows.Scan(&t.id, &t.ap, &t.minutes, &t.revision); err != nil {
			rows.Close()
			return 0, err
		}
		triggers = append(triggers, t)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	admitted := 0
	for _, t := range triggers {
		var duplicate bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM autopilot_message_event WHERE trigger_id=$1 AND source_id=$2 AND event_id=$3)`, t.id, event.SourceID, event.EventID).Scan(&duplicate); err != nil {
			return 0, err
		}
		if duplicate {
			continue
		}
		// A configuration change cancels its old collecting window. Expired
		// windows are frozen before admitting the next message to a new window.
		if _, err = tx.Exec(ctx, `UPDATE autopilot_message_window SET status=CASE WHEN revision<>$2 OR agent_id<>$3 OR source_id<>$4 THEN 'cancelled' ELSE 'ready' END
            WHERE trigger_id=$1 AND status='collecting' AND (revision<>$2 OR agent_id<>$3 OR source_id<>$4 OR due_at<=now())`, t.id, t.revision, agentID, event.SourceID); err != nil {
			return 0, err
		}
		var windowID pgtype.UUID
		err = tx.QueryRow(ctx, `SELECT id FROM autopilot_message_window WHERE trigger_id=$1 AND status='collecting' FOR UPDATE`, t.id).Scan(&windowID)
		if errors.Is(err, pgx.ErrNoRows) {
			err = tx.QueryRow(ctx, `INSERT INTO autopilot_message_window(trigger_id,autopilot_id,workspace_id,agent_id,source_id,revision,due_at,runtime_context)
                VALUES($1,$2,$3,$4,$5,$6,now()+make_interval(mins=>$7),$8) RETURNING id`, t.id, t.ap, workspaceID, agentID, event.SourceID, t.revision, t.minutes, runtimeContext).Scan(&windowID)
		}
		if err != nil {
			return 0, err
		}
		_, err = tx.Exec(ctx, `INSERT INTO autopilot_message_event(trigger_id,source_id,event_id,window_id,conversation_id,conversation_cid,conversation_title,conversation_type,mentioned,occurred_at)
            VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, t.id, event.SourceID, event.EventID, windowID, event.ConversationID, event.ConversationCID, event.ConversationTitle, event.ConversationType, event.Mentioned, event.OccurredAt)
		if err != nil {
			return 0, err
		}
		admitted++
	}
	return admitted, tx.Commit(ctx)
}

type MessageConversationStatistics struct {
	ConversationID  string    `json:"conversation_id"`
	ConversationCID string    `json:"conversation_cid,omitempty"`
	Title           string    `json:"title"`
	Type            string    `json:"type"`
	MessageCount    int64     `json:"message_count"`
	MentionCount    int64     `json:"mention_count"`
	FirstMessageAt  time.Time `json:"first_message_at"`
	LastMessageAt   time.Time `json:"last_message_at"`
}

type MessageStatisticsPayload struct {
	Event                string                          `json:"event"`
	WindowID             string                          `json:"window_id"`
	WindowStart          time.Time                       `json:"window_start"`
	WindowEnd            time.Time                       `json:"window_end"`
	MergeIntervalMinutes int32                           `json:"merge_interval_minutes"`
	MessageCount         int64                           `json:"message_count"`
	MentionCount         int64                           `json:"mention_count"`
	ConversationCount    int                             `json:"conversation_count"`
	Conversations        []MessageConversationStatistics `json:"conversations"`
}

func (s *MessageAutomationService) Run(ctx context.Context) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		for i := 0; i < 32; i++ {
			worked, err := s.ProcessNext(ctx)
			if err != nil {
				if ctx.Err() == nil {
					slog.Error("message automation processing failed", "error", err)
				}
				break
			}
			if !worked {
				break
			}
		}
	}
}

// A session advisory lock spans dispatch, while row transactions are short.
// This avoids holding a window row lock while the normal automation service
// updates the parent automation on another connection. A crash releases the
// session lock; the stable (trigger, due_at) run key makes recovery idempotent.
func (s *MessageAutomationService) ProcessNext(ctx context.Context) (bool, error) {
	conn, err := s.Pool.Acquire(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Release()
	var id pgtype.UUID
	err = conn.QueryRow(ctx, `SELECT id FROM autopilot_message_window WHERE status IN ('collecting','ready')
        AND due_at<=now() AND next_attempt_at<=now() ORDER BY due_at,id LIMIT 1`).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	lockKey := "message-automation:" + util.UUIDToString(id)
	var locked bool
	if err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1,0))`, lockKey).Scan(&locked); err != nil {
		return false, err
	}
	if !locked {
		return false, nil
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if _, unlockErr := conn.Exec(unlockCtx, `SELECT pg_advisory_unlock(hashtextextended($1,0))`, lockKey); unlockErr != nil {
			// Never return a pooled connection with an unknown lock state.
			conn.Hijack().Close(unlockCtx)
		}
	}()
	tx, err := conn.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var triggerID, apID, workspaceID, agentID pgtype.UUID
	var source, status string
	var revision int64
	var opened, due time.Time
	var runtimeContext, payload []byte
	err = tx.QueryRow(ctx, `SELECT trigger_id,autopilot_id,workspace_id,agent_id,source_id,revision,opened_at,due_at,status,runtime_context,trigger_payload
        FROM autopilot_message_window WHERE id=$1 FOR UPDATE`, id).Scan(&triggerID, &apID, &workspaceID, &agentID, &source, &revision, &opened, &due, &status, &runtimeContext, &payload)
	if err != nil {
		return false, err
	}
	if status != "collecting" && status != "ready" {
		return false, nil
	}
	var valid bool
	var interval int32
	err = tx.QueryRow(ctx, `SELECT t.enabled AND a.status='active' AND t.kind='dingtalk_message' AND t.message_revision=$2
        AND a.workspace_id=$3 AND a.assignee_type='agent' AND a.assignee_id=$4 AND ag.archived_at IS NULL
        AND EXISTS(SELECT 1 FROM channel_installation ci WHERE ci.agent_id=$4 AND ci.workspace_id=$3
            AND ci.channel_type='dingtalk_account' AND ci.status='active' AND ci.config->>'router_source_id'=$5),
        COALESCE(t.merge_interval_minutes,0)
        FROM autopilot_trigger t JOIN autopilot a ON a.id=t.autopilot_id JOIN agent ag ON ag.id=$4
        WHERE t.id=$1 AND a.id=$6`, triggerID, revision, workspaceID, agentID, source, apID).Scan(&valid, &interval)
	if errors.Is(err, pgx.ErrNoRows) {
		valid = false
		err = nil
	}
	if err != nil {
		return false, err
	}
	if !valid {
		if _, err = tx.Exec(ctx, `UPDATE autopilot_message_window SET status='cancelled',completed_at=now() WHERE id=$1`, id); err != nil {
			return false, err
		}
		return true, tx.Commit(ctx)
	}
	ap, err := s.Autopilot.Queries.WithTx(tx).GetAutopilotInWorkspace(ctx, db.GetAutopilotInWorkspaceParams{ID: apID, WorkspaceID: workspaceID})
	if err != nil {
		return false, err
	}
	if len(payload) == 0 {
		rows, queryErr := tx.Query(ctx, `SELECT conversation_id,max(conversation_cid),max(conversation_title),max(conversation_type),count(*),count(*) FILTER(WHERE mentioned),min(occurred_at),max(occurred_at)
            FROM autopilot_message_event WHERE window_id=$1 GROUP BY conversation_id ORDER BY conversation_id`, id)
		if queryErr != nil {
			return false, queryErr
		}
		summary := MessageStatisticsPayload{Event: "dingtalk.messages.received", WindowID: util.UUIDToString(id), WindowStart: opened, WindowEnd: due, MergeIntervalMinutes: interval, Conversations: []MessageConversationStatistics{}}
		for rows.Next() {
			var c MessageConversationStatistics
			if err = rows.Scan(&c.ConversationID, &c.ConversationCID, &c.Title, &c.Type, &c.MessageCount, &c.MentionCount, &c.FirstMessageAt, &c.LastMessageAt); err != nil {
				rows.Close()
				return false, err
			}
			summary.Conversations = append(summary.Conversations, c)
			summary.MessageCount += c.MessageCount
			summary.MentionCount += c.MentionCount
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return false, err
		}
		if summary.MessageCount == 0 {
			return false, errors.New("message automation window has no messages")
		}
		summary.ConversationCount = len(summary.Conversations)
		payload, err = json.Marshal(summary)
		if err != nil {
			return false, err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE autopilot_message_window SET status='ready',trigger_payload=$2 WHERE id=$1`, id, payload); err != nil {
		return false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	run, dispatchErr := s.Autopilot.DispatchAutopilotForMessageWindow(ctx, ap, triggerID, payload, runtimeContext, due)
	if dispatchErr != nil {
		_, err = conn.Exec(ctx, `UPDATE autopilot_message_window SET next_attempt_at=now()+interval '30 seconds',last_error=$2 WHERE id=$1`, id, dispatchErr.Error())
		return true, errors.Join(dispatchErr, err)
	}
	if run == nil {
		return false, errors.New("message automation returned no run")
	}
	if _, err = conn.Exec(ctx, `UPDATE autopilot_message_window SET status='dispatched',run_id=$2,completed_at=now(),last_error=NULL WHERE id=$1`, id, run.ID); err != nil {
		return false, err
	}
	if _, err = conn.Exec(ctx, `UPDATE autopilot_trigger SET last_fired_at=now() WHERE id=$1`, triggerID); err != nil {
		return false, err
	}
	slog.Info("message automation dispatched", "window_id", util.UUIDToString(id), "autopilot_id", util.UUIDToString(apID), "run_id", util.UUIDToString(run.ID), "status", run.Status)
	return true, nil
}

// Stable occurrence identity and persisted runtime identity cover both crash
// boundaries: before the downstream effect and before linking its task/run.
func (s *AutopilotService) DispatchAutopilotForMessageWindow(ctx context.Context, ap db.Autopilot, triggerID pgtype.UUID, payload, runtimeContext []byte, due time.Time) (*db.AutopilotRun, error) {
	if !triggerID.Valid || due.IsZero() || !json.Valid(runtimeContext) {
		return nil, errors.New("invalid message automation occurrence")
	}
	planned := pgtype.Timestamptz{Time: due.UTC(), Valid: true}
	existing, err := s.Queries.GetAutopilotRunByTriggerAndPlanned(ctx, db.GetAutopilotRunByTriggerAndPlannedParams{TriggerID: triggerID, PlannedAt: planned})
	if errors.Is(err, pgx.ErrNoRows) {
		copy := *s
		copy.RuntimeContext = runtimeContext
		run, _, dispatchErr := copy.dispatchAutopilot(ctx, ap, triggerID, DingTalkMessageTrigger, payload, planned, pgtype.UUID{}, pgtype.UUID{})
		return run, dispatchErr
	}
	if err != nil {
		return nil, err
	}
	if existing.Source != DingTalkMessageTrigger {
		return nil, fmt.Errorf("message occurrence collided with %s", existing.Source)
	}
	if isAutopilotRunComplete(existing) {
		if existing.IssueID.Valid {
			if err = s.ensureWebhookCreateIssueTask(ctx, ap, existing); err != nil {
				return &existing, err
			}
		}
		return &existing, nil
	}
	if ap.ExecutionMode == "run_only" {
		task, taskErr := s.Queries.GetAutopilotTaskByRun(ctx, existing.ID)
		if taskErr == nil {
			updated, updateErr := s.Queries.UpdateAutopilotRunRunning(ctx, db.UpdateAutopilotRunRunningParams{ID: existing.ID, TaskID: task.ID})
			if updateErr == nil {
				s.TaskSvc.NotifyTaskEnqueued(ctx, task)
			}
			return &updated, updateErr
		}
		if !errors.Is(taskErr, pgx.ErrNoRows) {
			return nil, taskErr
		}
	}
	run, _, err := s.dispatchAutopilotRun(ctx, ap, triggerID, DingTalkMessageTrigger, &existing, pgtype.UUID{})
	return run, err
}
