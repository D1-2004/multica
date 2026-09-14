package service

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/dshhost"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const dshInputDomain = "multica-dsh-native-input-v1\n"

// The input lane cannot delay periodic access checks. Its result only attests
// durable platform admission; the daemon owns task-context binding and execution.
func (b *dshNativeAuthorityBridge) answerInput(ctx context.Context, request dshAuthorityRequest, host dshhost.Host, manager dshhost.NativeAccessManager, authority string, submit DSHNativePromptSubmit) dshAuthorityPacket {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	result := map[string]any{"status": http.StatusServiceUnavailable, "error": "DSH native admission is unconfirmed; retry the same request"}
	finish := func() dshAuthorityPacket {
		raw, _ := json.Marshal(map[string]any{"authority": authority, "id": request.ID, "result": result})
		return dshAuthorityPacket{Payload: base64.StdEncoding.EncodeToString(raw), Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(b.key, append([]byte(dshInputDomain), raw...)))}
	}
	authCtx, authCancel := context.WithTimeout(ctx, 4*time.Second)
	access, err := manager.Authorize(authCtx, request.Token, host)
	authCancel()
	if err != nil {
		result = map[string]any{"status": http.StatusForbidden, "error": "DSH native invocation is not authorized"}
		return finish()
	}
	prompt, err := protocol.DecodeDSHNativePrompt(request.Prompt)
	if err != nil || request.Exchange {
		result = map[string]any{"status": http.StatusBadRequest, "error": "Invalid DSH native prompt"}
		return finish()
	}
	if submit == nil {
		return finish()
	}
	input := DSHNativeChatInput{SessionID: prompt.SessionID, RequestID: uuid.MustParse(prompt.RequestID), Workdir: dshhost.MountPath + "/workspaces/" + prompt.SessionID, Prompt: prompt}
	receipt, err := submit(ctx, access, input, prompt.DisplayText())
	if err != nil {
		switch {
		case errors.Is(err, dshhost.ErrNativeAccessDenied):
			result = map[string]any{"status": http.StatusForbidden, "error": "DSH native invocation is not authorized"}
		case errors.Is(err, ErrDSHNativeInput):
			result = map[string]any{"status": http.StatusBadRequest, "error": "Invalid DSH native prompt"}
		case errors.Is(err, ErrDSHNativeSteerBusy):
			result = map[string]any{"status": http.StatusConflict, "error": "Steering the active task is not available yet"}
		case errors.Is(err, dshhost.ErrChanged), errors.Is(err, ErrChatSessionArchived):
			result = map[string]any{"status": http.StatusConflict, "error": "DSH native request conflicts with an existing admission"}
		}
		return finish()
	}
	if receipt.SessionID != prompt.SessionID || receipt.RequestID.String() != prompt.RequestID || !receipt.ChatSessionID.Valid || !receipt.TaskID.Valid || !receipt.MessageID.Valid || receipt.ChatSessionID.Bytes == [16]byte{} || receipt.TaskID.Bytes == [16]byte{} || receipt.MessageID.Bytes == [16]byte{} {
		return finish()
	}
	status := http.StatusCreated
	if receipt.Replayed {
		status = http.StatusOK
	}
	result = map[string]any{"status": status, "receipt": receipt}
	return finish()
}
