package userdecision

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/storage"
)

// Database is deliberately transactional: event deduplication, acceptance and
// durable work become visible together, irrespective of the receiving replica.
type Database interface {
	Begin(context.Context) (pgx.Tx, error)
}
type Store struct {
	DB          Database
	Blobs       storage.Storage
	Environment string
}

type Request struct {
	LeaseToken        string          `json:"lease_token"`
	JobLease          string          `json:"-"`
	UpdatedAt         time.Time       `json:"updated_at"`
	ExecutionResult   json.RawMessage `json:"execution_result"`
	FinalPlan         json.RawMessage `json:"final_plan"`
	LastError         string          `json:"last_error"`
	CardUpdatePending bool            `json:"card_update_pending"`
	ID                string          `json:"id"`
	WorkspaceID       string          `json:"workspace_id"`
	AgentID           string          `json:"agent_id"`
	JobID             string          `json:"job_id"`
	Environment       string          `json:"environment"`
	CorpID            string          `json:"corp_id"`
	ConversationID    string          `json:"conversation_id"`
	InitiatorID       string          `json:"initiator_id"`
	SenderUID         string          `json:"sender_uid"`
	SenderOrgID       string          `json:"sender_org_id"`
	State             string          `json:"state"`
	Version           string          `json:"version"`
	Snapshot          json.RawMessage `json:"snapshot"`
	Proposal          Proposal        `json:"proposal"`
	Submission        *Submission     `json:"submission,omitempty"`
	CardID            string          `json:"card_biz_id"`
	SendRequestID     string          `json:"send_request_id"`
	SentAt            *time.Time      `json:"sent_at,omitempty"`
	ExpiresAt         *time.Time      `json:"expires_at,omitempty"`
}
type Submission struct {
	EventID    string    `json:"event_id"`
	OperatorID string    `json:"operator_id"`
	OptionID   string    `json:"option_id,omitempty"`
	Custom     string    `json:"custom"`
	ReceivedAt time.Time `json:"received_at"`
}

func (s *Store) Prepare(ctx context.Context, r Request) (string, error) {
	if s.DB == nil || s.Environment == "" || r.Environment != s.Environment {
		return "", errors.New("decision environment unavailable")
	}
	if err := r.Proposal.Validate(); err != nil {
		return "", err
	}
	if r.InitiatorID == "" || r.CorpID == "" || r.ConversationID == "" || r.SenderUID == "" || r.SenderOrgID == "" || !json.Valid(r.Snapshot) {
		return "", errors.New("incomplete decision identity or snapshot")
	}
	for _, id := range []string{r.WorkspaceID, r.AgentID, r.JobID} {
		if _, err := uuid.Parse(id); err != nil {
			return "", errors.New("invalid decision identifier")
		}
	}
	if r.ID == "" {
		r.ID = uuid.NewString()
	}
	if _, err := uuid.Parse(r.ID); err != nil {
		return "", errors.New("invalid decision request identifier")
	}
	r.CardID = "coordinator_" + r.ID
	snapshot, err := freezeSnapshot(ctx, s.Blobs, r)
	if err != nil {
		return "", err
	}
	r.Snapshot = snapshot
	p, err := json.Marshal(r.Proposal)
	if err != nil {
		return "", err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	var id string
	err = tx.QueryRow(ctx, `INSERT INTO coordinator_user_decision
 (id,workspace_id,agent_id,job_id,environment,corp_id,conversation_id,initiator_id,sender_uid,sender_org_id,version,snapshot,proposal,send_request_id,card_biz_id)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
 ON CONFLICT(job_id) DO NOTHING RETURNING id::text`, r.ID, r.WorkspaceID, r.AgentID, r.JobID, r.Environment, r.CorpID, r.ConversationID, r.InitiatorID, r.SenderUID, r.SenderOrgID, Version, r.Snapshot, p, uuid.NewString(), r.CardID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `SELECT id::text FROM coordinator_user_decision WHERE job_id=$1 AND environment=$2 AND agent_id=$3 AND initiator_id=$4`, r.JobID, s.Environment, r.AgentID, r.InitiatorID).Scan(&id)
	}
	if err != nil {
		return "", err
	}
	if r.JobLease != "" {
		tag, parkErr := tx.Exec(ctx, `UPDATE inbound_coordinator_job SET status='pending',available_at='infinity',lease_token=NULL,lease_expires_at=NULL,attempt_count=GREATEST(attempt_count-1,0),last_error='awaiting_user_decision',updated_at=now() WHERE id=$1 AND status='running' AND lease_token=$2`, r.JobID, r.JobLease)
		if parkErr != nil {
			return "", parkErr
		}
		if tag.RowsAffected() != 1 {
			return "", errors.New("decision job lease lost")
		}
	}
	return id, tx.Commit(ctx)
}

