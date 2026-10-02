package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type steerCompletionCallback struct {
	URL    string `json:"url"`
	Target string `json:"target"`
}

// Keep every accepted dispatch callback when physical correction runs coalesce.
// Latest input remains the root; older inputs use the existing nullable-root
// outbox contract, so they neither run twice nor lose their terminal receipt.
func mergeSteerCorrectionContext(previous, correction []byte) ([]byte, error) {
	old, next := map[string]json.RawMessage{}, map[string]json.RawMessage{}
	if len(previous) > 0 {
		if err := json.Unmarshal(previous, &old); err != nil {
			return nil, err
		}
	}
	if len(correction) > 0 {
		if err := json.Unmarshal(correction, &next); err != nil {
			return nil, err
		}
	}
	if old == nil {
		old = map[string]json.RawMessage{}
	}
	var callbacks []steerCompletionCallback
	for _, private := range []map[string]json.RawMessage{old, next} {
		var root steerCompletionCallback
		_ = json.Unmarshal(private["completion_callback"], &root)
		if root.URL != "" {
			callbacks = append(callbacks, root)
		}
		var extras []steerCompletionCallback
		_ = json.Unmarshal(private["steer_completion_callbacks"], &extras)
		callbacks = append(callbacks, extras...)
	}
	for k, v := range next {
		old[k] = v
	}
	var primary steerCompletionCallback
	_ = json.Unmarshal(old["completion_callback"], &primary)
	unique := []steerCompletionCallback{}
	seen := map[steerCompletionCallback]bool{primary: true}
	for _, callback := range callbacks {
		if callback.URL != "" && !seen[callback] {
			unique = append(unique, callback)
			seen[callback] = true
		}
	}
	old["steer_completion_callbacks"], _ = json.Marshal(unique)
	return json.Marshal(old)
}

func enqueueSteerCallbackCompletions(ctx context.Context, q *db.Queries, task db.AgentTaskQueue, status string, result []byte, errMessage, failureReason string) (bool, error) {
	var private struct {
		Callbacks []steerCompletionCallback `json:"steer_completion_callbacks"`
	}
	if len(task.Context) == 0 {
		return false, nil
	}
	if err := json.Unmarshal(task.Context, &private); err != nil {
		return false, err
	}
	queued := false
	for _, callback := range private.Callbacks {
		target := taskCompletionTarget{AgentID: task.AgentID, CallbackURL: callback.URL, TargetIdentity: callback.Target}
		completion := buildTaskCompletion(target, task, status, result, "", errMessage, failureReason)
		digest := sha256.Sum256([]byte(callback.URL + "\x00" + callback.Target))
		requestID := "multica-steer-terminal:" + hex.EncodeToString(digest[:])
		summary, err := json.Marshal(map[string]any{"task_id": task.ID, "reply_decision": completion.ReplyDecision})
		if err != nil {
			return false, err
		}
		if err = q.EnqueueSteerCallbackCompletion(ctx, db.EnqueueSteerCallbackCompletionParams{CallbackUrl: callback.URL, TargetIdentity: callback.Target, RequestID: requestID, AgentID: task.AgentID, SessionID: pgtype.Text{String: task.SessionID.String, Valid: task.SessionID.Valid}, ExecutionStatus: status, ResultMessage: completion.ResultMessage, ExecutionSummary: summary, Error: pgtype.Text{String: completion.Error, Valid: completion.Error != ""}, FailureReason: pgtype.Text{String: failureReason, Valid: failureReason != ""}}); err != nil {
			return false, fmt.Errorf("enqueue merged steer callback: %w", err)
		}
		queued = true
	}
	return queued, nil
}
