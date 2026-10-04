package dingtalkresponse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/dwsclient"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// RecordSandboxReceipt persists observations from an authenticated task. Only
// server-side Query can promote an observation to delivered. It never sends.
func (s *Service) RecordSandboxReceipt(ctx context.Context, in ActionInput, receipt protocol.DingTalkSendReceipt) error {
	if s == nil || s.pool == nil {
		return errors.New("response service is not configured")
	}
	if err := validateSandboxInput(in, receipt); err != nil {
		return err
	}
	// Keep credentials out, and freeze only the identity and source needed to
	// verify this task's send. Incoming callback content is not trusted routing.
	in = ActionInput{WorkspaceID: in.WorkspaceID, AgentID: in.AgentID, TaskID: in.TaskID, IssueID: in.IssueID,
		DWSUID: in.DWSUID, DWSOrgID: in.DWSOrgID, ConversationID: in.ConversationID,
		SenderOpenDingTalkID: in.SenderOpenDingTalkID, IsGroup: in.IsGroup, DWSEnvironment: in.DWSEnvironment}
	raw, err := json.Marshal(in)
	if err != nil {
		return err
	}
	id := sandboxReceiptID(in, receipt.ClientActionID)
	targetCID := receipt.OpenConversationID
	if targetCID == "" && !in.IsGroup && receipt.RecipientOpenDingTalkID != "" && receipt.RecipientOpenDingTalkID == in.SenderOpenDingTalkID {
		targetCID = in.ConversationID
	}
	state := "pending"
	if receipt.OpenTaskID != "" {
		state = "provider_accepted"
	} else if receipt.State != "pending" {
		state = "unknown"
	}
	var due *time.Time
	if state == "pending" || receipt.OpenTaskID != "" {
		now := time.Now()
		due = &now
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Insert before locking makes duplicate initial observations serialize on
	// the unique ID without requiring a process-local task lock.
	_, err = tx.Exec(ctx, `INSERT INTO sandbox_send_receipt
		(id,workspace_id,agent_id,task_id,issue_id,client_action_id,input,payload_hash,idempotency_key,
		target_conversation_id,recipient_open_dingtalk_id,observed_state,state,provider_task_id,error_code,next_attempt_at)
		VALUES ($1,$2,$3,$4,NULLIF($5,'')::uuid,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
		ON CONFLICT(id) DO NOTHING`, id, in.WorkspaceID, in.AgentID, in.TaskID, in.IssueID, receipt.ClientActionID, raw,
		receipt.PayloadHash, receipt.IdempotencyKey, targetCID, receipt.RecipientOpenDingTalkID, receipt.State, state,
		receipt.OpenTaskID, safeOptionalCode(receipt.ErrorCode), due)
	if err != nil {
		return fmt.Errorf("persist sandbox receipt: %w", err)
	}
	var existing struct {
		input                                                        []byte
		payloadHash, key, taskID, target, recipient, state, observed string
	}
	err = tx.QueryRow(ctx, `SELECT input,payload_hash,idempotency_key,provider_task_id,target_conversation_id,recipient_open_dingtalk_id,state,observed_state
		FROM sandbox_send_receipt WHERE id=$1 FOR UPDATE`, id).Scan(&existing.input, &existing.payloadHash, &existing.key, &existing.taskID, &existing.target, &existing.recipient, &existing.state, &existing.observed)
	if err != nil {
		return err
	}
	var frozen ActionInput
	if err := json.Unmarshal(existing.input, &frozen); err != nil {
		return err
	}
	if frozen != in || existing.payloadHash != receipt.PayloadHash || (existing.key != "" && receipt.IdempotencyKey != "" && existing.key != receipt.IdempotencyKey) ||
		(existing.taskID != "" && receipt.OpenTaskID != "" && existing.taskID != receipt.OpenTaskID) ||
		(existing.target != "" && targetCID != "" && existing.target != targetCID) ||
		(existing.recipient != "" && receipt.RecipientOpenDingTalkID != "" && existing.recipient != receipt.RecipientOpenDingTalkID) {
		return errors.New("sandbox receipt conflicts with frozen send intent")
	}
	// Late pending observations cannot erase a provider task or authoritative
	// verdict. A client-reported message ID is deliberately not persisted as a
	// verified ID; it must come back from the server's authenticated query.
	if existing.state != "delivered" && existing.state != "failed" && receipt.State != "pending" {
		providerTask := existing.taskID
		if providerTask == "" {
			providerTask = receipt.OpenTaskID
		}
		if providerTask != "" {
			state = "provider_accepted"
			now := time.Now()
			due = &now
		} else {
			state = "unknown"
			due = nil
		}
		_, err = tx.Exec(ctx, `UPDATE sandbox_send_receipt SET observed_state=$2,state=$3,provider_task_id=$4,observation_revision=observation_revision+1,
			target_conversation_id=CASE WHEN target_conversation_id='' THEN $5 ELSE target_conversation_id END,
			recipient_open_dingtalk_id=CASE WHEN recipient_open_dingtalk_id='' THEN $6 ELSE recipient_open_dingtalk_id END,
			idempotency_key=CASE WHEN idempotency_key='' THEN $7 ELSE idempotency_key END,
			error_code=$8,next_attempt_at=$9,updated_at=now()
			WHERE id=$1`, id, receipt.State, state, providerTask, targetCID, receipt.RecipientOpenDingTalkID, receipt.IdempotencyKey, safeOptionalCode(receipt.ErrorCode), due)
		if err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	s.notifySandbox()
	return nil
}

func validateSandboxInput(in ActionInput, receipt protocol.DingTalkSendReceipt) error {
	for _, id := range []string{in.WorkspaceID, in.AgentID, in.TaskID} {
		if _, err := uuid.Parse(id); err != nil {
			return errors.New("sandbox receipt scope UUID is invalid")
		}
	}
	if in.IssueID != "" {
		if _, err := uuid.Parse(in.IssueID); err != nil {
			return errors.New("sandbox receipt issue UUID is invalid")
		}
	}
	if in.DWSUID == "" || in.DWSOrgID == "" {
		return errors.New("sandbox receipt identity is incomplete")
	}
	if receipt.ClientActionID == "" || len(receipt.ClientActionID) > 256 || receipt.PayloadHash == "" || len(receipt.PayloadHash) > 256 {
		return errors.New("sandbox receipt intent key is invalid")
	}
	switch receipt.State {
	case "pending", "unknown", "failed":
	case "accepted", "provider_accepted", "delivered":
		if strings.TrimSpace(receipt.OpenTaskID) == "" {
			return errors.New("sandbox accepted receipt requires provider task id")
		}
	default:
		return errors.New("sandbox receipt state is invalid")
	}
	return nil
}

func sandboxReceiptID(in ActionInput, clientActionID string) string {
	return StableActionID(in.WorkspaceID, in.AgentID, in.TaskID+"\x00"+clientActionID, "sandbox.send")
}
func safeOptionalCode(code string) string {
	if code == "" {
		return ""
	}
	return dwsclient.SafeCode(code)
}

// SandboxResponseState examines only the specified task, issue, and originating
// conversation. A proactive send to another chat cannot suppress this reply.
func (s *Service) SandboxResponseState(ctx context.Context, workspaceID, agentID, issueID, taskID, cid string) (string, error) {
	if s == nil || s.pool == nil {
		return "", errors.New("response service is not configured")
	}
	if taskID == "" || cid == "" {
		return "", nil
	}
	var state string
	err := s.pool.QueryRow(ctx, `SELECT state FROM sandbox_send_receipt
		WHERE workspace_id=$1 AND agent_id=$2 AND task_id=$3
		AND ($4::text='' OR issue_id=NULLIF($4,'')::uuid)
		AND (COALESCE(NULLIF(provider_conversation_id,''),target_conversation_id)=$5
		 OR (provider_conversation_id='' AND target_conversation_id='' AND recipient_open_dingtalk_id=''
		     AND state IN ('pending','provider_accepted','unknown')))
		ORDER BY CASE state WHEN 'delivered' THEN 0 WHEN 'unknown' THEN 1 WHEN 'provider_accepted' THEN 2 WHEN 'pending' THEN 3 ELSE 4 END,created_at DESC LIMIT 1`,
		workspaceID, agentID, taskID, issueID, cid).Scan(&state)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return state, err
}

type sandboxObservation struct {
	id                                           string
	in                                           ActionInput
	state, providerTaskID, targetCID, leaseToken string
	providerCID, providerMessageID               string
	hookComplete                                 bool
	attempts                                     int
	revision                                     int64
	createdAt                                    time.Time
}

func (s *Service) processSandboxOne(ctx context.Context) (bool, error) {
	token := uuid.NewString()
	var a sandboxObservation
	var raw []byte
	err := s.pool.QueryRow(ctx, `WITH due AS (
		SELECT id FROM sandbox_send_receipt WHERE next_attempt_at<=now() AND (lease_until IS NULL OR lease_until<now())
		ORDER BY next_attempt_at,created_at FOR UPDATE SKIP LOCKED LIMIT 1
	) UPDATE sandbox_send_receipt a SET lease_token=$1::uuid,lease_until=now()+interval '2 minutes',attempts=attempts+1
	FROM due WHERE a.id=due.id RETURNING a.id,a.input,a.state,a.provider_task_id,a.target_conversation_id,a.attempts,a.created_at,a.observation_revision,
		a.provider_conversation_id,a.provider_message_id,a.delivery_hook_completed_at IS NOT NULL`, token).
		Scan(&a.id, &raw, &a.state, &a.providerTaskID, &a.targetCID, &a.attempts, &a.createdAt, &a.revision, &a.providerCID, &a.providerMessageID, &a.hookComplete)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(raw, &a.in); err != nil {
		return true, err
	}
	a.leaseToken = token
	if a.state == "delivered" {
		return true, s.finishSandboxDelivery(ctx, a)
	}
	now := time.Now()
	state, code := a.state, ""
	conversationID, messageID := "", ""
	var due *time.Time
	if a.providerTaskID == "" {
		if now.Sub(a.createdAt) >= deliveryTimeout {
			state, code = "unknown", "sandbox_send_unconfirmed"
		} else {
			next := now.Add(15 * time.Second)
			due = &next
		}
	} else {
		if s.provider != nil {
			callCtx, cancel := context.WithTimeout(ctx, providerTimeout)
			status, queryErr := s.provider.Query(callCtx, a.in, a.providerTaskID)
			cancel()
			if queryErr == nil {
				switch status.State {
				case "delivered":
					if status.OpenConversationID != "" && status.OpenMessageID != "" {
						if a.targetCID != "" && a.targetCID != status.OpenConversationID {
							state, code = "unknown", "delivery_target_mismatch"
						} else {
							state = "delivered"
							conversationID, messageID = status.OpenConversationID, status.OpenMessageID
						}
					}
				case "failed":
					state, code = "failed", dwsclient.SafeCode(status.ErrorCode)
				}
			}
		}
		if state != "delivered" && state != "failed" {
			if now.Sub(a.createdAt) >= deliveryTimeout {
				state = "unknown"
				if code == "" {
					code = "delivery_confirmation_timeout"
				}
			}
			if now.Sub(a.createdAt) < reconcileWindow {
				next := now.Add(retryDelay(a.attempts))
				if state == "unknown" {
					next = now.Add(5 * time.Minute)
				}
				due = &next
			}
		}
	}
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if state == "delivered" {
		next := time.Now()
		due = &next
	}
	tag, err := s.pool.Exec(persistCtx, `UPDATE sandbox_send_receipt SET state=$3,error_code=$4,
		provider_conversation_id=$5,provider_message_id=$6,next_attempt_at=$7,
		lease_token=CASE WHEN $3='delivered' THEN lease_token ELSE NULL END,
		lease_until=CASE WHEN $3='delivered' THEN lease_until ELSE NULL END,
		updated_at=now() WHERE id=$1 AND lease_token=$2::uuid AND provider_task_id=$8 AND observation_revision=$9`,
		a.id, a.leaseToken, state, code, conversationID, messageID, due, a.providerTaskID, a.revision)
	if err != nil {
		return true, err
	}
	if tag.RowsAffected() != 1 {
		// A task may have reported acceptance while we were inspecting its
		// pending intent. Leave that newer state intact and relinquish only our lease.
		_, err = s.pool.Exec(persistCtx, `UPDATE sandbox_send_receipt SET lease_token=NULL,lease_until=NULL WHERE id=$1 AND lease_token=$2::uuid`, a.id, a.leaseToken)
		return true, err
	}
	if state == "delivered" {
		a.state, a.providerCID, a.providerMessageID = state, conversationID, messageID
		return true, s.finishSandboxDelivery(ctx, a)
	}
	return true, err
}

func (s *Service) finishSandboxDelivery(ctx context.Context, a sandboxObservation) error {
	var hookErr error
	if !a.hookComplete && s.OnSandboxDelivered != nil {
		callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		hookErr = s.OnSandboxDelivered(callCtx, a.in, a.providerCID, a.providerMessageID)
		cancel()
	}
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	var due *time.Time
	code := ""
	if hookErr != nil {
		next := time.Now().Add(retryDelay(a.attempts))
		due = &next
		code = "delivery_hook_failed"
	}
	tag, err := s.pool.Exec(persistCtx, `UPDATE sandbox_send_receipt SET
		delivery_hook_completed_at=CASE WHEN $3::boolean THEN COALESCE(delivery_hook_completed_at,now()) ELSE delivery_hook_completed_at END,
		error_code=$4,next_attempt_at=$5,lease_token=NULL,lease_until=NULL
		WHERE id=$1 AND lease_token=$2::uuid AND state='delivered'`, a.id, a.leaseToken, hookErr == nil, code, due)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("sandbox delivery hook lease lost")
	}
	return nil
}