// Accept never trusts option labels, plans or actor IDs from action.context.
// Rejected submissions are retained but do not consume the valid submission.
func (s *Store) Accept(ctx context.Context, e Event) (string, error) {
	return s.AcceptFrom(ctx, e, "", "")
}

func (s *Store) AcceptFrom(ctx context.Context, e Event, senderUID, senderOrgID string) (string, error) {
	if _, err := uuid.Parse(e.RequestID); err != nil {
		return "", pgx.ErrNoRows
	}
	if s.DB == nil || s.Environment == "" {
		return "", errors.New("decision store unavailable")
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	var i Identity
	var state string
	var expires *time.Time
	var proposal []byte
	var actualUID, actualOrg string
	err = tx.QueryRow(ctx, `SELECT environment,corp_id,conversation_id,COALESCE(card_biz_id,''),initiator_id,id::text,version,state,expires_at,proposal,sender_uid,sender_org_id
 FROM coordinator_user_decision WHERE id=$1 AND environment=$2 FOR UPDATE`, e.RequestID, s.Environment).Scan(&i.Environment, &i.CorpID, &i.ConversationID, &i.CardID, &i.InitiatorID, &i.RequestID, &i.Version, &state, &expires, &proposal, &actualUID, &actualOrg)
	if err != nil {
		return "", err
	}
	// Use the database clock for both expiry and acceptance, across replicas.
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return "", err
	}
	deadline := time.Time{}
	early := (state == "sending" || state == "send_unknown") && expires == nil
	if early {
		deadline = now.Add(24 * time.Hour)
	}
	if expires != nil {
		deadline = *expires
	}
	outcome := "accepted"
	if senderUID != "" && (senderUID != actualUID || senderOrgID != actualOrg) {
		outcome = "sender_identity_mismatch"
	}
	if err = i.Validate(e, s.Environment, now, deadline); err != nil {
		outcome = err.Error()
	}
	if outcome == "accepted" && state != "waiting" && !early {
		outcome = "already_resolved"
	}
	var p Proposal
	if err = json.Unmarshal(proposal, &p); err != nil {
		return "", fmt.Errorf("decode frozen proposal: %w", err)
	}
	option := ""
	if outcome == "accepted" {
		if e.ValidationError != "" || len(e.Selected) > 1 || (len(e.Selected) == 0 && strings.TrimSpace(e.Custom) == "") || len([]rune(e.Custom)) > 8000 {
			outcome = "invalid_answer"
		}
		if len(e.Selected) == 1 {
			option = e.Selected[0]
			found := false
			for _, o := range p.Options {
				if o.ID == option {
					found = true
					break
				}
			}
			if !found {
				outcome = "unknown_option"
			}
		}
	}
	if !json.Valid(e.Raw) || e.ID == "" {
		return "", errors.New("invalid event audit payload")
	}
	tag, err := tx.Exec(ctx, `INSERT INTO coordinator_user_decision_event(decision_id,environment,event_id,operator_id,outcome,payload,protocol)
 VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(environment,event_id) DO NOTHING`, e.RequestID, s.Environment, e.ID, e.OperatorID, outcome, e.Raw, e.Protocol)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() == 0 {
		// Preserve the original submission and its outcome; replay delivery is
		// separate audit metadata, never a second accepted sample.
		_, err = tx.Exec(ctx, `UPDATE coordinator_user_decision_event SET delivery_count=delivery_count+1,last_received_at=clock_timestamp(),last_delivery_outcome='duplicate_event' WHERE environment=$1 AND event_id=$2`, s.Environment, e.ID)
		if err != nil {
			return "", err
		}
		return "duplicate_event", tx.Commit(ctx)
	}
	if outcome == "accepted" {
		b, _ := json.Marshal(Submission{EventID: e.ID, OperatorID: e.OperatorID, OptionID: option, Custom: e.Custom, ReceivedAt: now})
		_, err = tx.Exec(ctx, `UPDATE coordinator_user_decision SET state='accepted',submission=$2,accepted_at=$3,sent_at=COALESCE(sent_at,$3),expires_at=COALESCE(expires_at,$3+interval '24 hours'),available_at=$3,card_update_pending=true,updated_at=$3 WHERE id=$1`, e.RequestID, b, now)
		if err != nil {
			return "", err
		}
	}
	return outcome, tx.Commit(ctx)
}

