package agentmessagerouter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/dwsclient"
	"github.com/multica-ai/multica/server/internal/integrations/agentidentityhsf"
)

// DWSDelivery is the immutable, credential-free Router callback delivery plan.
// It is absent on callbacks accepted by an older Router that performed the send.
type DWSDelivery struct {
	IdempotencyKey          string `json:"idempotencyKey"`
	AgentID                 string `json:"agentId"`
	Environment             string `json:"environment"`
	SenderUID               string `json:"senderUid"`
	SenderOrgID             string `json:"senderOrgId"`
	OpenConversationID      string `json:"openConversationId"`
	RecipientOpenDingTalkID string `json:"recipientOpenDingTalkId"`
	AtOpenDingTalkID        string `json:"atOpenDingTalkId"`
	SourceOpenMessageID     string `json:"sourceOpenMessageId,omitempty"`
	Title                   string `json:"title"`
	Text                    string `json:"text"`
	ErrorCode               string `json:"errorCode"`
}

type dwsDeliveryState struct {
	Delivery           DWSDelivery `json:"delivery"`
	StartedAt          time.Time   `json:"startedAt"`
	Status             string      `json:"status"`
	OpenTaskID         string      `json:"openTaskId,omitempty"`
	OpenMessageID      string      `json:"openMessageId,omitempty"`
	OpenConversationID string      `json:"openConversationId,omitempty"`
}

type dwsDeliveryPermanentError struct{ code string }

func (e *dwsDeliveryPermanentError) Error() string { return "DWS reply delivery: " + e.code }

func validateDWSDelivery(d *DWSDelivery, agentID string) error {
	if d == nil {
		return nil
	}
	if d.ErrorCode != "" {
		return &dwsDeliveryPermanentError{dwsclient.SafeCode(d.ErrorCode)}
	}
	if d.AgentID != agentID || !strings.HasPrefix(d.IdempotencyKey, "router-reply:") ||
		len(d.IdempotencyKey) > 256 || d.OpenConversationID == "" ||
		d.Text == "" && d.AtOpenDingTalkID == "" ||
		d.Environment != "staging" && d.Environment != "production" ||
		d.RecipientOpenDingTalkID != "" && d.AtOpenDingTalkID != "" {
		return &dwsDeliveryPermanentError{"invalid_delivery_target"}
	}
	for _, id := range []string{d.SenderUID, d.SenderOrgID} {
		n, err := strconv.ParseUint(id, 10, 64)
		if err != nil || n == 0 {
			return &dwsDeliveryPermanentError{"invalid_sender_identity"}
		}
	}
	return nil
}

type replyReceipt struct{ OpenTaskID, OpenMessageID, OpenConversationID, SendStatus string }

type DWSReplySession interface {
	Send(context.Context, dwsclient.SendRequest) (replyReceipt, error)
	Status(context.Context, string) (replyReceipt, error)
	Close()
}

type DWSReplySender interface {
	Open(context.Context, DWSDelivery) (DWSReplySession, error)
}

type dwsContextIssuer interface {
	CreateContext(context.Context, agentidentityhsf.CreateContextRequest) (agentidentityhsf.CreateContextResult, error)
}

type DWSReplySenderConfig struct {
	AgentIdentity dwsContextIssuer
	Redeemer      dwsclient.Redeemer
	CLIPath       string
	ClientSecret  string
}

type dwsReplySender struct{ config DWSReplySenderConfig }

func NewDWSReplySender(config DWSReplySenderConfig) DWSReplySender {
	return &dwsReplySender{config: config}
}

