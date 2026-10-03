package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service/employeememory"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type employeeMemoryCommandResult struct {
	Reply string `json:"reply"`
}

func isEmployeeResetMemory(m DispatchMessage) bool {
	if m.Reaction != nil || m.ReferencedMessage != nil || len(m.Attachments) > 0 {
		return false
	}
	fields := strings.Fields(m.Text)
	if len(fields) > 0 && isInboundMentionToken(fields[0]) {
		fields = fields[1:]
	}
	return len(fields) == 1 && strings.EqualFold(fields[0], inboundResetMemoryCommand)
}

// memoryCommands consumes exact slash messages independently from model work.
// Each reset and its source journal share the same transaction; a retry can
// recover its reply without clearing any memory learned after that reset.
func (w *EmployeeSceneWorker) memoryCommands(ctx context.Context, job employeeentry.Job, envelopes []employeeDispatchEnvelope) ([]employeeDispatchEnvelope, map[string]string, string, error) {
	work := append([]employeeDispatchEnvelope(nil), envelopes...)
	replies := map[string]string{}
	firstWorkReceipt := ""
	for i, item := range job.Items {
		work[i].Command.Event.Data.Messages = nil
		for _, source := range employeeSourceMessages(item, envelopes[i]) {
			if !isEmployeeResetMemory(source.Message) {
				work[i].Command.Event.Data.Messages = append(work[i].Command.Event.Data.Messages, source.Message)
				if firstWorkReceipt == "" {
					firstWorkReceipt = item.ReceiptID
				}
				continue
			}
			encoded, err := json.Marshal(source)
			if err != nil {
				return nil, nil, "", err
			}
			sum := sha256.Sum256(encoded)
			key := "host:reset-memory:" + hex.EncodeToString(sum[:])
			raw, err := w.store.ExecuteTool(ctx, job, key, encoded, nil, func(tx pgx.Tx) (json.RawMessage, error) {
				result := employeeMemoryCommandResult{Reply: "无法确认这条指令的发送者，记忆没有清理。"}
				if source.RequesterRef != "" && source.SourceRef != "" {
					if w.handler.EmployeeMemory == nil {
						return nil, errors.New("employee memory is unavailable")
					}
					ws, agent := parseUUID(job.Scope.WorkspaceID), parseUUID(job.Scope.AgentID)
					ref := scene.Ref{SceneID: job.Scope.SceneID}
					if _, err := fencedScene(ctx, db.New(tx), &ref, scene.Owner{WorkspaceID: ws, AgentID: agent}, job.Scope.TenantOrgID); err != nil {
						return nil, err
					}
					permissionView := &Handler{Queries: db.New(tx)}
					if err := employeePrincipalAllowed(ctx, permissionView, job.Scope, envelopes[i].PrincipalID); err != nil {
						return nil, err
					}
					scope := employeememory.Scope{WorkspaceID: ws, AgentID: agent, TenantOrgID: job.Scope.TenantOrgID, Scene: ref, Kind: employeememory.ScopeScene}
					if _, err := w.handler.EmployeeMemory.ResetSceneTx(ctx, tx, scope, nil); err != nil {
						return nil, err
					}
					scope.Kind = employeememory.ScopePrivate
					scope.PrincipalID = source.RequesterRef
					if err := w.handler.EmployeeMemory.ResetPrivateTx(ctx, tx, scope); err != nil {
						return nil, err
					}
					result.Reply = "已清理本会话的 Employee 共享记忆，以及该指令发送者在这里的个人记忆。"
				}
				return json.Marshal(result)
			})
			if err != nil {
				return nil, nil, "", err
			}
			var result employeeMemoryCommandResult
			if err = json.Unmarshal(raw, &result); err != nil {
				return nil, nil, "", err
			}
			if replies[item.ReceiptID] == "" {
				replies[item.ReceiptID] = result.Reply
			} else if !strings.Contains(replies[item.ReceiptID], result.Reply) {
				replies[item.ReceiptID] += "\n" + result.Reply
			}
		}
	}
	return work, replies, firstWorkReceipt, nil
}
func employeeJobReplyID(job employeeentry.Job, saved employeeSavedOutcome, receiptID string) string {
	if len(saved.SourceReplies) == 0 {
		return job.ID
	}
	return uuid.NewSHA1(uuid.MustParse(job.ID), []byte("receipt:"+receiptID)).String()
}
