package userdecision

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type ResolveFunc func(context.Context, Request) (plan, interpretation json.RawMessage, err error)
type Service struct {
	Store       *Store
	Pool        *pgxpool.Pool
	Transport   Transport
	Resolve     ResolveFunc
	Wake        func()
	NotifyAlert func(Alert)
}

// Run supervises one leased consumer per sender identity across all replicas.
// Waiting rows hold neither a Coordinator lease nor an executor slot.
func (s *Service) Run(ctx context.Context) {
	if s == nil || s.Pool == nil || s.Transport == nil {
		return
	}
	var mu sync.Mutex
	running := map[string]bool{}
	var wg sync.WaitGroup
	defer wg.Wait()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	// Allow subscription ownership to settle during rolling restarts.
	nextMonitor := time.Now().Add(2 * time.Minute)
	for {
		if time.Now().After(nextMonitor) {
			monitorCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			alerts, err := s.Store.Monitor(monitorCtx)
			cancel()
			if err != nil && ctx.Err() == nil {
				slog.Error("user decision monitor failed", "event", "user_decision_monitor_failed")
			}
			for _, alert := range alerts {
				slog.Warn("user decision needs attention", "event", "user_decision_alert", "decision_id", alert.DecisionID, "reason", alert.Reason)
				if s.NotifyAlert != nil {
					s.NotifyAlert(alert)
				}
			}
			nextMonitor = time.Now().Add(time.Minute)
		}
		if err := s.Store.Sweep(ctx); err != nil && ctx.Err() == nil {
			slog.Error("user decision sweep failed", "event", "user_decision_sweep_failed")
		}
		if err := s.refreshExecution(ctx); err != nil && ctx.Err() == nil {
			slog.Error("user decision execution tracking failed", "event", "user_decision_execution_tracking_failed")
		}
		rows, err := s.Pool.Query(ctx, `SELECT DISTINCT ON (sender_uid,sender_org_id) to_jsonb(d) FROM coordinator_user_decision d WHERE environment=$1 AND (state IN ('prepared','sending','send_unknown','waiting','accepted','resuming') OR card_update_pending) ORDER BY sender_uid,sender_org_id,created_at`, s.Store.Environment)
		if err == nil {
			for rows.Next() {
				var raw []byte
				if rows.Scan(&raw) != nil {
					continue
				}
				var r Request
				if json.Unmarshal(raw, &r) != nil {
					continue
				}
				key := r.SenderUID + ":" + r.SenderOrgID
				mu.Lock()
				exists := running[key]
				if !exists {
					running[key] = true
				}
				mu.Unlock()
				if exists {
					continue
				}
				wg.Add(1)
				go func(r Request, key string) {
					defer wg.Done()
					defer func() { mu.Lock(); delete(running, key); mu.Unlock() }()
					s.runIdentity(ctx, r)
				}(r, key)
			}
			rows.Close()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (s *Service) runIdentity(parent context.Context, r Request) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	owner := uuid.NewString()
	tag, err := s.Pool.Exec(ctx, `INSERT INTO coordinator_user_decision_consumer(environment,sender_uid,sender_org_id,owner,lease_expires_at) VALUES($1,$2,$3,$4,now()+interval '90 seconds') ON CONFLICT(environment,sender_uid,sender_org_id) DO UPDATE SET owner=EXCLUDED.owner,lease_expires_at=EXCLUDED.lease_expires_at,ready=false,updated_at=now() WHERE coordinator_user_decision_consumer.lease_expires_at<=now()`, s.Store.Environment, r.SenderUID, r.SenderOrgID, owner)
	if err != nil || tag.RowsAffected() != 1 {
		return
	}
	defer func() {
		cleanup, c := context.WithTimeout(context.Background(), 3*time.Second)
		defer c()
		_, _ = s.Pool.Exec(cleanup, `UPDATE coordinator_user_decision_consumer SET ready=false,lease_expires_at=now()+interval '15 seconds',updated_at=now() WHERE environment=$1 AND sender_uid=$2 AND sender_org_id=$3 AND owner=$4`, s.Store.Environment, r.SenderUID, r.SenderOrgID, owner)
	}()
	authCtx, authCancel := context.WithTimeout(ctx, 25*time.Second)
	session, err := s.Transport.Open(authCtx, r)
	authCancel()
	if err != nil {
		slog.Warn("user decision consumer identity unavailable", "event", "user_decision_consumer_auth_failed", "agent_id", r.AgentID)
		return
	}
	defer session.Close()
	ready := make(chan struct{})
	done := make(chan error, 1)
	var once sync.Once
	go func() {
		defer cancel()
		consumeErr := session.Consume(ctx, func() { once.Do(func() { close(ready) }) }, func(raw []byte) error {
			e, err := ParseAuditEvent(raw)
			if err != nil {
				slog.Warn("user decision event could not be decoded", "event", "user_decision_event_decode_failed", "agent_id", r.AgentID, "bytes", len(raw))
				return nil
			}
			outcome, err := s.Store.AcceptFrom(ctx, e, r.SenderUID, r.SenderOrgID)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			if err != nil {
				slog.Error("user decision callback persistence failed", "event", "user_decision_callback_persist_failed", "decision_id", e.RequestID)
				return err
			}
			slog.Info("user decision callback", "event", "user_decision_callback", "decision_id", e.RequestID, "outcome", outcome)
			return nil
		})
		slog.Info("user decision consumer stopped", "event", "user_decision_consumer_stopped", "agent_id", r.AgentID, "cancelled", ctx.Err() != nil, "failed", consumeErr != nil)
		done <- consumeErr
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(8 * time.Second):
		}
	}()
	select {
	case <-ready:
		slog.Info("user decision consumer ready", "event", "user_decision_consumer_ready", "agent_id", r.AgentID, "environment", s.Store.Environment)
	case <-done:
		return
	case <-ctx.Done():
		return
	case <-time.After(30 * time.Second):
		slog.Warn("user decision subscription not ready", "event", "user_decision_consumer_not_ready", "agent_id", r.AgentID)
		return
	}
	// Renew independently of network/model work; losing ownership cancels it.
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		tick := time.NewTicker(20 * time.Second)
		defer tick.Stop()
		for {
			renewCtx, renewCancel := context.WithTimeout(ctx, 5*time.Second)
			tag, err := s.Pool.Exec(renewCtx, `UPDATE coordinator_user_decision_consumer SET lease_expires_at=now()+interval '90 seconds',ready=true,updated_at=now() WHERE environment=$1 AND sender_uid=$2 AND sender_org_id=$3 AND owner=$4 AND lease_expires_at>now()`, s.Store.Environment, r.SenderUID, r.SenderOrgID, owner)
			renewCancel()
			if err != nil || tag.RowsAffected() != 1 {
				cancel()
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
		}
	}()
	defer func() { cancel(); <-heartbeatDone }()
	// Restore waiting forms from their frozen snapshots after reconnect/rollout.
	// Component-only updates preserve typed answers and do not create another card.
	if _, err = s.Pool.Exec(ctx, `UPDATE coordinator_user_decision SET card_update_pending=true,available_at=now(),updated_at=now() WHERE environment=$1 AND sender_uid=$2 AND sender_org_id=$3 AND state='waiting'`, s.Store.Environment, r.SenderUID, r.SenderOrgID); err != nil {
		slog.Warn("user decision waiting card recovery failed", "event", "user_decision_card_recovery_failed", "agent_id", r.AgentID)
		return
	}

	// Re-authenticate periodically so a long-lived subscription never retains
	// expired per-session credentials indefinitely.
	refresh := time.NewTimer(30 * time.Minute)
	defer refresh.Stop()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if err = s.processIdentity(ctx, r, session); err != nil && ctx.Err() == nil {
			slog.Warn("user decision work will retry", "event", "user_decision_worker_retry", "agent_id", r.AgentID)
		}
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-refresh.C:
			return
		case <-ticker.C:
		}
	}
}
func (s *Service) processIdentity(ctx context.Context, identity Request, session Session) error {
	send, err := s.Store.ClaimSend(ctx, identity.SenderUID, identity.SenderOrgID)
	if err == nil {
		sendCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
		cardID, sendErr := session.Send(sendCtx, send)
		cancel()
		if sendErr != nil {
			_ = s.Store.MarkSendUnknown(ctx, send.ID)
			fields := []any{"event", "user_decision_send_unknown", "decision_id", send.ID}
			// Only typed, allowlisted CLI diagnostics may cross the log boundary.
			var diagnostic interface{ DiagnosticFields() map[string]any }
			if errors.As(sendErr, &diagnostic) {
				fields = append(fields, "diagnostics", diagnostic.DiagnosticFields())
			}
			slog.Warn("user decision send needs reconciliation", fields...)
		} else if err = s.Store.ConfirmSent(ctx, send.ID, cardID); err != nil {
			slog.Warn("user decision receipt persistence failed", "event", "user_decision_receipt_pending", "decision_id", send.ID, "provider_card_id", cardID)
			return err
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	// Updating the original business ID can confirm an uncertain send without
	// creating a second card. Failed probes remain durable and retry later.
	if probe, ok := session.(interface {
		Reconcile(context.Context, Request) error
	}); ok {
		var raw []byte
		probeErr := s.Pool.QueryRow(ctx, `UPDATE coordinator_user_decision SET available_at=now()+interval '30 seconds' WHERE id=(SELECT id FROM coordinator_user_decision WHERE environment=$1 AND sender_uid=$2 AND sender_org_id=$3 AND state='send_unknown' AND available_at<=now() ORDER BY created_at LIMIT 1) RETURNING to_jsonb(coordinator_user_decision)`, s.Store.Environment, identity.SenderUID, identity.SenderOrgID).Scan(&raw)
		if probeErr == nil {
			var unknown Request
			if json.Unmarshal(raw, &unknown) == nil {
				probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
				probeErr = probe.Reconcile(probeCtx, unknown)
				cancel()
				if probeErr == nil {
					if err := s.Store.ConfirmSent(ctx, unknown.ID, unknown.CardID); err != nil {
						return err
					}
				}
			}
		} else if !errors.Is(probeErr, pgx.ErrNoRows) {
			return probeErr
		}
	}
	r, err := s.claimResolution(ctx, identity)
	if err == nil {
		status, ack := Status(r)
		ackCtx, ackCancel := context.WithTimeout(ctx, 10*time.Second)
		_ = session.Update(ackCtx, r, status, ack)
		ackCancel()
		resolveCtx, cancel := context.WithTimeout(ctx, 55*time.Second)
		plan, interpretation, resolveErr := s.Resolve(resolveCtx, r)
		cancel()
		reason := ""
		if resolveErr != nil {
			reason = "无法依据本次选择和补充说明形成明确、合法的处理计划，本次未执行。"
		}
		if err = s.finishResolution(ctx, r, plan, interpretation, reason); err != nil {
			return err
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	var raw []byte
	err = s.Pool.QueryRow(ctx, `SELECT to_jsonb(d) FROM coordinator_user_decision d WHERE environment=$1 AND sender_uid=$2 AND sender_org_id=$3 AND card_update_pending AND sent_at IS NOT NULL AND available_at<=now() ORDER BY updated_at LIMIT 1`, s.Store.Environment, identity.SenderUID, identity.SenderOrgID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = json.Unmarshal(raw, &r); err != nil {
		return err
	}
	status, text := Status(r)
	updateCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	err = session.Update(updateCtx, r, status, text)
	cancel()
	if err != nil {
		_, _ = s.Pool.Exec(ctx, `UPDATE coordinator_user_decision SET available_at=now()+interval '15 seconds' WHERE id=$1 AND updated_at=$2`, r.ID, r.UpdatedAt)
		return err
	}
	_, err = s.Pool.Exec(ctx, `UPDATE coordinator_user_decision SET card_update_pending=false WHERE id=$1 AND updated_at=$2`, r.ID, r.UpdatedAt)
	return err
}
func Status(r Request) (string, string) {
	text := "已收到你的补充说明。"
	if r.Submission != nil {
		for _, o := range r.Proposal.Options {
			if o.ID == r.Submission.OptionID {
				text = "已收到你的选择：" + o.Label
				break
			}
		}
		if r.Submission.Custom != "" {
			text += "\n补充说明：" + r.Submission.Custom
		}
	}
	var execution struct {
		State string `json:"state"`
	}
	_ = json.Unmarshal(r.ExecutionResult, &execution)
	if execution.State == "completed" {
		return "FINISH", text + "\n本次处理已结束，请查看回复消息。"
	}
	if execution.State == "failed" {
		return "ERROR", text + "\n本次处理未成功完成，请查看失败说明。"
	}

	switch r.State {
	case "waiting", "prepared", "sending", "send_unknown":
		return "CONFIRMING", r.Proposal.Question
	case "expired":
		return "TIMEOUT", "选择已过期，本次未执行。"
	case "cancelled":
		return "ABORTED", "员工已停用或删除，本次未执行。"
	case "not_executed", "failed":
		return "ERROR", r.LastError
	}

	if r.State == "dispatched" {
		return "EXECUTING", text + "\n已进入处理流程，执行结果将另行告知。"
	}
	return "CONFIRMED", text + "\n正在校验，尚未执行。"
}
func (s *Service) claimResolution(ctx context.Context, identity Request) (Request, error) {
	var raw []byte
	err := s.Pool.QueryRow(ctx, `WITH candidate AS (SELECT id FROM coordinator_user_decision WHERE environment=$1 AND sender_uid=$2 AND sender_org_id=$3 AND (state='accepted' OR (state='resuming' AND lease_expires_at<=now())) ORDER BY accepted_at FOR UPDATE SKIP LOCKED LIMIT 1)
 UPDATE coordinator_user_decision d SET state='resuming',lease_token=gen_random_uuid(),lease_expires_at=now()+interval '75 seconds',updated_at=now() FROM candidate WHERE d.id=candidate.id RETURNING to_jsonb(d)`, s.Store.Environment, identity.SenderUID, identity.SenderOrgID).Scan(&raw)
	if err != nil {
		return Request{}, err
	}
	var r Request
	err = json.Unmarshal(raw, &r)
	return r, err
}
func (s *Service) finishResolution(ctx context.Context, r Request, plan, interpretation json.RawMessage, reason string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var state string
	err = tx.QueryRow(ctx, `SELECT state FROM coordinator_user_decision WHERE id=$1 AND lease_token=$2 FOR UPDATE`, r.ID, r.LeaseToken).Scan(&state)
	if err != nil {
		return err
	}
	if state != "resuming" {
		return errors.New("decision resolution no longer owns request")
	}
	var active bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent WHERE id=$1 AND workspace_id=$2 AND archived_at IS NULL AND status NOT IN ('disabled','paused'))`, r.AgentID, r.WorkspaceID).Scan(&active); err != nil {
		return err
	}
	if !active {
		reason = "员工已停用或删除，本次未执行。"
	}
	if reason != "" {
		plan, _ = json.Marshal(map[string]any{"Action": "reply", "UserText": reason, "PlanVersion": "window-plan-v1", "Reason": "user_decision_not_executed"})
		state = "not_executed"
	} else if !json.Valid(plan) {
		return errors.New("invalid resolved plan")
	} else {
		state = "dispatched"
	}
	if len(interpretation) == 0 {
		interpretation = json.RawMessage(`{}`)
	}
	_, err = tx.Exec(ctx, `UPDATE coordinator_user_decision SET state=$2,final_plan=$3,interpretation=$4,last_error=NULLIF($5,''),card_update_pending=true,lease_token=NULL,lease_expires_at=NULL,available_at=now(),updated_at=now() WHERE id=$1`, r.ID, state, plan, interpretation, reason)
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE inbound_coordinator_job SET command=jsonb_set(command,'{_coordinator_plan}',$2::jsonb),status='pending',available_at=now(),lease_token=NULL,lease_expires_at=NULL,last_error=NULL,updated_at=now() WHERE id=$1 AND status='pending' AND last_error='awaiting_user_decision'`, r.JobID, plan)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("waiting coordinator job could not resume")
	}
	if err = tx.Commit(ctx); err == nil && s.Wake != nil {
		s.Wake()
	}
	return err
}
