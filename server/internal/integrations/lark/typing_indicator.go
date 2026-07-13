package lark

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// typingEmoji is the Lark emoji_type used for the "processing" indicator.
// It renders as a small typing-animation badge on the message.
const typingEmoji = "Typing"

// typingIndicatorMaxAge is how old a message can be before we skip the
// typing indicator. This prevents stale reactions when a WebSocket
// reconnect replays old events. Aligned with OpenClaw's 2-minute bound.
const typingIndicatorMaxAge = 2 * time.Minute

// TypingIndicatorState holds the identifiers needed to remove a reaction.
// TypingIndicatorState is the persisted shape of one pending reaction. It
// lives in channel_typing_indicator.target (migration 168) rather than in
// process memory: the replica that clears a reaction is the one that serves
// the daemon's completion POST, not the lease-holding replica that ingested
// the message. ReactionID is only knowable from the Add response, so unlike
// DingTalk/Slack it cannot be rebuilt from the binding row.
type TypingIndicatorState struct {
	MessageID  string `json:"message_id"`
	ReactionID string `json:"reaction_id"`
}

// TypingIndicatorStore persists pending reactions across replicas. Separate
// from TypingIndicatorQueries so the lark store abstraction (which speaks lark
// types) stays untouched.
type TypingIndicatorStore interface {
	AddChannelTypingIndicator(ctx context.Context, arg db.AddChannelTypingIndicatorParams) error
	TakeChannelTypingIndicators(ctx context.Context, arg db.TakeChannelTypingIndicatorsParams) ([]db.ChannelTypingIndicator, error)
}

// TypingIndicatorQueries is the narrow DB surface the manager needs.
type TypingIndicatorQueries interface {
	GetLarkChatSessionBindingBySession(ctx context.Context, chatSessionID pgtype.UUID) (ChatSessionBinding, error)
	GetLarkInstallation(ctx context.Context, id pgtype.UUID) (Installation, error)
}

// TypingIndicatorManager owns the "processing" reaction lifecycle for
// inbound Lark messages. When a message is successfully ingested it adds
// a Typing reaction; when the agent eventually replies (or fails) it
// clears the reaction(s) for that chat session.
//
// The manager is safe for concurrent use. It tolerates missing or
// stale state gracefully: adding a reaction to a message that already
// has one simply appends another state entry; clearing a session with
// no tracked state is a no-op.
type TypingIndicatorManager struct {
	client      APIClient
	credentials CredentialsResolver
	queries     TypingIndicatorQueries
	log         *slog.Logger

	store TypingIndicatorStore
}

// SetStore wires the cross-replica store. Nil-safe: without it the manager adds
// reactions it can never clear, which is the pre-migration-168 behavior.
func (m *TypingIndicatorManager) SetStore(store TypingIndicatorStore) {
	if m != nil {
		m.store = store
	}
}

// NewTypingIndicatorManager constructs a manager. All dependencies must
// be non-nil; the manager panics on nil client / credentials / queries.
func NewTypingIndicatorManager(client APIClient, credentials CredentialsResolver, queries TypingIndicatorQueries, log *slog.Logger) *TypingIndicatorManager {
	if log == nil {
		log = slog.Default()
	}
	return &TypingIndicatorManager{
		client:      client,
		credentials: credentials,
		queries:     queries,
		log:         log,
	}
}

// Add sends a Typing reaction to the given message and records the state
// under the chat session. It is synchronous — the caller decides whether
// to run it in a detached goroutine. Errors are logged and swallowed.
//
// createTime is Lark's epoch-millisecond string (InboundMessage.CreateTime).
// Messages older than typingIndicatorMaxAge are silently skipped so that
// WebSocket replays and stale reconnects do not surface misleading "processing"
// badges on long-finished conversations.
func (m *TypingIndicatorManager) Add(ctx context.Context, inst Installation, chatSessionID pgtype.UUID, messageID string, createTime string) {
	if messageID == "" {
		return
	}
	if isMessageTooOld(createTime) {
		m.log.Debug("lark typing indicator: message too old, skipping",
			"chat_session_id", uuidString(chatSessionID),
			"message_id", messageID,
			"create_time", createTime,
		)
		return
	}
	creds, err := m.resolveCredentials(inst)
	if err != nil {
		m.log.Warn("lark typing indicator: failed to resolve credentials",
			"chat_session_id", uuidString(chatSessionID),
			"message_id", messageID,
			"err", err,
		)
		return
	}

	reactionID, err := m.client.AddMessageReaction(ctx, AddReactionParams{
		InstallationID: creds,
		MessageID:      messageID,
		EmojiType:      typingEmoji,
	})
	if err != nil {
		m.log.Warn("lark typing indicator: add reaction failed",
			"chat_session_id", uuidString(chatSessionID),
			"message_id", messageID,
			"err", err,
		)
		return
	}

	key := uuidString(chatSessionID)
	if m.store != nil {
		payload, jerr := json.Marshal(TypingIndicatorState{MessageID: messageID, ReactionID: reactionID})
		if jerr != nil {
			m.log.Warn("lark typing indicator: encode target failed",
				"chat_session_id", key, "err", jerr)
		} else if serr := m.store.AddChannelTypingIndicator(ctx, db.AddChannelTypingIndicatorParams{
			ChatSessionID:  chatSessionID,
			ChannelType:    "feishu",
			InstallationID: inst.ID,
			Target:         payload,
		}); serr != nil {
			// The reaction is already on the message; failing to record it only
			// means it will not be removed. Cosmetic, so log and move on.
			m.log.Warn("lark typing indicator: persist target failed",
				"chat_session_id", key, "message_id", messageID, "err", serr)
		}
	}

	m.log.Debug("lark typing indicator: reaction added",
		"chat_session_id", key,
		"message_id", messageID,
		"reaction_id", reactionID,
	)
}

