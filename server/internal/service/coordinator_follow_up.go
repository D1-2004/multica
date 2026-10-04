package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// QueueCoordinatorFollowUp records an authorized continuation without holding
// the conversation open for an active executor. Stable DWS identity is resolved
// afresh when its independent successor launches; task-scoped tokens are not reused.
func (s *IssueCommentService) QueueCoordinatorFollowUp(ctx context.Context, p IssueCommentCreateParams) (IssueCommentCreateResult, error) {
	if s.TaskService == nil || s.TaskService.TxStarter == nil || p.IdempotencyKey == "" {
		return IssueCommentCreateResult{}, errors.New("durable follow-up requires an idempotency key and transaction store")
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(p.DispatchContext, &payload); err != nil {
		return IssueCommentCreateResult{}, err
	}
	var identity struct {
		DWS struct {
			UID   string `json:"uid"`
			OrgID string `json:"orgId"`
		} `json:"dws"`
	}
	if json.Unmarshal(payload["external_identity"], &identity) != nil || identity.DWS.UID == "" || identity.DWS.OrgID == "" {
		return IssueCommentCreateResult{}, errors.New("durable follow-up requires stable DWS identity")
	}
	delete(payload, "agent_identity_context_token")
	raw, err := json.Marshal(payload)
	if err != nil {
		return IssueCommentCreateResult{}, err
	}
	fingerprint := issueFollowUpFingerprint(p)
	tx, err := s.TaskService.TxStarter.Begin(ctx)
	if err != nil {
		return IssueCommentCreateResult{}, err
	}
	defer tx.Rollback(ctx)
	q := s.Queries.WithTx(tx)
	if _, err = q.LockIssueForExternalFollowUp(ctx, db.LockIssueForExternalFollowUpParams{ID: p.Issue.ID, WorkspaceID: p.Issue.WorkspaceID}); err != nil {
		return IssueCommentCreateResult{}, err
	}
	issue, err := q.GetIssue(ctx, p.Issue.ID)
	if err != nil {
		return IssueCommentCreateResult{}, err
	}
	if issue.AssigneeType.String != "agent" || issue.AssigneeID != p.Issue.AssigneeID {
		return IssueCommentCreateResult{}, errors.New("follow-up assignee changed")
	}
	var commentID, taskID pgtype.UUID
	var prior string
	err = tx.QueryRow(ctx, `SELECT comment_id,task_id,fingerprint FROM coordinator_issue_follow_up WHERE workspace_id=$1 AND agent_id=$2 AND idempotency_key=$3`, issue.WorkspaceID, issue.AssigneeID, p.IdempotencyKey).Scan(&commentID, &taskID, &prior)
	if err == nil {
		if prior != fingerprint {
			return IssueCommentCreateResult{}, ErrIssueFollowUpIdempotencyConflict
		}
		comment, e := q.GetComment(ctx, commentID)
		if e != nil {
			return IssueCommentCreateResult{}, e
		}
		var task db.AgentTaskQueue
		if taskID.Valid {
			task, e = q.GetAgentTask(ctx, taskID)
			if e != nil {
				return IssueCommentCreateResult{}, e
			}
		}
		return IssueCommentCreateResult{Comment: comment, Task: task}, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return IssueCommentCreateResult{}, err
	}
	comment, err := q.CreateComment(ctx, db.CreateCommentParams{IssueID: issue.ID, WorkspaceID: issue.WorkspaceID, AuthorType: "member", AuthorID: p.AuthorID, Content: p.Content, Type: "comment", ParentID: p.ParentID})
	if err != nil {
		return IssueCommentCreateResult{}, err
	}
	if len(p.AttachmentIDs) > 0 {
		if err = q.LinkAttachmentsToComment(ctx, db.LinkAttachmentsToCommentParams{CommentID: comment.ID, IssueID: issue.ID, Column3: p.AttachmentIDs}); err != nil {
			return IssueCommentCreateResult{}, err
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO coordinator_issue_follow_up(workspace_id,agent_id,issue_id,comment_id,idempotency_key,fingerprint,dispatch_context) VALUES($1,$2,$3,$4,$5,$6,$7)`, issue.WorkspaceID, issue.AssigneeID, issue.ID, comment.ID, p.IdempotencyKey, fingerprint, raw)
	if err != nil {
		return IssueCommentCreateResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return IssueCommentCreateResult{}, err
	}
	s.publishExternalFollowUpComment(p, IssueCommentCreateOpts{}, comment, nil)
	return IssueCommentCreateResult{Comment: comment}, nil
}

// RunCoordinatorFollowUps only inspects durable pending work. It never polls IM
// history or invokes a model when there are no new inputs.
func (s *IssueCommentService) RunCoordinatorFollowUps(ctx context.Context) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		for range 32 {
			ok, err := s.ProcessCoordinatorFollowUp(ctx)
			if err != nil {
				if ctx.Err() == nil {
					slog.Error("coordinator follow-up dispatch failed", "error", err)
				}
				break
			}
			if !ok {
				break
			}
		}
	}
}

func (s *IssueCommentService) ProcessCoordinatorFollowUp(ctx context.Context) (bool, error) {
	tx, err := s.TaskService.TxStarter.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	q := s.Queries.WithTx(tx)
	issue, claimed, err := claimCoordinatorFollowUpIssue(ctx, tx)
	if err != nil || !claimed {
		return false, err
	}
	agentID := issue.AssigneeID
	if err := prepareEmployeeIssueFollowUp(ctx, tx, issue); err != nil {
		if errors.Is(err, employeetask.ErrRunNotReady) {
			return false, tx.Commit(ctx)
		}
		return false, err
	}
	rows, err := tx.Query(ctx, `SELECT f.id,f.comment_id,f.dispatch_context,c.content FROM coordinator_issue_follow_up f JOIN comment c ON c.id=f.comment_id AND c.workspace_id=f.workspace_id
 WHERE f.issue_id=$1 AND f.agent_id=$2 AND f.workspace_id=$3 AND f.task_id IS NULL ORDER BY f.created_at,f.id LIMIT 100 FOR UPDATE OF f`, issue.ID, agentID, issue.WorkspaceID)
	if err != nil {
		return false, err
	}
	var ids, comments []pgtype.UUID
	var raw []byte
	var scope string
	var content []string
	var messages []json.RawMessage
	for rows.Next() {
		var id, comment pgtype.UUID
		var item []byte
		var text string
		if err = rows.Scan(&id, &comment, &item, &text); err != nil {
			rows.Close()
			return false, err
		}
		var envelope map[string]json.RawMessage
		if err = json.Unmarshal(item, &envelope); err != nil {
			rows.Close()
			return false, err
		}
		// A reply belongs to its original receiving identity and conversation.
		var data struct {
			Conversation json.RawMessage   `json:"conversation"`
			Messages     []json.RawMessage `json:"messages"`
		}
		if err = json.Unmarshal(envelope["dispatch_event_data"], &data); err != nil {
			rows.Close()
			return false, err
		}
		itemScope := string(envelope["external_identity"]) + "|" + followUpConversationScope(data.Conversation) + "|" + string(envelope["dispatch_endpoint_id"])
		if len(ids) > 0 && itemScope != scope {
			break
		}
		scope = itemScope
		raw = item
		ids = append(ids, id)
		comments = append(comments, comment)
		content = append(content, text)
		messages = append(messages, data.Messages...)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	if len(ids) == 0 {
		return false, errors.New("pending follow-up has no comment")
	}
	var envelope map[string]json.RawMessage
	if err = json.Unmarshal(raw, &envelope); err != nil {
		return false, err
	}
	var data map[string]json.RawMessage
	if err = json.Unmarshal(envelope["dispatch_event_data"], &data); err != nil {
		return false, err
	}
	data["messages"], _ = json.Marshal(messages)
	envelope["dispatch_event_data"], _ = json.Marshal(data)
	commentIDs := make([]string, len(comments))
	for i, id := range comments {
		commentIDs[i] = util.UUIDToString(id)
	}
	envelope["coordinator_follow_up_comment_ids"], _ = json.Marshal(commentIDs)
	envelope["coordinator_follow_up_content"], _ = json.Marshal(strings.Join(content, "\n\n"))
	envelope["dispatch_idempotency_key"], _ = json.Marshal("coordinator-follow-up:" + util.UUIDToString(ids[0]))
	raw, err = json.Marshal(envelope)
	if err != nil {
		return false, err
	}
	tasks := &TaskService{Queries: q, Bus: events.New(), FeatureFlags: s.TaskService.FeatureFlags, Composio: s.TaskService.Composio}
	task, err := tasks.EnqueueTaskForIssueWithDispatchContext(ctx, issue, "", raw, comments[len(comments)-1])
	if err != nil {
		return false, err
	}
	if len(comments) > 1 {
		if _, err = tx.Exec(ctx, `UPDATE agent_task_queue SET coalesced_comment_ids=$2 WHERE id=$1`, task.ID, comments[:len(comments)-1]); err != nil {
			return false, err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE coordinator_issue_follow_up SET task_id=$2,dispatched_at=now(),last_error=NULL WHERE id=ANY($1::uuid[]) AND workspace_id=$3 AND task_id IS NULL`, ids, task.ID, issue.WorkspaceID); err != nil {
		return false, err
	}
	if err = mapEmployeeIssueFollowUpQueue(ctx, tx, issue, task); err != nil {
		return false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	s.TaskService.publishIssueTaskEnqueued(ctx, task)
	slog.Info("coordinator follow-ups dispatched", "issue_id", util.UUIDToString(issue.ID), "task_id", util.UUIDToString(task.ID), "message_count", len(ids))
	return true, nil
}

// OutstandingCoordinatorFollowUps is scoped to the finished task's issue and
// recipient. These inputs have been accepted but were not in that task's snapshot.
func (s *IssueCommentService) OutstandingCoordinatorFollowUps(ctx context.Context, task db.AgentTaskQueue, cid string) (string, error) {
	tx, err := s.TaskService.TxStarter.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT c.content,COALESCE(t.status,'waiting'),f.comment_id::text FROM coordinator_issue_follow_up f
 JOIN comment c ON c.id=f.comment_id AND c.workspace_id=f.workspace_id LEFT JOIN LATERAL (
 WITH RECURSIVE attempts AS (SELECT id,status FROM agent_task_queue WHERE id=f.task_id
 UNION ALL SELECT child.id,child.status FROM agent_task_queue child JOIN attempts parent ON child.retry_of_task_id=parent.id)
 SELECT attempts.id,attempts.status FROM attempts JOIN agent_task_queue latest ON latest.id=attempts.id ORDER BY latest.created_at DESC LIMIT 1
 ) t ON true
 WHERE f.issue_id=$1 AND f.agent_id=$2 AND f.task_id IS DISTINCT FROM $3
 AND f.workspace_id=(SELECT workspace_id FROM issue WHERE id=$1)
 AND f.dispatch_context->'external_identity'=($5::jsonb)->'external_identity'
 AND t.id IS DISTINCT FROM $3
 AND f.dispatch_context->'dispatch_event_data'->'conversation'->>'openConversationId'=$4
 AND (f.task_id IS NULL OR t.status IN ('queued','dispatched','running','waiting_local_directory','deferred')) ORDER BY f.created_at LIMIT 21`, task.IssueID, task.AgentID, task.ID, cid, task.Context)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	type pending struct {
		Content   string `json:"content"`
		Status    string `json:"execution_status"`
		CommentID string `json:"comment_id"`
	}
	items := []pending{}
	for rows.Next() {
		var item pending
		if err = rows.Scan(&item.Content, &item.Status, &item.CommentID); err != nil {
			return "", err
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		return "", err
	}
	truncated := len(items) > 20
	if truncated {
		items = items[:20]
	}
	raw, err := json.Marshal(map[string]any{"status": "loaded", "coverage": "same_issue_same_recipient_not_in_finished_task", "truncated": truncated, "items": items})
	if err != nil {
		return "", fmt.Errorf("encode outstanding follow-ups: %w", err)
	}
	return string(raw), nil
}

// Only the actual claim-time delivery receipt can cover a queued supplement.
// A payload-size limit or legacy daemon must leave omitted comments pending.
func (s *IssueCommentService) ReconcileCoordinatorFollowUpReceipts(ctx context.Context, task db.AgentTaskQueue) (map[string]bool, error) {
	tx, err := s.TaskService.TxStarter.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if task.Status == "completed" {
		if _, err = tx.Exec(ctx, `WITH RECURSIVE ancestors AS (SELECT id,retry_of_task_id FROM agent_task_queue WHERE id=$1 UNION ALL SELECT parent.id,parent.retry_of_task_id FROM agent_task_queue parent JOIN ancestors child ON parent.id=child.retry_of_task_id) UPDATE coordinator_issue_follow_up SET task_id=NULL,dispatched_at=NULL WHERE task_id IN(SELECT id FROM ancestors) AND issue_id=$2 AND agent_id=$3 AND NOT(comment_id=ANY(COALESCE($4::uuid[],'{}'::uuid[])))`, task.ID, task.IssueID, task.AgentID, task.DeliveredCommentIds); err != nil {
			return nil, err
		}
	}
	rows, err := tx.Query(ctx, `SELECT comment_id::text FROM coordinator_issue_follow_up WHERE issue_id=$1 AND agent_id=$2`, task.IssueID, task.AgentID)
	if err != nil {
		return nil, err
	}
	owned := map[string]bool{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		owned[id] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	return owned, tx.Commit(ctx)
}

// Candidate discovery takes no row locks. Each attempt acquires workspace then
// Issue, and a savepoint rollback releases both locks when the candidate is busy.
// Skipping a locked oldest Issue must not suppress unrelated ready work.
func claimCoordinatorFollowUpIssue(ctx context.Context, tx pgx.Tx) (db.Issue, bool, error) {
	excluded := []pgtype.UUID{}
	for {
		var issueID, agentID, workspaceID pgtype.UUID
		err := tx.QueryRow(ctx, `SELECT i.id,i.assignee_id,i.workspace_id FROM issue i JOIN agent a ON a.id=i.assignee_id AND a.workspace_id=i.workspace_id
 WHERE i.assignee_type='agent' AND a.archived_at IS NULL AND NOT(i.id=ANY($1::uuid[]))
 AND EXISTS(SELECT 1 FROM coordinator_issue_follow_up f WHERE f.issue_id=i.id AND f.workspace_id=i.workspace_id AND f.agent_id=i.assignee_id AND f.task_id IS NULL)
 AND NOT EXISTS(SELECT 1 FROM agent_task_queue t WHERE t.issue_id=i.id AND t.agent_id=i.assignee_id AND t.status IN ('queued','dispatched','running','waiting_local_directory','deferred'))
 AND NOT EXISTS(SELECT 1 FROM employee_task et WHERE et.workspace_id=i.workspace_id AND et.agent_id=i.assignee_id AND et.issue_id=i.id AND et.owner_loop='employee' AND et.state IN ('failed','cancelled'))
 ORDER BY i.updated_at,i.id LIMIT 1`, excluded).Scan(&issueID, &agentID, &workspaceID)
		if errors.Is(err, pgx.ErrNoRows) {
			return db.Issue{}, false, nil
		}
		if err != nil {
			return db.Issue{}, false, err
		}
		excluded = append(excluded, issueID)
		issue, claimed, err := tryClaimCoordinatorFollowUpIssue(ctx, tx, workspaceID, issueID, agentID)
		if err != nil || claimed {
			return issue, claimed, err
		}
	}
}

func tryClaimCoordinatorFollowUpIssue(ctx context.Context, outer pgx.Tx, workspaceID, issueID, agentID pgtype.UUID) (db.Issue, bool, error) {
	tx, err := outer.Begin(ctx)
	if err != nil {
		return db.Issue{}, false, err
	}
	defer tx.Rollback(ctx)
	var locked pgtype.UUID
	err = tx.QueryRow(ctx, `SELECT id FROM workspace WHERE id=$1 FOR KEY SHARE SKIP LOCKED`, workspaceID).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Issue{}, false, nil
	}
	if err != nil {
		return db.Issue{}, false, err
	}
	err = tx.QueryRow(ctx, `SELECT id FROM issue WHERE id=$1 AND workspace_id=$2 AND assignee_id=$3 AND assignee_type='agent' FOR UPDATE SKIP LOCKED`, issueID, workspaceID, agentID).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Issue{}, false, nil
	}
	if err != nil {
		return db.Issue{}, false, err
	}
	q := db.New(tx)
	active, err := q.HasActiveTaskForIssueAndAgent(ctx, db.HasActiveTaskForIssueAndAgentParams{IssueID: issueID, AgentID: agentID})
	if err != nil || active {
		return db.Issue{}, false, err
	}
	var pending bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM coordinator_issue_follow_up WHERE issue_id=$1 AND workspace_id=$2 AND agent_id=$3 AND task_id IS NULL)`, issueID, workspaceID, agentID).Scan(&pending)
	if err != nil || !pending {
		return db.Issue{}, false, err
	}
	issue, err := q.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: issueID, WorkspaceID: workspaceID})
	if err != nil {
		return db.Issue{}, false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return db.Issue{}, false, err
	}
	return issue, true, nil
}

// followUpConversationScope is the conversation a follow-up belongs to: its
// openConversationId and type. The title is left out: a rename, or a native
// delivery whose title could not be read, does not split one conversation's
// follow-ups. A conversation that does not decode keeps its raw JSON.
func followUpConversationScope(raw json.RawMessage) string {
	var conversation struct {
		OpenConversationID string `json:"openConversationId"`
		Type               string `json:"type"`
	}
	if json.Unmarshal(raw, &conversation) != nil {
		return string(raw)
	}
	return strings.TrimSpace(conversation.OpenConversationID) + "\x00" + strings.TrimSpace(conversation.Type)
}
