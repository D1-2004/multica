package handler

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/service/employeeloop"
)

// A human-response execution is proven by its actual typed job and tool
// journal; the original message job is only its unchanged authority anchor.
func employeeHumanExecutionProof(ctx context.Context, tx pgx.Tx, b *employeeExecutionBinding, job employeeentry.Job) (string, error) {
	binding, err := readEmployeeHumanBinding(ctx, tx, job)
	if err != nil {
		return "human_response_binding_revoked", nil
	}
	var key string
	err = tx.QueryRow(ctx, `SELECT source_key FROM employee_task_entry WHERE task_id=$1::uuid AND run_id=$2::uuid AND kind='run_started' AND source_namespace='employee_scene'`, b.TaskID, b.RunID).Scan(&key)
	if err != nil {
		return "human_response_run_source_missing", nil
	}
	if key != binding.Question.SourceReceiptID+"/"+binding.Response.ID+"/run" {
		return "human_response_run_source_mismatch", nil
	}
	var raw []byte
	if err = tx.QueryRow(ctx, `SELECT tool_journal FROM employee_scene_job WHERE id=$1::uuid`, job.ID).Scan(&raw); err != nil {
		return "", err
	}
	var journal map[string]struct {
		Input  employeeloop.ToolCall `json:"input"`
		Result employeeToolRecord    `json:"result"`
	}
	if json.Unmarshal(raw, &journal) != nil {
		return "human_response_journal_invalid", nil
	}
	count := 0
	for id, saved := range journal {
		if saved.Input.Name != "continue_question_work" || saved.Result.Result.Receipt != b.RunID {
			continue
		}
		if id != saved.Input.NativeToolCallID || saved.Result.Failure != "" || saved.Result.Result.Terminal == nil || saved.Result.Result.Terminal.Kind != employeeloop.Dispatched {
			return "human_response_effect_invalid", nil
		}
		var ids struct {
			TaskID  string `json:"task_id"`
			RunID   string `json:"run_id"`
			QueueID string `json:"queue_task_id"`
		}
		if json.Unmarshal([]byte(saved.Result.Result.Content), &ids) != nil || ids.TaskID != b.TaskID || ids.RunID != b.RunID || ids.QueueID != b.QueueTaskID {
			return "human_response_effect_target_mismatch", nil
		}
		count++
	}
	if count != 1 {
		return "human_response_effect_missing", nil
	}
	return "", nil
}
