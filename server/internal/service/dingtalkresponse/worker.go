package dingtalkresponse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/dwsclient"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	leaseDuration   = 2 * time.Minute
	providerTimeout = 75 * time.Second
	deliveryTimeout = 15 * time.Minute
	reconcileWindow = 24 * time.Hour
)

type action struct {
	ID                     string
	Input                  ActionInput
	State                  string
	ProviderTaskID         string
	ProviderConversationID string
	ProviderMessageID      string
	ErrorCode              string
	Attempts               int
	ReceiptState           string
	LeaseToken             string
	FirstAttemptAt         pgtype.Timestamptz
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

func (s *Service) claim(ctx context.Context) (*action, error) {
	token := uuid.NewString()
	row := s.pool.QueryRow(ctx, `WITH due AS (
		SELECT id FROM response_action
		WHERE next_attempt_at <= now() AND (lease_until IS NULL OR lease_until < now())
		ORDER BY next_attempt_at, created_at FOR UPDATE SKIP LOCKED LIMIT 1
	) UPDATE response_action a SET lease_token=$1::uuid,
		lease_until=now()+make_interval(secs => $2::double precision), attempts=attempts+1
	FROM due WHERE a.id=due.id
	RETURNING a.id,a.input,a.state,a.provider_task_id,a.provider_conversation_id,
		a.provider_message_id,a.error_code,a.attempts,a.receipt_state,a.first_attempt_at,a.created_at,a.updated_at`, token, leaseDuration.Seconds())
	var a action
	var raw []byte
	err := row.Scan(&a.ID, &raw, &a.State, &a.ProviderTaskID, &a.ProviderConversationID,
		&a.ProviderMessageID, &a.ErrorCode, &a.Attempts, &a.ReceiptState, &a.FirstAttemptAt, &a.CreatedAt, &a.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	a.LeaseToken = token
	if err := json.Unmarshal(raw, &a.Input); err != nil {
		return nil, fmt.Errorf("decode persisted response action: %w", err)
	}
	return &a, nil
}

func (s *Service) processOne(ctx context.Context) (bool, error) {
	a, err := s.claim(ctx)
	if err != nil || a == nil {
		return false, err
	}
	progress := a.Input.CoordinatorWaitJobID != "" && a.Input.CallbackURL == ""
	if progress && a.State == "pending" {
		var waiting bool
		err = s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM inbound_coordinator_job WHERE id=$1 AND workspace_id=$2 AND agent_id=$3 AND status IN ('pending','running') AND command #>> '{_coordinator_wait,response_action_id}'=$4 AND command #>> '{_coordinator_plan,PlanVersion}'='window-plan-v1' AND jsonb_array_length(command #> '{_coordinator_plan,Items}') > COALESCE(jsonb_array_length(NULLIF(command #> '{_coordinator_plan,CompletedActionKeys}', 'null'::jsonb)),0))`, a.Input.CoordinatorWaitJobID, a.Input.WorkspaceID, a.Input.AgentID, a.ID).Scan(&waiting)
		if err != nil {
			return true, s.release(ctx, a, time.Now().Add(retryDelay(a.Attempts)))
		}
		if !waiting {
			if err = s.saveState(ctx, a, "cancelled", "", "", "", "coordinator_wait_resolved"); err != nil {
				return true, err
			}
		}
	}
	if a.State == "pending" {
		if a.Input.Text == "" {
			err = s.saveState(ctx, a, a.Input.CloseState, "", "", "", "")
		} else {
			err = s.send(ctx, a)
		}
	} else if (a.State == "provider_accepted" || a.State == "unknown") && a.ProviderTaskID != "" {
		err = s.query(ctx, a)
	}
	if err != nil {
		return true, err
	}

	if !progress && isReceiptState(a.State) && a.ReceiptState != a.State {
		if s.receipts == nil {
			return true, s.release(ctx, a, time.Now().Add(time.Minute))
		}
		receipt := protocol.DingTalkResponseReceipt{
			RequestID: a.Input.RequestID, AgentID: a.Input.AgentID, ActionID: a.ID,
			State: a.State, OccurredAt: a.UpdatedAt.UnixMilli(), OpenTaskID: a.ProviderTaskID,
			OpenConversationID: a.ProviderConversationID, OpenMessageID: a.ProviderMessageID, ErrorCode: a.ErrorCode,
		}
		callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		err := s.receipts.SendResponseReceipt(callCtx, a.Input.CallbackURL, a.Input.CallbackTarget, receipt)
		cancel()
		if err != nil {
			return true, s.release(ctx, a, time.Now().Add(retryDelay(a.Attempts)))
		}
		// Persist callback acknowledgement before releasing the lease. A crash
		// here only repeats the same action receipt, which Router deduplicates.
		persistCtx, persistCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		tag, err := s.pool.Exec(persistCtx, `UPDATE response_action SET receipt_state=$3 WHERE id=$1 AND lease_token=$2::uuid`, a.ID, a.LeaseToken, a.State)
		persistCancel()
		if err != nil {
			return true, err
		}
		if tag.RowsAffected() != 1 {
			return true, errors.New("response action receipt lease lost")
		}
		a.ReceiptState = a.State
		slog.Info("response receipt acknowledged", "event", "response_receipt_acknowledged",
			"action_id", a.ID, "request_id", a.Input.RequestID, "workspace_id", a.Input.WorkspaceID,
			"agent_id", a.Input.AgentID, "delivery_state", a.State,
			"state_to_receipt_ack_ms", time.Since(a.UpdatedAt).Milliseconds())
	}
	return true, s.release(ctx, a, nextAttempt(a, time.Now()))
}

func (s *Service) send(ctx context.Context, a *action) error {
	if s.provider == nil {
		return s.saveState(ctx, a, "failed", "", "", "", "provider_not_configured")
	}
	// This commit must precede the external write. If we disappear after send,
	// the next owner sees unknown and cannot automatically submit again.
	if err := s.saveState(ctx, a, "unknown", "", "", "", "submission_interrupted"); err != nil {
		return err
	}
	callCtx, cancel := context.WithTimeout(ctx, providerTimeout)
	result, err := s.provider.Send(callCtx, a.Input, "multica-response:"+a.ID)
	cancel()
	state, code := "provider_accepted", ""
	if err != nil {
		var notSubmitted *NotSubmittedError
		var rejected *dwsclient.SendRejectedError
		switch {
		case errors.As(err, &notSubmitted):
			state, code = "pending", "identity_unavailable"
			if a.Attempts >= 5 {
				state = "failed"
			}
		case errors.As(err, &rejected):
			state, code = "failed", dwsclient.SafeCode(rejected.Code)
		default:
			state, code = "unknown", "send_result_unknown"
			// Nothing else records why: keep the provider's (allowlisted)
			// error for diagnosis.
			msg := err.Error()
			if len(msg) > 300 {
				msg = msg[:300]
			}
			slog.Warn("response send result unknown", "event", "response_send_result_unknown", "action_id", a.ID,
				"agent_id", a.Input.AgentID, "dws_environment", a.Input.DWSEnvironment, "error", msg)
		}
	} else if result.OpenTaskID == "" {
		state, code = "unknown", "send_task_id_missing"
	}
	// Save the provider task even if shutdown cancelled the initiating request.
	persistCtx, persistCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer persistCancel()
	return s.saveState(persistCtx, a, state, result.OpenTaskID, "", "", code)
}

func (s *Service) query(ctx context.Context, a *action) error {
	if s.provider == nil {
		return s.saveState(ctx, a, "unknown", a.ProviderTaskID, "", "", "provider_not_configured")
	}
	callCtx, cancel := context.WithTimeout(ctx, providerTimeout)
	status, err := s.provider.Query(callCtx, a.Input, a.ProviderTaskID)
	cancel()
	state, code := a.State, a.ErrorCode
	conversationID, messageID := a.ProviderConversationID, a.ProviderMessageID
	if err == nil {
		switch status.State {
		case "delivered":
			if status.OpenMessageID != "" && status.OpenConversationID != "" {
				if status.OpenConversationID != a.Input.ConversationID {
					state, code = "unknown", "delivery_target_mismatch"
				} else {
					state, code = "delivered", ""
					conversationID, messageID = status.OpenConversationID, status.OpenMessageID
				}
			}
		case "failed":
			state, code = "failed", dwsclient.SafeCode(status.ErrorCode)
		}
	}
	if state == "provider_accepted" && time.Since(a.FirstAttemptAt.Time) >= deliveryTimeout {
		state, code = "unknown", "delivery_confirmation_timeout"
	}
	if state == a.State && code == a.ErrorCode && messageID == a.ProviderMessageID {
		return nil
	}
	persistCtx, persistCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer persistCancel()
	return s.saveState(persistCtx, a, state, a.ProviderTaskID, conversationID, messageID, code)
}

func (s *Service) saveState(ctx context.Context, a *action, state, taskID, conversationID, messageID, code string) error {
	now := time.Now().UTC()
	previousState := a.State
	var first pgtype.Timestamptz
	err := s.pool.QueryRow(ctx, `UPDATE response_action SET state=$3,provider_task_id=$4,
		provider_conversation_id=$5,provider_message_id=$6,error_code=$7,updated_at=$8,
		first_attempt_at=COALESCE(first_attempt_at,$8)
		WHERE id=$1 AND lease_token=$2::uuid RETURNING first_attempt_at`,
		a.ID, a.LeaseToken, state, taskID, conversationID, messageID, code, now).Scan(&first)
	if err != nil {
		return fmt.Errorf("persist response delivery state: %w", err)
	}
	a.State, a.ProviderTaskID, a.ProviderConversationID, a.ProviderMessageID = state, taskID, conversationID, messageID
	a.ErrorCode, a.UpdatedAt, a.FirstAttemptAt = code, now, first
	slog.Info("response action state changed", "event", "response_action_state_changed",
		"action_id", a.ID, "request_id", a.Input.RequestID, "workspace_id", a.Input.WorkspaceID,
		"agent_id", a.Input.AgentID, "previous_state", previousState, "delivery_state", state,
		"error_code", code, "action_age_ms", now.Sub(a.CreatedAt).Milliseconds(), "attempts", a.Attempts)
	return nil
}

func (s *Service) release(ctx context.Context, a *action, due time.Time) error {
	var next *time.Time
	if !due.IsZero() {
		next = &due
	}
	tag, err := s.pool.Exec(ctx, `UPDATE response_action SET lease_token=NULL,lease_until=NULL,next_attempt_at=$3
		WHERE id=$1 AND lease_token=$2::uuid`, a.ID, a.LeaseToken, next)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("response action lease lost")
	}
	return nil
}

func isReceiptState(state string) bool {
	switch state {
	case "delivered", "failed", "silent", "cancelled", "unknown":
		return true
	}
	return false
}

func retryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 6 {
		attempt = 6
	}
	return time.Duration(1<<(attempt-1)) * time.Second
}

func nextAttempt(a *action, now time.Time) time.Time {
	switch a.State {
	case "pending":
		return now.Add(retryDelay(a.Attempts))
	case "provider_accepted":
		return now.Add(retryDelay(a.Attempts))
	case "unknown":
		if a.ProviderTaskID != "" && a.FirstAttemptAt.Valid && now.Sub(a.FirstAttemptAt.Time) < reconcileWindow {
			return now.Add(5 * time.Minute)
		}
	}
	return time.Time{}
}
