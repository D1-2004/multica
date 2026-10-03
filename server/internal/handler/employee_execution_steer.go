package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/eventrouter"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/service/employeeloop"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// The accepted Run input boundary, not a mutable queue flag, identifies a steer.
// It covers both a successor and a correction merged before the existing claim.
func employeeExecutionSteerInput(ctx context.Context, tx pgx.Tx, b *employeeExecutionBinding) (employeetask.Entry, bool, error) {
	var e employeetask.Entry
	err := tx.QueryRow(ctx, `SELECT e.seq,e.source_namespace,e.source_key,e.actor_ref,e.body,e.goal_revision,COALESCE(e.run_id::text,'') FROM employee_task_entry e JOIN employee_task_run r ON r.task_id=e.task_id AND r.workspace_id=e.workspace_id AND r.agent_id=e.agent_id AND r.tenant_org_id=e.tenant_org_id AND r.input_seq=e.seq WHERE r.id=$1::uuid AND r.queue_task_id=$2::uuid AND e.workspace_id=$3::uuid AND e.agent_id=$4::uuid AND e.tenant_org_id=$5 AND e.task_id=$6::uuid AND e.kind='steer'`, b.RunID, b.QueueTaskID, b.Scope.WorkspaceID, b.Scope.AgentID, b.Scope.TenantOrgID, b.TaskID).Scan(&e.Seq, &e.Source.Namespace, &e.Source.Key, &e.ActorRef, &e.Body, &e.GoalRevision, &e.RunID)
	if errors.Is(err, pgx.ErrNoRows) {
		return e, false, nil
	}
	return e, err == nil, err
}

