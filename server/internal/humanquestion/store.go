package humanquestion

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/employeeentry"
)

// Store uses the caller's transaction for both answers and their durable wake.
type Store struct{ DB employeeentry.DB }

func NewStore(database employeeentry.DB) *Store { return &Store{DB: database} }

const columns = `id::text,workspace_id::text,agent_id::text,tenant_org_id,scene_id::text,principal_id::text,source_job_id::text,source_receipt_id::text,source_ref,requester_ref,operator_open_id,COALESCE(task_id::text,''),COALESCE(run_id::text,''),goal_revision,version,summary,choice,card_public_id,action_id,state,COALESCE(response_id::text,''),created_at`
const scoped = `workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND scene_id=$4::uuid`

func scopeArgs(s employeeentry.Scope) []any {
	return []any{s.WorkspaceID, s.AgentID, s.TenantOrgID, s.SceneID}
}
func read(row pgx.Row) (Question, error) {
	var q Question
	var spec []byte
	err := row.Scan(&q.ID, &q.Scope.WorkspaceID, &q.Scope.AgentID, &q.Scope.TenantOrgID, &q.Scope.SceneID, &q.PrincipalID, &q.SourceJobID, &q.SourceReceiptID, &q.SourceRef, &q.RequesterRef, &q.OperatorOpenID, &q.TaskID, &q.RunID, &q.GoalRevision, &q.Version, &q.Summary, &spec, &q.PublicID, &q.ActionID, &q.State, &q.ResponseID, &q.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return q, ErrNotFound
	}
	if err != nil {
		return q, err
	}
	err = json.Unmarshal(spec, &q.Choice)
	return q, err
}

