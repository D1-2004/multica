package employeeentry

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/eventrouter"
)

// ExecutionFact records a Host-verified terminal fact without authorizing work.
type ExecutionFact struct {
	Scope                                  Scope
	ReceiptID, PrincipalID, ConfigRevision string
	Payload                                json.RawMessage
	State, Reason                          string
}

// RecordExecutionFact shares the receipt's transaction when Store wraps pgx.Tx.
// A fact never joins a message window, grants tool access, or creates a job.
// The boolean identifies a new insert; the caller still owns any outer commit.
func (s *Store) RecordExecutionFact(ctx context.Context, fact ExecutionFact) (Consumption, bool, error) {
	if !validReceiptScope(fact.Scope) || !validID(fact.ReceiptID) || !validID(fact.PrincipalID) || !json.Valid(fact.Payload) || len(fact.Payload) > eventrouter.MaxPayloadBytes || fact.ConfigRevision == "" || (fact.State != "completed" && fact.State != "held") || (fact.State == "held" && fact.Reason == "") {
		return Consumption{}, false, ErrInvalid
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Consumption{}, false, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	var workspace string
	if err = tx.QueryRow(ctx, `SELECT id FROM workspace WHERE id=$1::uuid FOR KEY SHARE`, fact.Scope.WorkspaceID).Scan(&workspace); err != nil {
		return Consumption{}, false, mapError(err)
	}
	args := append(scopeArgs(fact.Scope), fact.ReceiptID, fact.PrincipalID)
	var receiptRoute, receiptState, receiptReason, category string
	var payloadMatches bool
	if err = tx.QueryRow(ctx, `SELECT route,state,reason,envelope->>'category',envelope->'payload'=$7::jsonb FROM scene_event_receipt WHERE `+receiptScopeWhere+` AND id=$5::uuid AND principal_id=$6::uuid FOR UPDATE`, append(args, fact.Payload)...).Scan(&receiptRoute, &receiptState, &receiptReason, &category, &payloadMatches); err != nil {
		return Consumption{}, false, mapError(err)
	}
	// Provider routing is independent of the frozen Employee work owner.
	resolvedRoute := receiptRoute == eventrouter.Unified && receiptState == eventrouter.Ready || receiptRoute == eventrouter.Legacy && receiptState == eventrouter.Legacy
	if category != eventrouter.RunCallback || (fact.State == "completed" && (!resolvedRoute || receiptReason != "" || fact.Scope.SceneID == "")) {
		return Consumption{}, false, ErrInvalid
	}
	if !payloadMatches {
		return Consumption{}, false, ErrConflict
	}
	var c Consumption
	var payload json.RawMessage
	err = tx.QueryRow(ctx, `SELECT receipt_id::text,owner_loop,COALESCE(job_id::text,''),state,reason,payload FROM employee_event_consumption WHERE `+receiptScopeWhere+` AND receipt_id=$5::uuid AND principal_id=$6::uuid`, args...).Scan(&c.ReceiptID, &c.Owner, &c.JobID, &c.State, &c.Reason, &payload)
	if err == nil {
		equal, err := jsonEqual(ctx, tx, payload, fact.Payload)
		if err != nil {
			return Consumption{}, false, err
		}
		if !equal || c.Owner != Employee || c.JobID != "" || c.State != fact.State || c.Reason != fact.Reason {
			return Consumption{}, false, ErrConflict
		}
		return c, false, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Consumption{}, false, err
	}
	c = Consumption{ReceiptID: fact.ReceiptID, Owner: Employee, State: fact.State, Reason: fact.Reason}
	_, err = tx.Exec(ctx, `INSERT INTO employee_event_consumption(workspace_id,agent_id,tenant_org_id,scene_id,receipt_id,principal_id,owner_loop,config_revision,payload,state,reason) VALUES($1::uuid,$2::uuid,$3,NULLIF($4,'')::uuid,$5::uuid,$6::uuid,'employee',$7,$8::jsonb,$9,$10)`, append(args, fact.ConfigRevision, fact.Payload, c.State, c.Reason)...)
	if err != nil {
		return Consumption{}, false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Consumption{}, false, err
	}
	return c, true, nil
}
