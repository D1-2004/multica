package employeeentry

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type DB interface {
	Begin(context.Context) (pgx.Tx, error)
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}
type Store struct{ db DB }

func NewStore(db DB) *Store { return &Store{db: db} }

const jobColumns = `id::text,workspace_id::text,agent_id::text,tenant_org_id,scene_id::text,principal_id::text,items,state,COALESCE(lease_token::text,''),lease_until,generation,attempt_count,model_attempts,input_snapshot,model_journal,outcome,last_error,created_at`
const scopeWhere = `workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND scene_id=$4::uuid`
const receiptScopeWhere = `workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND scene_id IS NOT DISTINCT FROM NULLIF($4,'')::uuid`

func scopeArgs(s Scope) []any { return []any{s.WorkspaceID, s.AgentID, s.TenantOrgID, s.SceneID} }
func validID(s string) bool {
	id, err := uuid.Parse(s)
	return err == nil && id != uuid.Nil && id.String() == s
}
func validReceiptScope(s Scope) bool {
	return validID(s.WorkspaceID) && validID(s.AgentID) && strings.TrimSpace(s.TenantOrgID) == s.TenantOrgID && (s.SceneID == "" || validScope(s))
}

func validScope(s Scope) bool {
	return validID(s.WorkspaceID) && validID(s.AgentID) && validID(s.SceneID) && strings.TrimSpace(s.TenantOrgID) == s.TenantOrgID && s.TenantOrgID != ""
}
func mapError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
func scanJob(row pgx.Row) (Job, error) {
	var j Job
	err := row.Scan(&j.ID, &j.Scope.WorkspaceID, &j.Scope.AgentID, &j.Scope.TenantOrgID, &j.Scope.SceneID, &j.PrincipalID, &j.Items, &j.State, &j.LeaseToken, &j.LeaseUntil, &j.Generation, &j.Attempts, &j.ModelAttempts, &j.InputSnapshot, &j.ModelJournal, &j.Outcome, &j.LastError, &j.CreatedAt)
	return j, mapError(err)
}
func lockScope(ctx context.Context, tx pgx.Tx, s Scope) error {
	if !validScope(s) {
		return ErrInvalid
	}
	var id string
	if err := tx.QueryRow(ctx, `SELECT id FROM workspace WHERE id=$1::uuid FOR KEY SHARE`, s.WorkspaceID).Scan(&id); err != nil {
		return mapError(err)
	}
	return mapError(tx.QueryRow(ctx, `SELECT id FROM agent_scene WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND id=$4::uuid FOR UPDATE`, scopeArgs(s)...).Scan(&id))
}
func jsonEqual(ctx context.Context, tx pgx.Tx, a, b json.RawMessage) (bool, error) {
	var equal bool
	err := tx.QueryRow(ctx, `SELECT $1::jsonb=$2::jsonb`, a, b).Scan(&equal)
	return equal, err
}