func (s *dwsReplySender) Open(ctx context.Context, d DWSDelivery) (DWSReplySession, error) {
	if s.config.AgentIdentity == nil {
		return nil, errors.New("DWS reply identity issuer unavailable")
	}
	mint := func(ctx context.Context) (dwsclient.Credential, error) {
		requestID := "dws-reply:" + uuid.NewString()
		identity, err := s.config.AgentIdentity.CreateContext(ctx, agentidentityhsf.CreateContextRequest{
			RequestID: requestID, TaskID: d.IdempotencyKey, AgentID: d.AgentID,
			RuntimeType: "SERVER", RuntimeID: requestID, Reason: "Multica Router callback DWS delivery",
			UID: d.SenderUID, OrgID: d.SenderOrgID, TTLSeconds: 120,
			Source: map[string]string{"app": "dt-fde-multica", "identity_source": "router_callback_reply"},
		})
		if err != nil {
			return dwsclient.Credential{}, errors.New("DWS reply identity issuance failed")
		}
		credential, err := s.config.Redeemer.Redeem(ctx, identity.ContextToken)
		if err != nil {
			return dwsclient.Credential{}, err
		}
		if credential.UID != d.SenderUID {
			return dwsclient.Credential{}, &dwsDeliveryPermanentError{"sender_identity_mismatch"}
		}
		return credential, nil
	}
	cli := dwsclient.CLI{Path: s.config.CLIPath, ClientSecret: s.config.ClientSecret, Environment: d.Environment}
	// The SDK transport reuses the sender's shared token and mints only
	// without one; the dws CLI exchanges a credential for this delivery.
	if dir, cleanup, ok, err := (dwsclient.Shared{CLI: cli}).Open(ctx,
		dwsclient.Identity{AgentID: d.AgentID, UID: d.SenderUID, OrgID: d.SenderOrgID}, mint); ok {
		if err != nil {
			return nil, err
		}
		return &dwsReplySession{cli: cli, dir: dir, cleanup: cleanup}, nil
	}
	credential, err := mint(ctx)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "multica-dws-reply-")
	if err != nil {
		return nil, errors.New("create DWS reply config directory")
	}
	if err = cli.Exchange(ctx, dir, credential); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	return &dwsReplySession{cli: cli, dir: dir}, nil
}

type dwsReplySession struct {
	cli     dwsclient.CLI
	dir     string
	cleanup func()
}

func (s *dwsReplySession) Send(ctx context.Context, request dwsclient.SendRequest) (replyReceipt, error) {
	receipt, err := s.cli.Send(ctx, s.dir, request)
	return replyReceipt{OpenTaskID: receipt.OpenTaskID}, err
}
func (s *dwsReplySession) Status(ctx context.Context, taskID string) (replyReceipt, error) {
	receipt, err := s.cli.QuerySendStatus(ctx, s.dir, taskID)
	return replyReceipt{SendStatus: receipt.State, OpenMessageID: receipt.OpenMessageID, OpenConversationID: receipt.OpenConversationID}, err
}
func (s *dwsReplySession) Close() {
	if s.cleanup != nil {
		s.cleanup()
		return
	}
	_ = os.RemoveAll(s.dir)
}