// LockScope keeps the established workspace -> scene -> question -> Task order.
func LockScope(ctx context.Context, tx pgx.Tx, s employeeentry.Scope) error {
	var id string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM workspace WHERE id=$1::uuid FOR KEY SHARE`, s.WorkspaceID).Scan(&id); err != nil {
		return err
	}
	return tx.QueryRow(ctx, `SELECT id::text FROM agent_scene WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND id=$4::uuid FOR UPDATE`, scopeArgs(s)...).Scan(&id)
}
func (s *Store) Get(ctx context.Context, scope employeeentry.Scope, id string) (Question, error) {
	if !idValid(id) {
		return Question{}, ErrInvalid
	}
	return read(s.DB.QueryRow(ctx, `SELECT `+columns+` FROM employee_human_question WHERE `+scoped+` AND id=$5::uuid`, append(scopeArgs(scope), id)...))
}
func (s *Store) ByPublicID(ctx context.Context, agentID, org, publicID string) (Question, error) {
	return read(s.DB.QueryRow(ctx, `SELECT `+columns+` FROM employee_human_question WHERE agent_id=$1::uuid AND tenant_org_id=$2 AND card_public_id=$3`, agentID, org, publicID))
}
func (s *Store) Pending(ctx context.Context, scope employeeentry.Scope, requester string) ([]Question, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+columns+` FROM employee_human_question WHERE `+scoped+` AND requester_ref=$5 AND state IN ('open','deferred') ORDER BY created_at,id LIMIT 20`, append(scopeArgs(scope), requester)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Question{}
	for rows.Next() {
		q, err := read(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

func StageTx(ctx context.Context, tx pgx.Tx, q Question) (Question, error) {
	q.Choice = q.Choice.Normalized()
	q.State = "open"
	if q.Validate() != nil {
		return Question{}, ErrInvalid
	}
	if !strings.HasPrefix(q.SourceRef, q.SourceReceiptID+"/") {
		return Question{}, ErrInvalid
	}
	if err := LockScope(ctx, tx, q.Scope); err != nil {
		return Question{}, err
	}
	var valid bool
	args := append(scopeArgs(q.Scope), q.SourceJobID, q.SourceReceiptID, q.PrincipalID)
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM employee_event_consumption c JOIN employee_scene_job j ON j.id=c.job_id WHERE c.`+strings.ReplaceAll(scoped, " AND ", " AND c.")+` AND c.job_id=$5::uuid AND c.receipt_id=$6::uuid AND c.principal_id=$7::uuid AND c.owner_loop='employee' AND j.kind='message')`, args...).Scan(&valid)
	if err != nil {
		return Question{}, err
	}
	if !valid {
		return Question{}, ErrForbidden
	}
	if err := CurrentTargetTx(ctx, tx, q); err != nil {
		return Question{}, err
	}
	spec, _ := json.Marshal(q.Choice)
	_, err = tx.Exec(ctx, `INSERT INTO employee_human_question(id,workspace_id,agent_id,tenant_org_id,scene_id,principal_id,source_job_id,source_receipt_id,source_ref,requester_ref,operator_open_id,task_id,run_id,goal_revision,version,summary,choice,card_public_id,action_id) VALUES($1::uuid,$2::uuid,$3::uuid,$4,$5::uuid,$6::uuid,$7::uuid,$8::uuid,$9,$10,$11,NULLIF($12,'')::uuid,NULLIF($13,'')::uuid,$14,$15,$16,$17,$18,$19) ON CONFLICT(id) DO NOTHING`, q.ID, q.Scope.WorkspaceID, q.Scope.AgentID, q.Scope.TenantOrgID, q.Scope.SceneID, q.PrincipalID, q.SourceJobID, q.SourceReceiptID, q.SourceRef, q.RequesterRef, q.OperatorOpenID, q.TaskID, q.RunID, q.GoalRevision, q.Version, q.Summary, spec, q.PublicID, q.ActionID)
	if err != nil {
		return Question{}, err
	}
	stored, err := read(tx.QueryRow(ctx, `SELECT `+columns+` FROM employee_human_question WHERE id=$1::uuid`, q.ID))
	if err != nil {
		return stored, err
	}
	display := stored
	display.State = q.State
	display.ResponseID = ""
	a, _ := json.Marshal(q)
	b, _ := json.Marshal(display)
	// Immutable routing fields are intentionally excluded from display JSON.
	if string(a) != string(b) || stored.Scope != q.Scope || stored.PrincipalID != q.PrincipalID || stored.SourceJobID != q.SourceJobID || stored.SourceReceiptID != q.SourceReceiptID || stored.SourceRef != q.SourceRef || stored.RequesterRef != q.RequesterRef || stored.OperatorOpenID != q.OperatorOpenID || stored.PublicID != q.PublicID || stored.ActionID != q.ActionID {
		return Question{}, ErrConflict
	}
	return stored, nil
}

// CurrentTargetTx never substitutes a new Run or the most recent Task.
func CurrentTargetTx(ctx context.Context, tx pgx.Tx, q Question) error {
	if q.TaskID == "" {
		return nil
	}
	var id string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM employee_task WHERE id=$1::uuid AND workspace_id=$2::uuid AND agent_id=$3::uuid AND tenant_org_id=$4 FOR UPDATE`, q.TaskID, q.Scope.WorkspaceID, q.Scope.AgentID, q.Scope.TenantOrgID).Scan(&id); err != nil {
		return err
	}
	var valid bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM employee_task t JOIN employee_task_run r ON r.task_id=t.id WHERE t.workspace_id=$1::uuid AND t.agent_id=$2::uuid AND t.tenant_org_id=$3 AND t.scene_id=$4::uuid AND t.id=$5::uuid AND r.id=$6::uuid AND r.goal_revision=$7
 AND (t.goal_revision=$7 OR (t.goal_revision=$7+1 AND $8<>'' AND EXISTS(SELECT 1 FROM employee_task_entry e WHERE e.task_id=t.id AND e.kind='amendment' AND e.goal_revision=t.goal_revision AND e.source_namespace='employee_scene' AND e.source_key=$9)))
 AND t.owner_loop='employee' AND t.dispatch_mode='direct' AND t.state NOT IN ('cancelled','stopping') AND NOT EXISTS(SELECT 1 FROM employee_task_entry e WHERE e.task_id=t.id AND e.kind='input' AND e.payload->>'operation'='stop' AND e.seq>=r.input_seq) AND r.state='succeeded'
 AND NOT EXISTS(SELECT 1 FROM employee_task_run newer JOIN agent_task_queue aq ON aq.id=newer.queue_task_id WHERE newer.task_id=t.id AND newer.created_at>r.created_at AND COALESCE(aq.context->>'employee_human_response_id','')<>$8))`, append(scopeArgs(q.Scope), q.TaskID, q.RunID, q.GoalRevision, q.ResponseID, q.SourceReceiptID+"/"+q.ResponseID+"/amend")...).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return ErrStale
	}
	return nil
}