func employeeExecutionSteerProof(ctx context.Context, tx pgx.Tx, b *employeeExecutionBinding, e employeetask.Entry) (string, error) {
	prefix := b.SourceReceiptID + "/"
	if e.Source.Namespace != "employee_scene" || e.ActorRef != b.Requester || e.ActorRef != b.Source.RequesterRef || e.GoalRevision != b.GoalRevision || !strings.HasPrefix(e.Source.Key, prefix) || !strings.HasSuffix(e.Source.Key, "/steer") {
		return "source_steer_mismatch", nil
	}
	callID := strings.TrimSuffix(strings.TrimPrefix(e.Source.Key, prefix), "/steer")
	if callID == "" {
		return "source_steer_missing", nil
	}
	var key, namespace, queue string
	var revision, startedSeq int64
	err := tx.QueryRow(ctx, `SELECT source_namespace,source_key,payload->>'queue_task_id',goal_revision,seq FROM employee_task_entry WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND task_id=$4::uuid AND run_id=$5::uuid AND kind='run_started'`, b.Scope.WorkspaceID, b.Scope.AgentID, b.Scope.TenantOrgID, b.TaskID, b.RunID).Scan(&namespace, &key, &queue, &revision, &startedSeq)
	if errors.Is(err, pgx.ErrNoRows) {
		return "source_steer_missing", nil
	}
	if err != nil {
		return "", err
	}
	if queue != b.QueueTaskID || namespace != "employee_scene" || revision != b.GoalRevision {
		return "source_steer_mismatch", nil
	}
	if e.RunID == "" {
		if key != e.Source.Key+"/run" || startedSeq <= e.Seq {
			return "source_steer_mismatch", nil
		}
	} else if e.RunID != b.RunID || startedSeq >= e.Seq {
		return "source_steer_mismatch", nil
	}
	var raw []byte
	if err = tx.QueryRow(ctx, `SELECT tool_journal->$2 FROM employee_scene_job WHERE id=$1::uuid`, b.JobID, callID).Scan(&raw); err != nil {
		return "", err
	}
	var saved struct {
		Input  employeeloop.ToolCall `json:"input"`
		Result employeeToolRecord    `json:"result"`
	}
	if json.Unmarshal(raw, &saved) != nil || saved.Input.Name != "steer_task" || saved.Input.NativeToolCallID != callID || saved.Input.Arguments["source_ref"] != b.SourceRef || saved.Result.Failure != "" || saved.Result.Result.Receipt != b.RunID || saved.Result.Result.Terminal == nil || saved.Result.Result.Terminal.Kind != employeeloop.Dispatched {
		return "source_steer_mismatch", nil
	}
	correction, ok := saved.Input.Arguments["correction"].(string)
	if !ok || strings.TrimSpace(correction) != e.Body {
		return "source_steer_mismatch", nil
	}
	var ids struct {
		Task    string `json:"task_id"`
		Run     string `json:"run_id"`
		Queue   string `json:"queue_task_id"`
		Outcome string `json:"outcome"`
	}
	if json.Unmarshal([]byte(saved.Result.Result.Content), &ids) != nil || ids.Task != b.TaskID || ids.Run != b.RunID || ids.Queue != b.QueueTaskID {
		return "source_steer_mismatch", nil
	}
	if (e.RunID != "" && ids.Outcome != "merged") || (e.RunID == "" && ids.Outcome != "continued" && ids.Outcome != "interrupted") {
		return "source_steer_mismatch", nil
	}
	// Preserve the original Direct identity. Only the separately proved accepted
	// source and the deterministic correction rendering may differ from that input.
	direct, valid := service.ParseDirectTaskContext(b.Queue)
	var fields struct {
		Input             json.RawMessage `json:"employee_direct_input"`
		Base              string          `json:"direct_steer_base_prompt"`
		CorrectionVersion int             `json:"direct_steer_corrections_version"`
	}
	if !valid || json.Unmarshal(b.Queue.Context, &fields) != nil {
		return "execution_input_mismatch", nil
	}
	frozenQueue := b.Queue
	frozenQueue.Context = fields.Input
	frozen, valid := service.ParseDirectTaskContext(frozenQueue)
	var original employeeExecutionContext
	if !valid || json.Unmarshal(fields.Input, &original) != nil || original.Owner != "employee" || original.Scene.SceneID != b.SceneID || fields.Base != frozen.Prompt {
		return "execution_input_mismatch", nil
	}
	expected := frozen
	expected.Prompt = direct.Prompt
	if expected != direct {
		return "execution_input_mismatch", nil
	}
	var corrections []employeetask.SteerCorrection
	if fields.CorrectionVersion == 1 {
		scope := employeetask.Scope{WorkspaceID: b.Scope.WorkspaceID, AgentID: b.Scope.AgentID, TenantOrgID: b.Scope.TenantOrgID, Kind: employeetask.ScopeScene}
		scope.Scene.SceneID = b.SceneID
		entries, readErr := employeetask.NewStore(tx).CorrectionsThrough(ctx, scope, b.TaskID, e.Seq)
		if errors.Is(readErr, employeetask.ErrCorrectionBounds) {
			return "execution_input_mismatch", nil
		}
		if readErr != nil {
			return "", readErr
		}
		for _, entry := range entries {
			corrections = append(corrections, employeetask.SteerCorrection{Ref: fmt.Sprintf("employee_task_entry:%s/%d", b.TaskID, entry.Seq), ActorRef: entry.ActorRef, Body: entry.Body})
		}
	} else if fields.CorrectionVersion == 0 {
		rows, err := tx.Query(ctx, `SELECT seq,actor_ref,body FROM (SELECT seq,actor_ref,body FROM employee_task_entry WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND task_id=$4::uuid AND kind='steer' AND seq<=$5 ORDER BY seq DESC LIMIT 20) corrections ORDER BY seq`, b.Scope.WorkspaceID, b.Scope.AgentID, b.Scope.TenantOrgID, b.TaskID, e.Seq)
		if err != nil {
			return "", err
		}
		for rows.Next() {
			var seq int64
			var actor, body string
			if err = rows.Scan(&seq, &actor, &body); err != nil {
				break
			}
			corrections = append(corrections, employeetask.SteerCorrection{Ref: fmt.Sprintf("employee_task_entry:%s/%d", b.TaskID, seq), ActorRef: actor, Body: body})
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return "", err
		}
	} else {
		return "execution_input_mismatch", nil
	}
	if employeetask.WithCorrections(fields.Base, corrections) != direct.Prompt {
		return "execution_input_mismatch", nil
	}
	b.Steered = true
	return "", nil
}