// Admit freezes one work owner independently of the provider admission route.
// The scope and principal must match the already persisted event receipt.
func (s *Store) Admit(ctx context.Context, a Admission) (Consumption, error) {
	if !validReceiptScope(a.Scope) || !validID(a.Item.ReceiptID) || !validID(a.Item.PrincipalID) || !json.Valid(a.Item.Payload) || len(a.Item.Payload) > 1<<20 || a.Item.MessageCount < 1 || (a.Owner != Coordinator && a.Owner != Employee) {
		return Consumption{}, ErrInvalid
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Consumption{}, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if a.Scope.SceneID == "" {
		var workspace string
		err = tx.QueryRow(ctx, `SELECT id FROM workspace WHERE id=$1::uuid FOR KEY SHARE`, a.Scope.WorkspaceID).Scan(&workspace)
	} else {
		err = lockScope(ctx, tx, a.Scope)
	}
	if err != nil {
		return Consumption{}, mapError(err)
	}
	args := append(scopeArgs(a.Scope), a.Item.ReceiptID, a.Item.PrincipalID)
	var receiptState, receiptReason string
	lockReceipt := ""
	if a.Scope.SceneID == "" {
		lockReceipt = " FOR UPDATE"
	}
	if err = tx.QueryRow(ctx, `SELECT state,reason FROM scene_event_receipt WHERE `+receiptScopeWhere+` AND id=$5::uuid AND principal_id=$6::uuid`+lockReceipt, args...).Scan(&receiptState, &receiptReason); err != nil {
		return Consumption{}, mapError(err)
	}
	var c Consumption
	var payload json.RawMessage
	err = tx.QueryRow(ctx, `SELECT receipt_id::text,owner_loop,COALESCE(job_id::text,''),state,reason,payload FROM employee_event_consumption WHERE `+receiptScopeWhere+` AND receipt_id=$5::uuid AND principal_id=$6::uuid`, args...).Scan(&c.ReceiptID, &c.Owner, &c.JobID, &c.State, &c.Reason, &payload)
	if err == nil {
		equal, e := jsonEqual(ctx, tx, payload, a.Item.Payload)
		if e != nil {
			return Consumption{}, e
		}
		if !equal {
			return Consumption{}, ErrConflict
		}
		return c, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Consumption{}, err
	}
	if (a.Scope.SceneID == "" || receiptState == "unmapped" || receiptReason != "") && (a.Owner != Employee || a.HoldReason == "") {
		return Consumption{}, ErrInvalid
	}
	if a.Owner == Employee && a.HoldReason == "" && a.Item.MessageCount > MaxWindowMessages {
		return Consumption{}, ErrInvalid
	}
	c = Consumption{ReceiptID: a.Item.ReceiptID, Owner: a.Owner, State: "delegated"}
	if a.Owner == Employee && a.HoldReason != "" {
		c.State = "held"
		c.Reason = a.HoldReason
	}
	if a.Owner == Employee && a.HoldReason == "" {
		var pending Job
		pending, err = scanJob(tx.QueryRow(ctx, `SELECT `+jobColumns+` FROM employee_scene_job WHERE `+scopeWhere+` AND principal_id=$5::uuid AND state='pending' AND attempt_count=0 AND created_at>=now()-interval '250 milliseconds' AND jsonb_array_length(items)<16 AND message_count+$6<=32 ORDER BY created_at LIMIT 1 FOR UPDATE`, append(scopeArgs(a.Scope), a.Item.PrincipalID, a.Item.MessageCount)...))
		if err != nil && !errors.Is(err, ErrNotFound) {
			return Consumption{}, err
		}
		if err == nil {
			items, e := json.Marshal(append(pending.Items, a.Item))
			if e != nil {
				return Consumption{}, e
			}
			_, err = tx.Exec(ctx, `UPDATE employee_scene_job SET items=$2::jsonb,message_count=message_count+$3,updated_at=now() WHERE id=$1::uuid`, pending.ID, items, a.Item.MessageCount)
			c.JobID = pending.ID
		} else {
			items, e := json.Marshal([]Item{a.Item})
			if e != nil {
				return Consumption{}, e
			}
			err = tx.QueryRow(ctx, `INSERT INTO employee_scene_job(workspace_id,agent_id,tenant_org_id,scene_id,principal_id,items,message_count) VALUES($1::uuid,$2::uuid,$3,$4::uuid,$5::uuid,$6::jsonb,$7) RETURNING id::text`, append(scopeArgs(a.Scope), a.Item.PrincipalID, items, a.Item.MessageCount)...).Scan(&c.JobID)
		}
		if err != nil {
			return Consumption{}, err
		}
		c.State = "queued"
	}
	_, err = tx.Exec(ctx, `INSERT INTO employee_event_consumption(workspace_id,agent_id,tenant_org_id,scene_id,receipt_id,owner_loop,config_revision,principal_id,payload,job_id,state,reason) VALUES($1::uuid,$2::uuid,$3,NULLIF($4,'')::uuid,$5::uuid,$6,$7,$8::uuid,$9::jsonb,NULLIF($10,'')::uuid,$11,$12)`, append(scopeArgs(a.Scope), a.Item.ReceiptID, c.Owner, a.ConfigRevision, a.Item.PrincipalID, a.Item.Payload, c.JobID, c.State, c.Reason)...)
	if err != nil {
		return Consumption{}, err
	}
	return c, tx.Commit(ctx)
}

// Claim takes the workspace and scene locks before changing the candidate job.
// Scene serialization lasts only for this short transaction, never a model call.
func (s *Store) Claim(ctx context.Context) (Job, error) {
	candidate, err := scanJob(s.db.QueryRow(ctx, `SELECT `+jobColumns+` FROM employee_scene_job j WHERE ((state='pending' AND available_at<=now()) OR (state='running' AND lease_until<now())) AND NOT EXISTS(SELECT 1 FROM employee_scene_job active WHERE active.workspace_id=j.workspace_id AND active.agent_id=j.agent_id AND active.tenant_org_id=j.tenant_org_id AND active.scene_id=j.scene_id AND active.state='running' AND active.lease_until>=now()) ORDER BY available_at,created_at LIMIT 1`))
	if errors.Is(err, ErrNotFound) {
		return Job{}, ErrNoJob
	}
	if err != nil {
		return Job{}, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Job{}, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if err = lockScope(ctx, tx, candidate.Scope); err != nil {
		return Job{}, err
	}
	args := append(scopeArgs(candidate.Scope), candidate.ID)
	var busy bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM employee_scene_job WHERE `+scopeWhere+` AND state='running' AND lease_until>=now())`, scopeArgs(candidate.Scope)...).Scan(&busy); err != nil {
		return Job{}, err
	}
	if busy {
		return Job{}, ErrNoJob
	}
	job, err := scanJob(tx.QueryRow(ctx, `UPDATE employee_scene_job SET state='running',lease_token=gen_random_uuid(),lease_until=now()+interval '90 seconds',generation=generation+1,attempt_count=attempt_count+1,updated_at=now() WHERE `+scopeWhere+` AND id=$5::uuid AND ((state='pending' AND available_at<=now()) OR (state='running' AND lease_until<now())) RETURNING `+jobColumns, args...))
	if errors.Is(err, ErrNotFound) {
		return Job{}, ErrNoJob
	}
	if err != nil {
		return Job{}, err
	}
	return job, tx.Commit(ctx)
}
func (s *Store) lease(ctx context.Context, j Job, fn func(pgx.Tx, *Job) error) error {
	if !validScope(j.Scope) || !validID(j.ID) || !validID(j.LeaseToken) {
		return ErrInvalid
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if err = lockScope(ctx, tx, j.Scope); err != nil {
		return err
	}
	current, err := scanJob(tx.QueryRow(ctx, `SELECT `+jobColumns+` FROM employee_scene_job WHERE `+scopeWhere+` AND id=$5::uuid AND lease_token=$6::uuid AND generation=$7 AND state='running' AND lease_until>now() FOR UPDATE`, append(scopeArgs(j.Scope), j.ID, j.LeaseToken, j.Generation)...))
	if errors.Is(err, ErrNotFound) {
		return ErrLease
	}
	if err != nil {
		return err
	}
	if err = fn(tx, &current); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Store) WithLease(ctx context.Context, j Job, fn func(pgx.Tx) error) error {
	return s.lease(ctx, j, func(tx pgx.Tx, _ *Job) error {
		if fn == nil {
			return nil
		}
		return fn(tx)
	})
}
func (s *Store) SaveInput(ctx context.Context, j Job, input json.RawMessage) (json.RawMessage, error) {
	if !json.Valid(input) {
		return nil, ErrInvalid
	}
	var saved json.RawMessage
	err := s.lease(ctx, j, func(tx pgx.Tx, current *Job) error {
		if len(current.InputSnapshot) > 0 {
			saved = current.InputSnapshot
			return nil
		}
		saved = input
		_, err := tx.Exec(ctx, `UPDATE employee_scene_job SET input_snapshot=$2::jsonb WHERE id=$1::uuid`, j.ID, input)
		return err
	})
	return saved, err
}

// BeginModel reserves the request budget before network I/O. Replaying a saved
// completion costs no request and preserves the original native tool call IDs.
func (s *Store) BeginModel(ctx context.Context, j Job, ordinal int, request json.RawMessage) (json.RawMessage, error) {
	if ordinal < 0 || ordinal >= 3 || !json.Valid(request) {
		return nil, ErrInvalid
	}
	var cached json.RawMessage
	err := s.lease(ctx, j, func(tx pgx.Tx, current *Job) error {
		if ordinal > len(current.ModelJournal) {
			return ErrInvalid
		}
		if ordinal < len(current.ModelJournal) {
			turn := current.ModelJournal[ordinal]
			equal, err := jsonEqual(ctx, tx, turn.Request, request)
			if err != nil {
				return err
			}
			if !equal {
				return ErrConflict
			}
			if turn.Failure != "" {
				return &ModelFailure{Message: turn.Failure}
			}
			if len(turn.Response) > 0 {
				cached = turn.Response
				return nil
			}
		}
		if current.ModelAttempts >= 3 {
			return ErrModelBudget
		}
		if ordinal == len(current.ModelJournal) {
			current.ModelJournal = append(current.ModelJournal, ModelTurn{Request: request})
		}
		journal, err := json.Marshal(current.ModelJournal)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE employee_scene_job SET model_attempts=model_attempts+1,model_journal=$2::jsonb WHERE id=$1::uuid`, j.ID, journal)
		return err
	})
	return cached, err
}
func (s *Store) SaveModel(ctx context.Context, j Job, ordinal int, response json.RawMessage) error {
	if !json.Valid(response) {
		return ErrInvalid
	}
	return s.lease(ctx, j, func(tx pgx.Tx, current *Job) error {
		if ordinal < 0 || ordinal >= len(current.ModelJournal) {
			return ErrInvalid
		}
		if current.ModelJournal[ordinal].Failure != "" {
			return ErrConflict
		}
		if prior := current.ModelJournal[ordinal].Response; len(prior) > 0 {
			same, err := jsonEqual(ctx, tx, prior, response)
			if err != nil {
				return err
			}
			if !same {
				return ErrConflict
			}
			return nil
		}
		current.ModelJournal[ordinal].Response = response
		journal, err := json.Marshal(current.ModelJournal)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE employee_scene_job SET model_journal=$2::jsonb WHERE id=$1::uuid`, j.ID, journal)
		return err
	})
}
func (s *Store) SaveOutcome(ctx context.Context, j Job, outcome json.RawMessage) error {
	if !json.Valid(outcome) {
		return ErrInvalid
	}
	return s.lease(ctx, j, func(tx pgx.Tx, current *Job) error {
		if len(current.Outcome) > 0 {
			same, err := jsonEqual(ctx, tx, current.Outcome, outcome)
			if err != nil {
				return err
			}
			if !same {
				return ErrConflict
			}
			return nil
		}
		_, err := tx.Exec(ctx, `UPDATE employee_scene_job SET outcome=$2::jsonb WHERE id=$1::uuid`, j.ID, outcome)
		return err
	})
}

// Complete atomically records the existing outbox writes and marks this work
// consumption complete. fn must only write durable database effects.
func (s *Store) Complete(ctx context.Context, j Job, fn func(pgx.Tx) error) error {
	return s.lease(ctx, j, func(tx pgx.Tx, current *Job) error {
		if len(current.Outcome) == 0 {
			return ErrInvalid
		}
		if fn != nil {
			if err := fn(tx); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE employee_event_consumption SET state='completed' WHERE `+scopeWhere+` AND job_id=$5::uuid`, append(scopeArgs(j.Scope), j.ID)...); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE employee_scene_job SET state='completed',lease_token=NULL,lease_until=NULL,updated_at=now() WHERE id=$1::uuid`, j.ID)
		return err
	})
}
func (s *Store) Retry(ctx context.Context, j Job, reason string) error {
	return s.lease(ctx, j, func(tx pgx.Tx, _ *Job) error {
		_, err := tx.Exec(ctx, `UPDATE employee_scene_job SET state='pending',available_at=now()+interval '1 second',lease_token=NULL,lease_until=NULL,last_error=$2,updated_at=now() WHERE id=$1::uuid`, j.ID, reason)
		return err
	})
}

// Lookup reads a frozen owner before consulting mutable agent configuration.
func (s *Store) Lookup(ctx context.Context, scope Scope, receiptID string) (Consumption, error) {
	if !validReceiptScope(scope) || !validID(receiptID) {
		return Consumption{}, ErrInvalid
	}
	var c Consumption
	err := s.db.QueryRow(ctx, `SELECT receipt_id::text,owner_loop,COALESCE(job_id::text,''),state,reason FROM employee_event_consumption WHERE `+receiptScopeWhere+` AND receipt_id=$5::uuid`, append(scopeArgs(scope), receiptID)...).Scan(&c.ReceiptID, &c.Owner, &c.JobID, &c.State, &c.Reason)
	return c, mapError(err)
}

// ExecuteTool journals a Host result with its exact native call identity.
func (s *Store) ExecuteTool(ctx context.Context, j Job, key string, input json.RawMessage, execute func(pgx.Tx) (json.RawMessage, error)) (json.RawMessage, error) {
	if key == "" || len(key) > 256 || !json.Valid(input) || execute == nil {
		return nil, ErrInvalid
	}
	var result json.RawMessage
	err := s.lease(ctx, j, func(tx pgx.Tx, _ *Job) error {
		var prior json.RawMessage
		if err := tx.QueryRow(ctx, `SELECT tool_journal->$2 FROM employee_scene_job WHERE id=$1::uuid`, j.ID, key).Scan(&prior); err != nil {
			return err
		}
		if len(prior) > 0 {
			var saved struct {
				Input  json.RawMessage `json:"input"`
				Result json.RawMessage `json:"result"`
			}
			if err := json.Unmarshal(prior, &saved); err != nil {
				return err
			}
			same, err := jsonEqual(ctx, tx, saved.Input, input)
			if err != nil {
				return err
			}
			if !same {
				return ErrConflict
			}
			result = saved.Result
			return nil
		}
		var err error
		result, err = execute(tx)
		if err != nil {
			return err
		}
		if !json.Valid(result) {
			return ErrInvalid
		}
		record, err := json.Marshal(map[string]json.RawMessage{"input": input, "result": result})
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE employee_scene_job SET tool_journal=jsonb_set(tool_journal,ARRAY[$2::text],$3::jsonb) WHERE id=$1::uuid`, j.ID, key, record)
		return err
	})
	return result, err
}

