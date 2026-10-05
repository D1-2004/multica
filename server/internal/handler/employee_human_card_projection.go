package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/humanquestion"
	"github.com/multica-ai/multica/server/internal/service/a2ui"
	"github.com/multica-ai/multica/server/internal/service/dingtalkresponse"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const employeeHumanCardUpdateLease = 90 * time.Second

// stageEmployeeHumanCardProjectionTx is storage-only. AcceptTx invokes it after
// admission and commits the answer, unique job and update intent together.
func (h *Handler) stageEmployeeHumanCardProjectionTx(ctx context.Context, tx pgx.Tx, q humanquestion.Question, r *humanquestion.Response) error {
	if r == nil || q.Validate() != nil || q.ValidateResponse(*r) != nil {
		return humanquestion.ErrInvalid
	}
	// A native skip has no final projection; a later text answer may close it.
	if r.Intent == "skip" || (q.PublicID == "" && q.ActionID == "") {
		return nil
	}
	ref, ok := a2ui.ParseRef(q.PublicID)
	if !ok || ref.ID.String() != q.ID || q.ActionID == "" {
		return humanquestion.ErrInvalid
	}
	// Putting a card aside permanently closes this interaction surface. A
	// later text answer resumes work, but never races a different FINISH
	// payload against an older, possibly unknown provider update.
	if q.State == "deferred" && q.ResponseID != "" {
		var closed bool
		err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM employee_human_card_projection p JOIN employee_human_response old ON old.id=p.response_id AND old.question_id=p.question_id WHERE p.question_id=$1::uuid AND p.workspace_id=$2::uuid AND p.agent_id=$3::uuid AND p.tenant_org_id=$4 AND p.scene_id=$5::uuid AND p.principal_id=$6::uuid AND old.body->>'intent'='defer')`, q.ID, q.Scope.WorkspaceID, q.Scope.AgentID, q.Scope.TenantOrgID, q.Scope.SceneID, q.PrincipalID).Scan(&closed)
		if err != nil || closed {
			return err
		}
	}
	var same bool
	err := tx.QueryRow(ctx, `INSERT INTO employee_human_card_projection(question_id,response_id,workspace_id,agent_id,tenant_org_id,scene_id,principal_id)
 VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5,$6::uuid,$7::uuid)
 ON CONFLICT(question_id) DO UPDATE SET question_id=EXCLUDED.question_id
 RETURNING response_id=$2::uuid AND workspace_id=$3::uuid AND agent_id=$4::uuid AND tenant_org_id=$5 AND scene_id=$6::uuid AND principal_id=$7::uuid`, q.ID, r.ID, q.Scope.WorkspaceID, q.Scope.AgentID, q.Scope.TenantOrgID, q.Scope.SceneID, q.PrincipalID).Scan(&same)
	if err != nil {
		return err
	}
	if !same {
		return humanquestion.ErrConflict
	}
	return nil
}

type employeeHumanCardProjection struct {
	QuestionID, ResponseID string
	Scope                  employeeentry.Scope
	PrincipalID, Lease     string
	Attempts               int
}

// ReconcileEmployeeHumanCardProjections closes original cards after a committed
// answer. Each claim is durable, provider calls occur outside DB transactions,
// and repeated FINISH updates use the same accepted result and original biz id.
func (h *Handler) ReconcileEmployeeHumanCardProjections(ctx context.Context, limit int) (int, error) {
	if h == nil || h.A2UI == nil || h.DingTalkResponses == nil || h.EmployeeSceneWorker == nil || !h.EmployeeSceneWorker.humanQuestionsReady(ctx) {
		return 0, nil
	}
	database, ok := employeeEntryDB(h)
	if !ok {
		return 0, humanquestion.ErrInvalid
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	// Repair pre-upgrade accepted cards, and a commit made by an older reader
	// during rolling deployment. This only stages intent; use-time authority is
	// independently checked before every external update below.
	_, err := database.Exec(ctx, `INSERT INTO employee_human_card_projection(question_id,response_id,workspace_id,agent_id,tenant_org_id,scene_id,principal_id)
 SELECT q.id,q.response_id,q.workspace_id,q.agent_id,q.tenant_org_id,q.scene_id,q.principal_id
 FROM employee_human_question q JOIN employee_human_response r ON r.id=q.response_id
 JOIN a2ui_interaction a ON a.id=q.id AND a.workspace_id=q.workspace_id AND a.agent_id=q.agent_id AND a.sender_org_id=q.tenant_org_id AND a.scene_id=q.scene_id::text AND a.public_id=q.card_public_id
 WHERE (q.state='answered' OR (q.state='deferred' AND r.body->>'intent'='defer')) AND q.action_id<>'' AND q.card_public_id<>'' AND r.body->>'intent'<>'skip'
 AND NOT EXISTS(SELECT 1 FROM employee_human_card_projection p WHERE p.question_id=q.id)
 ORDER BY q.created_at,q.id LIMIT $1 ON CONFLICT(question_id) DO NOTHING`, limit)
	if err != nil {
		return 0, err
	}
	processed := 0
	for range limit {
		lease := uuid.NewString()
		var p employeeHumanCardProjection
		err = database.QueryRow(ctx, `WITH candidate AS (
 SELECT question_id FROM employee_human_card_projection WHERE state='pending' AND available_at<=now()
 AND (lease_until IS NULL OR lease_until<=now()) ORDER BY available_at,question_id FOR UPDATE SKIP LOCKED LIMIT 1)
 UPDATE employee_human_card_projection p SET lease_token=$1::uuid,lease_until=now()+$2::interval,attempts=attempts+1,updated_at=now()
 FROM candidate c WHERE p.question_id=c.question_id
 RETURNING p.question_id::text,p.response_id::text,p.workspace_id::text,p.agent_id::text,p.tenant_org_id,p.scene_id::text,p.principal_id::text,p.lease_token::text,p.attempts`, lease, "90 seconds").Scan(&p.QuestionID, &p.ResponseID, &p.Scope.WorkspaceID, &p.Scope.AgentID, &p.Scope.TenantOrgID, &p.Scope.SceneID, &p.PrincipalID, &p.Lease, &p.Attempts)
		if errors.Is(err, pgx.ErrNoRows) {
			break
		}
		if err != nil {
			return processed, err
		}
		processed++
		updateCtx, cancel := context.WithTimeout(ctx, employeeHumanCardUpdateLease/2)
		updateErr := h.updateEmployeeHumanCardProjection(updateCtx, database, p)
		cancel()
		state, reason := "completed", ""
		if updateErr != nil {
			state, reason = "pending", "update_unconfirmed"
			switch {
			case errors.Is(updateErr, errEmployeeHumanCardNeverSent):
				state, reason = "completed", "card_send_suppressed"
			case errors.Is(updateErr, errEmployeeHumanCardReceiptPending):
				reason = "send_receipt_pending"
			case errors.Is(updateErr, humanquestion.ErrForbidden), errors.Is(updateErr, humanquestion.ErrStale), errors.Is(updateErr, humanquestion.ErrInvalid), errors.Is(updateErr, humanquestion.ErrNotFound), errors.Is(updateErr, a2ui.ErrNotFound), errors.Is(updateErr, a2ui.ErrInvalid), errors.Is(updateErr, pgx.ErrNoRows):
				state, reason = "blocked", "binding_or_authority_changed"
			}
		}
		// A lost lease cannot acknowledge another worker's update. Unknown
		// provider outcomes remain pending and retry the same replacement.
		_, err = database.Exec(ctx, `UPDATE employee_human_card_projection SET state=$3,last_error=$4,available_at=now()+$5::interval,
 lease_token=NULL,lease_until=NULL,completed_at=CASE WHEN $3='completed' THEN now() ELSE completed_at END,updated_at=now()
 WHERE question_id=$1::uuid AND lease_token=$2::uuid AND state='pending' AND response_id=$6::uuid`, p.QuestionID, p.Lease, state, reason, fmt.Sprintf("%d seconds", int(employeeHumanCardRetryDelay(p.Attempts).Seconds())), p.ResponseID)
		if err != nil {
			return processed, err
		}

		slog.InfoContext(ctx, "human card projection reconciled", "event", "employee_human_card_projection", "question_id", p.QuestionID, "response_id", p.ResponseID, "scene_id", p.Scope.SceneID, "state", state, "reason", reason, "attempt", p.Attempts)
	}
	return processed, nil
}

var errEmployeeHumanCardReceiptPending = errors.New("human question card send receipt is pending")
var errEmployeeHumanCardNeverSent = errors.New("human question card was suppressed before submission")

func employeeHumanCardRetryDelay(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	if attempts > 6 {
		attempts = 6
	}
	return time.Duration(1<<(attempts-1)) * time.Second
}

func employeeHumanCardResult(q humanquestion.Question, r humanquestion.Response) (a2ui.Result, error) {
	if q.ValidateResponse(r) != nil || r.Intent == "skip" {
		return a2ui.Result{}, humanquestion.ErrInvalid
	}
	if r.Intent == "dismiss" {
		return a2ui.Result{Outcome: "disabled"}, nil
	}
	if r.Intent == "defer" {
		return a2ui.Result{Outcome: "deferred"}, nil
	}
	result := a2ui.Result{Outcome: string(a2ui.StatusAnswered), Selected: []string{}, Labels: []string{}, Custom: r.RawText}
	for _, selected := range r.Selected {
		for i, option := range q.Choice.Options {
			if option.ID == selected {
				result.Selected = append(result.Selected, "o"+strconv.Itoa(i))
				result.Labels = append(result.Labels, option.Label)
				break
			}
		}
	}
	return result, nil
}

func (h *Handler) updateEmployeeHumanCardProjection(ctx context.Context, database employeeentry.DB, p employeeHumanCardProjection) error {
	tx, err := database.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = humanquestion.LockScope(ctx, tx, p.Scope); err != nil {
		return err
	}
	store := humanquestion.NewStore(tx)
	q, err := store.Get(ctx, p.Scope, p.QuestionID)
	if err != nil {
		return err
	}
	r, err := store.Response(ctx, p.ResponseID)
	if err != nil {
		return err
	}
	if q.PrincipalID != p.PrincipalID || (q.State != "answered" && !(q.State == "deferred" && r.Intent == "defer")) || (q.ResponseID != p.ResponseID && r.Intent != "defer") || r.QuestionID != q.ID || r.Intent == "skip" || q.ValidateResponse(r) != nil {
		return humanquestion.ErrForbidden
	}
	// Closing a valid accepted question is independent of whether its Task
	// has already finished or another authorized Run has since started.
	b, err := readEmployeeQuestionSource(ctx, tx, q)
	if err != nil {
		return err
	}
	var raw []byte
	var actionState, actionError string
	if err = tx.QueryRow(ctx, `SELECT input,state,error_code FROM response_action WHERE id=$1 AND workspace_id=$2::uuid AND agent_id=$3::uuid`, q.ActionID, q.Scope.WorkspaceID, q.Scope.AgentID).Scan(&raw, &actionState, &actionError); err != nil {
		return err
	}
	var in dingtalkresponse.ActionInput
	if json.Unmarshal(raw, &in) != nil || in.A2UICard == nil || in.A2UICard.QuestionID != q.ID || in.A2UICard.PublicID != q.PublicID || in.WorkspaceID != q.Scope.WorkspaceID || in.AgentID != q.Scope.AgentID || in.DWSOrgID != q.Scope.TenantOrgID || in.SceneID != q.Scope.SceneID || in.ConversationID != b.Registered.ExternalSceneID || in.SenderOpenDingTalkID != q.OperatorOpenID || b.Envelope.Command.ExternalIdentity.DWS == nil || in.DWSUID != b.Envelope.Command.ExternalIdentity.DWS.UID {
		return humanquestion.ErrForbidden
	}
	if err = employeeHumanCardSenderCurrent(ctx, tx, q, in.DWSUID); err != nil {
		return err
	}
	in.ActionID = q.ActionID
	var bizID, publicID, senderUID, sourceRef, sceneID, conversationID string
	var matches bool
	err = tx.QueryRow(ctx, `SELECT card_biz_id,public_id,sender_uid,source_ref,scene_id,conversation_id,workspace_id=$2::uuid AND agent_id=$3::uuid AND sender_org_id=$4 FROM a2ui_interaction WHERE id=$1::uuid`, q.ID, q.Scope.WorkspaceID, q.Scope.AgentID, q.Scope.TenantOrgID).Scan(&bizID, &publicID, &senderUID, &sourceRef, &sceneID, &conversationID, &matches)
	if err != nil {
		return err
	}
	if !matches || publicID != q.PublicID || senderUID != in.DWSUID || sourceRef != q.SourceRef || sceneID != q.Scope.SceneID || conversationID != in.ConversationID {
		return humanquestion.ErrForbidden
	}
	neverSent := bizID == "" && actionState == "cancelled" && strings.HasPrefix(actionError, "host_send_suppressed:")
	if bizID == "" && !neverSent {
		return errEmployeeHumanCardReceiptPending
	}
	result, err := employeeHumanCardResult(q, r)
	if err != nil {
		return err
	}
	// Store the canonical result for both native and ordinary-text answers.
	// This is presentation only; routing still uses the accepted typed job.
	resultJSON, _ := json.Marshal(result)
	if _, err = tx.Exec(ctx, `UPDATE a2ui_interaction SET status='answered',result=$2,event_id=$3,operator_uid=$4,resolved_at=COALESCE(resolved_at,now()) WHERE id=$1::uuid`, q.ID, resultJSON, r.EventID, q.OperatorOpenID); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	if neverSent {
		return errEmployeeHumanCardNeverSent
	}
	messages, err := h.A2UI.ResolvedProjection(ctx, q.PublicID, result)
	if err != nil {
		return fmt.Errorf("closed card projection: %w", err)
	}
	// Recheck the exact source at the external-effect boundary after projection
	// work. Revoked access never gets an employee-authenticated provider call.
	if _, err = readEmployeeQuestionSource(ctx, database, q); err != nil {
		return err
	}
	if err = employeeHumanCardSenderCurrent(ctx, database, q, in.DWSUID); err != nil {
		return err
	}

	return h.DingTalkResponses.UpdateQuestionCard(ctx, in, bizID, messages)
}

func employeeHumanCardSenderCurrent(ctx context.Context, database employeeentry.DB, q humanquestion.Question, uid string) error {
	identity, err := db.New(database).GetAgentDingTalkIdentity(ctx, db.GetAgentDingTalkIdentityParams{WorkspaceID: parseUUID(q.Scope.WorkspaceID), AgentID: parseUUID(q.Scope.AgentID)})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && identity.DwsUid != uid) {
		return humanquestion.ErrForbidden
	}
	return err
}