// Steer preserves the delivery anchor on the original frozen request. Its
// effect attribution instead comes from the accepted ledger source and the
// corresponding completed Employee job. Neither address substitutes for the
// other's proof, and an uncommitted effect checkpoint must remain retryable.
func employeeExecutionSteerSource(ctx context.Context, tx pgx.Tx, b *employeeExecutionBinding, e employeetask.Entry) (string, error) {
	if e.Source.Namespace != "employee_scene" || !strings.HasSuffix(e.Source.Key, "/steer") {
		return "source_steer_missing", nil
	}
	parts := strings.SplitN(e.Source.Key, "/", 2)
	if len(parts) != 2 {
		return "source_steer_missing", nil
	}
	receipt, err := util.ParseUUID(parts[0])
	if err != nil {
		return "source_steer_missing", nil
	}
	callID := strings.TrimSuffix(parts[1], "/steer")
	if callID == "" {
		return "source_steer_missing", nil
	}
	var jobID, principal, consumed string
	var payload []byte
	err = tx.QueryRow(ctx, `SELECT job_id::text,principal_id::text,state,payload FROM employee_event_consumption WHERE receipt_id=$1 AND workspace_id=$2::uuid AND agent_id=$3::uuid AND tenant_org_id=$4 AND scene_id=$5::uuid AND owner_loop='employee'`, receipt, b.Scope.WorkspaceID, b.Scope.AgentID, b.Scope.TenantOrgID, b.SceneID).Scan(&jobID, &principal, &consumed, &payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return "source_steer_missing", nil
	}
	if err != nil {
		return "", err
	}
	var state string
	var raw []byte
	err = tx.QueryRow(ctx, `SELECT state,tool_journal->$6 FROM employee_scene_job WHERE id=$1::uuid AND workspace_id=$2::uuid AND agent_id=$3::uuid AND tenant_org_id=$4 AND scene_id=$5::uuid AND principal_id=$7::uuid`, jobID, b.Scope.WorkspaceID, b.Scope.AgentID, b.Scope.TenantOrgID, b.SceneID, callID, principal).Scan(&state, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return "source_steer_missing", nil
	}
	if err != nil {
		return "", err
	}
	if state != "completed" {
		return "source_job_pending", nil
	}
	if consumed != "completed" {
		return "source_consumption_missing", nil
	}
	var saved struct {
		Input employeeloop.ToolCall `json:"input"`
	}
	if json.Unmarshal(raw, &saved) != nil || saved.Input.Name != "steer_task" || saved.Input.NativeToolCallID != callID {
		return "source_steer_missing", nil
	}
	ref, ok := saved.Input.Arguments["source_ref"].(string)
	if !ok || !strings.HasPrefix(ref, parts[0]+"/") {
		return "source_steer_mismatch", nil
	}
	var env employeeDispatchEnvelope
	if json.Unmarshal(payload, &env) != nil || env.PrincipalID != principal || env.Command.EventReceiptID != parts[0] || dispatchSceneID(env.Command) != b.SceneID || dispatchRecordedOrg(env.Command) != b.Scope.TenantOrgID {
		return "source_steer_mismatch", nil
	}
	var selected employeeSourceMessage
	matches := 0
	for _, source := range employeeSourceMessages(employeeentry.Item{ReceiptID: parts[0]}, env) {
		if source.SourceRef == ref {
			selected = source
			matches++
		}
	}
	if matches != 1 || selected.RequesterRef != b.Requester || selected.RequesterRef != e.ActorRef {
		return "source_steer_mismatch", nil
	}
	actor, err := util.ParseUUID(principal)
	if err != nil {
		return "source_principal_invalid", nil
	}
	var original db.SceneEventReceipt
	err = tx.QueryRow(ctx, `SELECT principal_id,scene_id,route,state,reason,config_version FROM scene_event_receipt WHERE id=$1 AND workspace_id=$2::uuid AND agent_id=$3::uuid AND tenant_org_id=$4`, receipt, b.Scope.WorkspaceID, b.Scope.AgentID, b.Scope.TenantOrgID).Scan(&original.PrincipalID, &original.SceneID, &original.Route, &original.State, &original.Reason, &original.ConfigVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return "source_receipt_missing", nil
	}
	if err != nil {
		return "", err
	}
	resolved := original.Route == eventrouter.Unified && original.State == eventrouter.Ready || original.Route == eventrouter.Legacy && original.State == eventrouter.Legacy
	if original.PrincipalID != actor || uuidToString(original.SceneID) != b.SceneID || original.Reason != "" || !resolved {
		return "source_receipt_mismatch", nil
	}
	b.JobID, b.SourceRef, b.SourceReceiptID = jobID, ref, parts[0]
	b.Source, b.PrincipalID, b.Original = selected, actor, original
	return "", nil
}