// resumeDWSDelivery checkpoints acceptance before querying delivery. A lost callback
// response is replayed at Router. A lost send response can reuse the same key,
// but a duplicate rejection without the original receipt remains unconfirmed.
func (w *CompletionWorker) resumeDWSDelivery(ctx context.Context, raw []byte, save func([]byte) error) error {
	var state dwsDeliveryState
	if err := json.Unmarshal(raw, &state); err != nil {
		return &dwsDeliveryPermanentError{"invalid_delivery_state"}
	}
	d := state.Delivery
	if err := validateDWSDelivery(&d, d.AgentID); err != nil {
		return err
	}
	if state.Status == "delivered" {
		return nil
	}
	if state.Status == "confirmation_unavailable" {
		return &dwsDeliveryPermanentError{"send_confirmation_unavailable"}
	}
	if w.dwsSender == nil {
		return errors.New("DWS reply sender unavailable")
	}
	// DWS guarantees idempotency for 24h. Never resend an ambiguous request after that window.
	if state.OpenTaskID == "" && (state.StartedAt.IsZero() || time.Since(state.StartedAt) >= 23*time.Hour) {
		return &dwsDeliveryPermanentError{"send_confirmation_expired"}
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	session, err := w.dwsSender.Open(ctx, d)
	if err != nil {
		return err
	}
	defer session.Close()
	persist := func() error {
		raw, err := json.Marshal(state)
		if err != nil {
			return err
		}
		return save(raw)
	}
	if state.OpenTaskID == "" {
		content := d.Text
		if mention := dwsclient.MentionToken(d.AtOpenDingTalkID); mention != "" && !strings.Contains(content, mention) {
			content = mention + " " + content
		}
		conversationID := d.OpenConversationID
		recipientOpenDingTalkID := d.RecipientOpenDingTalkID
		if d.SourceOpenMessageID != "" {
			recipientOpenDingTalkID = ""
		} else if recipientOpenDingTalkID != "" {
			conversationID = ""
		}
		receipt, err := session.Send(ctx, dwsclient.SendRequest{
			SourceOpenMessageID: d.SourceOpenMessageID, SourceConversationID: d.OpenConversationID,
			ReplyToOpenMsgID: d.SourceOpenMessageID,
			ConversationID:   conversationID, RecipientOpenDingTalkID: recipientOpenDingTalkID,
			AtOpenDingTalkID: d.AtOpenDingTalkID, IdempotencyKey: d.IdempotencyKey, Title: d.Title, Content: content,
		})
		if err != nil {
			var operation *dwsclient.MessageOperationError
			if errors.As(err, &operation) && operation.DuplicateRequest {
				// The provider deduplicates but does not return the original
				// receipt. This proves neither delivery nor rejection. Keep
				// that uncertainty durable and never mint a new key/resend.
				state.Status = "confirmation_unavailable"
				if saveErr := persist(); saveErr != nil {
					return saveErr
				}
				return &dwsDeliveryPermanentError{"send_confirmation_unavailable"}
			}
			return err
		}
		if receipt.OpenTaskID == "" {
			return errors.New("DWS reply missing open task id")
		}
		state.OpenTaskID = receipt.OpenTaskID
		state.Status = "accepted"
		if err := persist(); err != nil {
			return err
		}
	}
	receipt, err := session.Status(ctx, state.OpenTaskID)
	if err != nil {
		return err
	}
	switch receipt.SendStatus {
	case "delivered":
		if receipt.OpenMessageID == "" || receipt.OpenConversationID == "" {
			return errors.New("DWS reply success missing message reference")
		}
		if receipt.OpenConversationID != d.OpenConversationID {
			return &dwsDeliveryPermanentError{"delivered_conversation_mismatch"}
		}
		state.Status = "delivered"
		state.OpenMessageID = receipt.OpenMessageID
		state.OpenConversationID = receipt.OpenConversationID
		if err := persist(); err != nil {
			return err
		}
		slog.Info("DWS reply delivered", "event", "multica_dws_reply_delivered",
			"delivery_key", d.IdempotencyKey, "agent_id", d.AgentID, "environment", d.Environment,
			"sender_uid", d.SenderUID, "open_task_id", state.OpenTaskID, "open_message_id", state.OpenMessageID)
		return nil
	case "failed":
		return &dwsDeliveryPermanentError{"send_failed"}
	default:
		return fmt.Errorf("DWS reply awaiting delivery confirmation")
	}
}

func freezeDWSDelivery(d *DWSDelivery, agentID string) ([]byte, error) {
	if d == nil {
		return nil, nil
	}
	// Keep invalid target evidence durable too; validation prevents any outbound call.
	if d.AgentID != agentID {
		return nil, &dwsDeliveryPermanentError{"callback_agent_mismatch"}
	}
	return json.Marshal(dwsDeliveryState{Delivery: *d, Status: "pending", StartedAt: time.Now().UTC()})
}