type AcceptedHook func(context.Context, pgx.Tx, Question, *Response) error

// AcceptTx wins the answer once and calls a storage-only admission hook before
// commit. Restart replays the original job rather than losing the wake.
func AcceptTx(ctx context.Context, tx pgx.Tx, scope employeeentry.Scope, r Response, hook AcceptedHook) (Question, Response, error) {
	if !idValid(r.QuestionID) {
		return Question{}, r, ErrInvalid
	}
	if err := LockScope(ctx, tx, scope); err != nil {
		return Question{}, r, err
	}
	q, err := read(tx.QueryRow(ctx, `SELECT `+columns+` FROM employee_human_question WHERE `+scoped+` AND id=$5::uuid FOR UPDATE`, append(scopeArgs(scope), r.QuestionID)...))
	if err != nil {
		return q, r, err
	}
	if err = q.ValidateResponse(r); err != nil {
		return q, r, err
	}
	var old []byte
	err = tx.QueryRow(ctx, `SELECT body FROM employee_human_response WHERE question_id=$1::uuid AND input_surface=$2 AND event_id=$3`, q.ID, r.Surface, r.EventID).Scan(&old)
	if err == nil {
		var stored Response
		if e := json.Unmarshal(old, &stored); e != nil {
			return q, r, e
		}
		want, _ := responseBody(r)
		canonical, _ := responseBody(stored)
		if string(want) != string(canonical) {
			return q, r, ErrConflict
		}
		return q, stored, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return q, r, err
	}
	if q.State != "open" && q.State != "deferred" {
		return q, r, ErrStale
	}
	if r.Intent != "dismiss" {
		if err = CurrentTargetTx(ctx, tx, q); err != nil {
			return q, r, err
		}
	}
	if hook == nil {
		return q, r, ErrInvalid
	}
	if err = hook(ctx, tx, q, &r); err != nil {
		return q, r, err
	}
	raw, err := responseBody(r)
	if err != nil {
		return q, r, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO employee_human_response(id,question_id,event_id,input_surface,requester_ref,body,receipt_id,job_id) VALUES($1::uuid,$2::uuid,$3,$4,$5,$6,NULLIF($7,'')::uuid,NULLIF($8,'')::uuid)`, r.ID, q.ID, r.EventID, r.Surface, r.RequesterRef, raw, r.ReceiptID, r.JobID)
	if err != nil {
		return q, r, err
	}
	state := "answered"
	if r.Intent == "skip" {
		state = "deferred"
	}
	_, err = tx.Exec(ctx, `UPDATE employee_human_question SET state=$2,response_id=$3::uuid WHERE id=$1::uuid`, q.ID, state, r.ID)
	q.State, q.ResponseID = state, r.ID
	return q, r, err
}

func (s *Store) Response(ctx context.Context, id string) (Response, error) {
	var r Response
	var raw []byte
	if !idValid(id) {
		return r, ErrInvalid
	}
	err := s.DB.QueryRow(ctx, `SELECT body,COALESCE(receipt_id::text,''),COALESCE(job_id::text,'') FROM employee_human_response WHERE id=$1::uuid`, id).Scan(&raw, &r.ReceiptID, &r.JobID)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	receipt, job := r.ReceiptID, r.JobID
	err = json.Unmarshal(raw, &r)
	r.ReceiptID, r.JobID = receipt, job
	return r, err
}