// ConfirmSent starts the 24 hour window exactly once. Retries cannot extend it.
// ConfirmSent receives the provider receipt, never a callback-supplied ID.
// The creation correlation ID is distinct from the provider-generated bizId.
// Only the first confirmed send may replace it; later confirmations must match.
func (s *Store) ConfirmSent(ctx context.Context, id, cardID string) error {
	if cardID == "" {
		return errors.New("missing confirmed card identity")
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE coordinator_user_decision SET state=CASE WHEN state IN ('sending','send_unknown') THEN 'waiting' ELSE state END,card_biz_id=$3,
 sent_at=COALESCE(sent_at,now()),expires_at=COALESCE(expires_at,now()+interval '24 hours'),
 lease_token=CASE WHEN state IN ('sending','send_unknown') THEN NULL ELSE lease_token END,
 lease_expires_at=CASE WHEN state IN ('sending','send_unknown') THEN NULL ELSE lease_expires_at END,
 card_update_pending=true,available_at=now(),updated_at=now()
 WHERE id=$1 AND environment=$2 AND state IN ('sending','send_unknown','waiting','accepted','resuming','dispatched','not_executed') AND ((sent_at IS NULL AND state IN ('sending','send_unknown')) OR card_biz_id=$3)`, id, s.Environment, cardID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("card confirmation does not match pending send")
	}
	return tx.Commit(ctx)
}

// Sweep only resolves unaccepted requests. Accepted work must pass the same
// active-agent and permissions checks immediately before committing its plan.
func (s *Store) Sweep(ctx context.Context) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `UPDATE coordinator_user_decision d SET state=CASE WHEN NOT EXISTS(SELECT 1 FROM agent a WHERE a.id=d.agent_id AND a.archived_at IS NULL AND a.status NOT IN ('disabled','paused')) THEN 'cancelled' ELSE 'expired' END,card_update_pending=true,available_at=now(),updated_at=now()
 WHERE environment=$1 AND state IN ('prepared','sending','send_unknown','waiting','accepted','resuming') AND ((state='waiting' AND expires_at<=now()) OR NOT EXISTS(SELECT 1 FROM agent a WHERE a.id=d.agent_id AND a.archived_at IS NULL AND a.status NOT IN ('disabled','paused'))) RETURNING job_id::text,state`, s.Environment)
	if err != nil {
		return err
	}
	type terminal struct{ job, state string }
	var ended []terminal
	for rows.Next() {
		var item terminal
		if err = rows.Scan(&item.job, &item.state); err != nil {
			rows.Close()
			return err
		}
		ended = append(ended, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, item := range ended {
		text := "选择已过期，本次未执行。"
		if item.state == "cancelled" {
			text = "员工已停用或删除，本次未执行。"
		}
		plan, _ := json.Marshal(map[string]any{"Action": "reply", "UserText": text, "PlanVersion": "window-plan-v1", "Reason": "user_decision_" + item.state})
		_, err = tx.Exec(ctx, `UPDATE inbound_coordinator_job SET command=jsonb_set(command,'{_coordinator_plan}',$2::jsonb),status='pending',available_at=now(),lease_token=NULL,lease_expires_at=NULL,last_error=NULL,updated_at=now() WHERE id=$1 AND status='pending' AND last_error='awaiting_user_decision'`, item.job, plan)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// LoadForJob returns the frozen request. Neither a setting change nor newer
// incoming messages mutate the snapshot belonging to this job.
func (s *Store) LoadForJob(ctx context.Context, jobID string) (Request, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return Request{}, err
	}
	defer tx.Rollback(ctx)
	var body []byte
	err = tx.QueryRow(ctx, `SELECT to_jsonb(d) FROM coordinator_user_decision d WHERE job_id=$1 AND environment=$2`, jobID, s.Environment).Scan(&body)
	if err != nil {
		return Request{}, err
	}
	var r Request
	if err = json.Unmarshal(body, &r); err != nil {
		return Request{}, err
	}
	return r, tx.Commit(ctx)
}

// ClaimSend commits the sending state before the network call. A crashed
// sender becomes send_unknown, never prepared: lease expiry cannot resend.
func (s *Store) ClaimSend(ctx context.Context, uid, orgID string) (Request, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return Request{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `UPDATE coordinator_user_decision SET state='send_unknown',lease_token=NULL,lease_expires_at=NULL,last_error='sender lease expired; reconcile original send',updated_at=now()
 WHERE environment=$1 AND state='sending' AND lease_expires_at<=now()`, s.Environment); err != nil {
		return Request{}, err
	}
	var body []byte
	err = tx.QueryRow(ctx, `WITH candidate AS (
 SELECT id FROM coordinator_user_decision WHERE environment=$1 AND sender_uid=$2 AND sender_org_id=$3 AND state='prepared' AND available_at<=now() ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1)
 UPDATE coordinator_user_decision d SET state='sending',lease_token=gen_random_uuid(),lease_expires_at=now()+interval '1 minute',updated_at=now()
 FROM candidate WHERE d.id=candidate.id RETURNING to_jsonb(d)`, s.Environment, uid, orgID).Scan(&body)
	if errors.Is(err, pgx.ErrNoRows) {
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return Request{}, commitErr
		}
		return Request{}, err
	}
	if err != nil {
		return Request{}, err
	}
	var r Request
	if err = json.Unmarshal(body, &r); err != nil {
		return Request{}, err
	}
	return r, tx.Commit(ctx)
}

// MarkSendUnknown is deliberately not a resend queue.
func (s *Store) MarkSendUnknown(ctx context.Context, id string) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `UPDATE coordinator_user_decision SET state='send_unknown',lease_token=NULL,lease_expires_at=NULL,last_error='send outcome unknown; reconcile original send',updated_at=now()
 WHERE id=$1 AND environment=$2 AND state='sending'`, id, s.Environment)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RejectSend atomically records a definite rejection and wakes the original job
// with a reply-only checkpoint. No user choice or business work is synthesized.
func (s *Store) RejectSend(ctx context.Context, r Request, message string) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	plan, err := json.Marshal(map[string]any{"Action": "reply", "UserText": message, "PlanVersion": "window-plan-v1", "Reason": "user_decision_send_rejected"})
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE coordinator_user_decision SET state='not_executed',last_error=$4,final_plan=$5,card_update_pending=false,lease_token=NULL,lease_expires_at=NULL,updated_at=now() WHERE id=$1 AND environment=$2 AND state='sending' AND lease_token=$3 AND sent_at IS NULL`, r.ID, s.Environment, r.LeaseToken, message, plan)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("decision send rejection no longer owns request")
	}
	tag, err = tx.Exec(ctx, `UPDATE inbound_coordinator_job SET command=jsonb_set(command,'{_coordinator_plan}',$2::jsonb),status='pending',available_at=now(),lease_token=NULL,lease_expires_at=NULL,last_error=NULL,updated_at=now() WHERE id=$1 AND status='pending' AND last_error='awaiting_user_decision'`, r.JobID, plan)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("waiting coordinator job could not resume after send rejection")
	}
	return tx.Commit(ctx)
}
