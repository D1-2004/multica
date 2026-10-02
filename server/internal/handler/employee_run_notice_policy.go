package handler

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/service/dingtalkresponse"
)

type employeeFileCheck struct{ File, Unconfirmed bool }
type employeeFileChecks map[string]employeeFileCheck

func employeeFileCheckKey(in dingtalkresponse.ActionInput, id string) string {
	return strings.Join([]string{in.WorkspaceID, in.AgentID, in.TaskID, in.DWSUID, in.DWSOrgID, in.ConversationID, id}, "\x00")
}

// employeeNoticeDeliveryDecision reads durable receipts only. Missing file
// classifications are returned to the caller to resolve outside its transaction.
func (h *Handler) employeeNoticeDeliveryDecision(ctx context.Context, tx pgx.Tx, b employeeNoticeBinding, in dingtalkresponse.ActionInput, checked employeeFileChecks) (string, []string, error) {
	if b.ResultState != "succeeded" || b.CompletionNotice.Mode != employeetask.CompletionNoticeIfNotDelivered {
		return "notify", nil, nil
	}
	in.TaskID = b.QueueID
	delivery, err := h.DingTalkResponses.SandboxDeliveryInTx(ctx, tx, in)
	if err != nil {
		return "", nil, err
	}
	var missing []string
	unconfirmed := false
	for _, id := range delivery.MessageIDs {
		check, known := checked[employeeFileCheckKey(in, id)]
		if known && check.File {
			return "suppress", nil, nil
		}
		unconfirmed = unconfirmed || check.Unconfirmed
		if !known {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		return "verify", missing, nil
	}
	// A delivered text acknowledgement must not hide the still-pending file.
	// A verified file above may satisfy duplicate SDK/shim receipts immediately.
	if delivery.HasPending {
		return "wait", nil, nil
	}
	if delivery.HasUnknown || unconfirmed {
		return "unconfirmed", nil, nil
	}
	if delivery.HasFailure {
		return "delivery_failed", nil, nil
	}

	return "notify", nil, nil
}
func (h *Handler) verifyEmployeeNoticeFiles(ctx context.Context, in dingtalkresponse.ActionInput, queueID string, ids []string, checked employeeFileChecks) error {
	in.TaskID = queueID
	// One unusable provider read becomes an explicit unconfirmed notice instead
	// of blocking every later completed Run. Parent cancellation still aborts.
	budget := 2 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		// The production notice batch has a ten-second parent deadline. Preserve
		// one second for authority/receipt revalidation and the terminal commit.
		remaining := time.Until(deadline) - time.Second
		if remaining < budget {
			budget = remaining
		}
	}
	if budget <= 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		for _, id := range ids {
			checked[employeeFileCheckKey(in, id)] = employeeFileCheck{Unconfirmed: true}
		}
		return nil
	}
	verifyCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return err
		}
		if verifyCtx.Err() != nil {
			checked[employeeFileCheckKey(in, id)] = employeeFileCheck{Unconfirmed: true}
			continue
		}
		file, err := h.DingTalkResponses.VerifySandboxFile(verifyCtx, in, id)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		checked[employeeFileCheckKey(in, id)] = employeeFileCheck{File: err == nil && file, Unconfirmed: err != nil}
	}

	return nil
}
func employeeNoticeDeliveryBody(decision string) string {
	switch decision {
	case "unconfirmed":
		return "本次执行已结束，但文件是否送达尚未确认。"
	case "delivery_failed":
		return "本次执行已结束，但文件发送失败。"
	default:
		return ""
	}
}

func (h *Handler) employeeNoticeReplicasReady(ctx context.Context) error {
	if h.EmployeeSceneWorker == nil || h.EmployeeSceneWorker.ReplicaReady == nil {
		return errors.New("employee notice replica capability verification is unavailable")
	}
	return h.EmployeeSceneWorker.ReplicaReady(ctx)
}

// refreshEmployeeNoticeBody changes only a never-submitted intent. Its stable
// id hashes scope/request/kind, not text; response_action has no separate body
// hash. Both persisted text copies commit together and the worker must reload.
func refreshEmployeeNoticeBody(ctx context.Context, tx pgx.Tx, in dingtalkresponse.ActionInput, runID, oldBody, newBody string) error {
	tag, err := tx.Exec(ctx, `UPDATE response_action SET input=jsonb_set(input,'{text}',to_jsonb($2::text)),updated_at=now()
 WHERE id=$1 AND state='pending' AND provider_task_id='' AND input->>'text'=$3
 AND input->>'employee_run_notice_id'=$4 AND (lease_until IS NULL OR lease_until>now())`, in.ActionID, newBody, oldBody, runID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("employee notice changed or was already submitted")
	}
	_, err = tx.Exec(ctx, `UPDATE employee_run_notice SET body=$2,updated_at=now() WHERE run_id=$1::uuid AND action_id=$3 AND state='enqueued'`, runID, newBody, in.ActionID)
	return err
}
