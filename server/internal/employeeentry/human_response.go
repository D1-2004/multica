package employeeentry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/eventrouter"
)

const HumanResponsePayloadSchema = "employee.human_response.v1"

// HumanResponse only identifies a Host-accepted answer. The worker reads the
// question and response ledger for content, authority and continuation context.
type HumanResponse struct {
	QuestionRef string `json:"question_ref"`
	ResponseRef string `json:"response_ref"`
	Version     int    `json:"version"`
}

func (r HumanResponse) Validate() error {
	if !validID(r.QuestionRef) || !validID(r.ResponseRef) || r.Version < 1 {
		return ErrInvalid
	}
	return nil
}

// DecodeHumanResponse rejects targets, content and trailing JSON. References
// grant no authority; the Host must revalidate the ledger before execution.
func DecodeHumanResponse(item Item) (HumanResponse, error) {
	decoder := json.NewDecoder(bytes.NewReader(item.Payload))
	decoder.DisallowUnknownFields()
	var response HumanResponse
	if err := decoder.Decode(&response); err != nil || item.MessageCount != 0 {
		return HumanResponse{}, ErrInvalid
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return HumanResponse{}, ErrInvalid
	}
	if err := response.Validate(); err != nil {
		return HumanResponse{}, err
	}
	return response, nil
}

// HumanResponseAdmission is assembled by the Host after the answer ledger and
// provider receipt have been accepted in the same transaction.
type HumanResponseAdmission struct {
	Scope       Scope
	ReceiptID   string
	PrincipalID string
	Response    HumanResponse
}

// AdmitHumanResponseTx atomically writes the receipt consumption and a fresh,
// single-item job in the caller's transaction. It does not commit, create a
// receipt, invent an IM message or require a Task for a foreground question.
// The caller owns question permissions and current goal-version validation.
func (s *Store) AdmitHumanResponseTx(ctx context.Context, tx pgx.Tx, a HumanResponseAdmission) (Job, Consumption, error) {
	if tx == nil || !validScope(a.Scope) || !validID(a.ReceiptID) || !validID(a.PrincipalID) || a.Response.Validate() != nil {
		return Job{}, Consumption{}, ErrInvalid
	}
	if err := lockScope(ctx, tx, a.Scope); err != nil {
		return Job{}, Consumption{}, err
	}
	args := append(scopeArgs(a.Scope), a.ReceiptID, a.PrincipalID)
	var receiptState, receiptReason, receiptRoute string
	if err := tx.QueryRow(ctx, `SELECT state,reason,route FROM scene_event_receipt WHERE `+scopeWhere+` AND id=$5::uuid AND principal_id=$6::uuid FOR SHARE`, args...).Scan(&receiptState, &receiptReason, &receiptRoute); err != nil {
		return Job{}, Consumption{}, mapError(err)
	}
	if receiptState != eventrouter.Ready || receiptRoute != eventrouter.Unified || receiptReason != "" {
		return Job{}, Consumption{}, ErrInvalid
	}
	payload, err := json.Marshal(a.Response)
	if err != nil {
		return Job{}, Consumption{}, err
	}
	var c Consumption
	var existingPayload json.RawMessage
	var principal, revision string
	err = tx.QueryRow(ctx, `SELECT receipt_id::text,owner_loop,COALESCE(job_id::text,''),state,reason,payload,principal_id::text,config_revision FROM employee_event_consumption WHERE `+scopeWhere+` AND receipt_id=$5::uuid`, args[:5]...).Scan(&c.ReceiptID, &c.Owner, &c.JobID, &c.State, &c.Reason, &existingPayload, &principal, &revision)
	if err == nil {
		equal, err := jsonEqual(ctx, tx, existingPayload, payload)
		if err != nil {
			return Job{}, Consumption{}, err
		}
		if !equal || c.Owner != Employee || c.JobID == "" || principal != a.PrincipalID || revision != HumanResponsePayloadSchema {
			return Job{}, Consumption{}, ErrConflict
		}
		job, err := scanJob(tx.QueryRow(ctx, `SELECT `+jobColumns+` FROM employee_scene_job WHERE `+scopeWhere+` AND id=$5::uuid`, append(scopeArgs(a.Scope), c.JobID)...))
		if err != nil {
			return Job{}, Consumption{}, err
		}
		if job.Kind != KindHumanResponse || job.PrincipalID != a.PrincipalID || len(job.Items) != 1 {
			return Job{}, Consumption{}, ErrConflict
		}
		stored, err := DecodeHumanResponse(job.Items[0])
		if err != nil || stored != a.Response || job.Items[0].ReceiptID != a.ReceiptID || job.Items[0].PrincipalID != a.PrincipalID {
			return Job{}, Consumption{}, ErrConflict
		}
		return job, c, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Job{}, Consumption{}, err
	}
	item := Item{ReceiptID: a.ReceiptID, PrincipalID: a.PrincipalID, Payload: payload, MessageCount: 0}
	items, err := json.Marshal([]Item{item})
	if err != nil {
		return Job{}, Consumption{}, err
	}
	job, err := scanJob(tx.QueryRow(ctx, `INSERT INTO employee_scene_job(workspace_id,agent_id,tenant_org_id,scene_id,principal_id,items,message_count,kind) VALUES($1::uuid,$2::uuid,$3,$4::uuid,$5::uuid,$6::jsonb,0,'human_response') RETURNING `+jobColumns, append(scopeArgs(a.Scope), a.PrincipalID, items)...))
	if err != nil {
		return Job{}, Consumption{}, err
	}
	c = Consumption{ReceiptID: a.ReceiptID, Owner: Employee, JobID: job.ID, State: "queued"}
	_, err = tx.Exec(ctx, `INSERT INTO employee_event_consumption(workspace_id,agent_id,tenant_org_id,scene_id,receipt_id,owner_loop,config_revision,principal_id,payload,job_id,state) VALUES($1::uuid,$2::uuid,$3,$4::uuid,$5::uuid,'employee',$6,$7::uuid,$8::jsonb,$9::uuid,'queued')`, append(scopeArgs(a.Scope), a.ReceiptID, HumanResponsePayloadSchema, a.PrincipalID, payload, c.JobID)...)
	if err != nil {
		return Job{}, Consumption{}, err
	}
	return job, c, nil
}