func (s *Store) Hold(ctx context.Context, j Job, reason string) error {
	if reason == "" || len(reason) > 128 {
		return ErrInvalid
	}
	return s.lease(ctx, j, func(tx pgx.Tx, _ *Job) error {
		if _, err := tx.Exec(ctx, `UPDATE employee_event_consumption SET state='held',reason=$6 WHERE `+scopeWhere+` AND job_id=$5::uuid`, append(scopeArgs(j.Scope), j.ID, reason)...); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE employee_scene_job SET state='completed',lease_token=NULL,lease_until=NULL,last_error=$2,outcome=COALESCE(outcome,jsonb_build_object('held_reason',$2::text)),updated_at=now() WHERE id=$1::uuid`, j.ID, reason)
		return err
	})
}

func (s *Store) SaveModelFailure(ctx context.Context, j Job, ordinal int, message string) error {
	if message == "" {
		return ErrInvalid
	}
	return s.lease(ctx, j, func(tx pgx.Tx, current *Job) error {
		if ordinal < 0 || ordinal >= len(current.ModelJournal) {
			return ErrInvalid
		}
		turn := &current.ModelJournal[ordinal]
		if len(turn.Response) > 0 {
			return ErrConflict
		}
		if turn.Failure != "" {
			if turn.Failure != message {
				return ErrConflict
			}
			return nil
		}
		turn.Failure = message
		journal, err := json.Marshal(current.ModelJournal)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE employee_scene_job SET model_journal=$2::jsonb WHERE id=$1::uuid`, j.ID, journal)
		return err
	})
}