// Clear removes every tracked Typing reaction for the chat session and
// drops the state entry. It is synchronous so the reaction is gone before
// the agent's reply is sent, giving the user a clean visual transition.
// Individual delete failures are logged but do not abort the loop.
func (m *TypingIndicatorManager) Clear(ctx context.Context, chatSessionID pgtype.UUID) {
	key := uuidString(chatSessionID)
	if m.store == nil {
		return
	}
	// DELETE ... RETURNING claims the pending reactions: if two replicas race
	// to clear the same run, exactly one gets the rows and only it removes.
	rows, err := m.store.TakeChannelTypingIndicators(ctx, db.TakeChannelTypingIndicatorsParams{
		ChatSessionID: chatSessionID,
		ChannelType:   "feishu",
	})
	if err != nil {
		m.log.Warn("lark typing indicator: take pending targets failed",
			"chat_session_id", key, "err", err)
		return
	}
	if len(rows) == 0 {
		return
	}
	states := make([]*TypingIndicatorState, 0, len(rows))
	for _, row := range rows {
		var st TypingIndicatorState
		if err := json.Unmarshal(row.Target, &st); err != nil {
			m.log.Warn("lark typing indicator: decode target failed",
				"chat_session_id", key, "err", err)
			continue
		}
		states = append(states, &st)
	}

	binding, err := m.queries.GetLarkChatSessionBindingBySession(ctx, chatSessionID)
	if err != nil {
		m.log.Warn("lark typing indicator: failed to lookup binding for clear",
			"chat_session_id", key,
			"err", err,
		)
		return
	}

	inst, err := m.queries.GetLarkInstallation(ctx, binding.InstallationID)
	if err != nil {
		m.log.Warn("lark typing indicator: failed to lookup installation for clear",
			"chat_session_id", key,
			"err", err,
		)
		return
	}

	creds, err := m.resolveCredentials(inst)
	if err != nil {
		m.log.Warn("lark typing indicator: failed to resolve credentials for clear",
			"chat_session_id", key,
			"err", err,
		)
		return
	}

	for _, s := range states {
		if s.ReactionID == "" {
			continue
		}
		if err := m.client.DeleteMessageReaction(ctx, DeleteReactionParams{
			InstallationID: creds,
			MessageID:      s.MessageID,
			ReactionID:     s.ReactionID,
		}); err != nil {
			m.log.Warn("lark typing indicator: delete reaction failed",
				"chat_session_id", key,
				"message_id", s.MessageID,
				"reaction_id", s.ReactionID,
				"err", err,
			)
			continue
		}
		m.log.Debug("lark typing indicator: reaction removed",
			"chat_session_id", key,
			"message_id", s.MessageID,
			"reaction_id", s.ReactionID,
		)
	}
}

func isMessageTooOld(createTime string) bool {
	if createTime == "" {
		return false
	}
	ms, err := strconv.ParseInt(createTime, 10, 64)
	if err != nil {
		return false
	}
	return time.Since(time.UnixMilli(ms)) > typingIndicatorMaxAge
}

func (m *TypingIndicatorManager) resolveCredentials(inst Installation) (InstallationCredentials, error) {
	secret, err := m.credentials.DecryptAppSecret(inst)
	if err != nil {
		return InstallationCredentials{}, err
	}
	creds := InstallationCredentials{
		AppID:     inst.AppID,
		AppSecret: secret,
		Region:    RegionOrDefault(inst.Region),
	}
	if inst.TenantKey.Valid {
		creds.TenantKey = inst.TenantKey.String
	}
	return creds, nil
}
