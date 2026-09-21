package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type dshNativeBindingReader interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// Only task-owned input may become a native prompt. The binding and Session
// mapping independently attest the scope; transcript summaries cannot execute.
func loadDSHNativeClaim(ctx context.Context, reader dshNativeBindingReader, task db.AgentTaskQueue, runtime db.AgentRuntime, backend service.SandboxBackendKind, workspaceID pgtype.UUID, capable bool, messages []db.ChatMessage) (*protocol.DSHNativePrompt, *claimBuildFailure) {
	var prompt *protocol.DSHNativePrompt
	invalid := func() (*protocol.DSHNativePrompt, *claimBuildFailure) {
		return nil, &claimBuildFailure{outcome: "error_dsh_native_input", status: http.StatusConflict, message: "native DSH task input or binding is invalid"}
	}
	for _, message := range messages {
		var source map[string]json.RawMessage
		if json.Unmarshal(message.SourcePayload, &source) != nil {
			continue
		}
		raw, found := source["dsh_native_prompt"]
		if !found {
			continue
		}
		var err error
		prompt, err = protocol.DecodeDSHNativePrompt(raw)
		if err != nil || len(messages) != 1 || !task.ID.Valid || task.ChatInputTaskID != task.ID ||
			!task.ChatSessionID.Valid || message.ChatSessionID != task.ChatSessionID || message.TaskID != task.ID ||
			message.Role != "user" || message.MessageKind != "message" || message.Content != prompt.DisplayText() {
			return invalid()
		}
	}
	if prompt == nil {
		return nil, nil
	}
	if runtime.Provider != "dsh" || !service.IsFCE2BRuntime(runtime) || backend != service.SandboxBackendAliyunFC ||
		!workspaceID.Valid || runtime.WorkspaceID != workspaceID || task.RuntimeID != runtime.ID {
		return invalid()
	}
	if !capable {
		return nil, &claimBuildFailure{outcome: "error_dsh_native_capability", status: http.StatusServiceUnavailable, message: "DSH Runtime must support dsh-native-prompt-v1 before receiving native input"}
	}
	if reader == nil {
		return nil, &claimBuildFailure{outcome: "error_dsh_native_binding_load", status: http.StatusServiceUnavailable, message: "native DSH binding is unavailable"}
	}
	var sessionID, requestID string
	err := reader.QueryRow(ctx, `SELECT b.session_id,b.request_id::text FROM dsh_task_binding b
 JOIN dsh_employee_session s ON s.workspace_id=b.workspace_id AND s.agent_id=b.agent_id AND s.session_id=b.session_id
 WHERE b.workspace_id=$1 AND b.agent_id=$2 AND b.task_id=$3 AND s.scope_kind='chat' AND s.scope_id=$4`,
		workspaceID, task.AgentID, task.ID, task.ChatSessionID).Scan(&sessionID, &requestID)
	if errors.Is(err, pgx.ErrNoRows) {
		return invalid()
	}
	if err != nil {
		return nil, &claimBuildFailure{outcome: "error_dsh_native_binding_load", status: http.StatusServiceUnavailable, message: "native DSH binding is unavailable"}
	}
	identity, identityErr := protocol.DSHNativeRequestIdentity(prompt.SessionID, prompt.RequestID)
	if identityErr != nil || sessionID != prompt.SessionID || requestID != identity.String() {
		return invalid()
	}
	return prompt, nil
}

// Keep native attachments out of generic callback text and duplicate claim
// fields. They are delivered only through the capability-gated typed payload.
func withoutDSHNativeSource(raw []byte) []byte {
	var source map[string]json.RawMessage
	if json.Unmarshal(raw, &source) != nil {
		return raw
	}
	if _, found := source["dsh_native_prompt"]; !found {
		return raw
	}
	delete(source, "dsh_native_prompt")
	if len(source) == 0 {
		return nil
	}
	encoded, err := json.Marshal(source)
	if err != nil {
		return nil
	}
	return encoded
}
