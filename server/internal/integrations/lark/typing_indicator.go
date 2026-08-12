package lark

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
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

// TypingIndicatorState holds the identifiers needed to remove a reaction, plus
// the installation whose app credentials added it. The installation id is
// recorded at add time because that is the last moment it is certainly
// resolvable: it is reachable from the session's channel_chat_session_binding
// row, and a session delete drops that row while the cancel it triggers is
// still on its way to the Patcher.
type TypingIndicatorState struct {
	MessageID       string                     `json:"message_id"`
	ReactionID      string                     `json:"reaction_id"`
	InstallSnapshot typingInstallationSnapshot `json:"installation_snapshot"`
}

type typingInstallationSnapshot struct {
	AppID              string `json:"app_id"`
	AppSecretEncrypted []byte `json:"app_secret_encrypted,omitempty"`
	TenantKey          string `json:"tenant_key,omitempty"`
	TenantKeyValid     bool   `json:"tenant_key_valid,omitempty"`
	Region             string `json:"region"`
}

func snapshotInstallation(inst Installation) typingInstallationSnapshot {
	return typingInstallationSnapshot{
		AppID:              inst.AppID,
		AppSecretEncrypted: append([]byte(nil), inst.AppSecretEncrypted...),
		TenantKey:          inst.TenantKey.String,
		TenantKeyValid:     inst.TenantKey.Valid,
		Region:             inst.Region,
	}
}

func (s typingInstallationSnapshot) installation(id pgtype.UUID) Installation {
	return Installation{
		ID:                 id,
		AppID:              s.AppID,
		AppSecretEncrypted: append([]byte(nil), s.AppSecretEncrypted...),
		TenantKey:          pgtype.Text{String: s.TenantKey, Valid: s.TenantKeyValid},
		Region:             s.Region,
	}
}

// TypingIndicatorStore persists pending reactions across replicas.
type TypingIndicatorStore interface {
	AddChannelTypingIndicator(ctx context.Context, arg db.AddChannelTypingIndicatorParams) error
	TakeChannelTypingIndicators(ctx context.Context, arg db.TakeChannelTypingIndicatorsParams) ([]db.ChannelTypingIndicator, error)
}

// TypingIndicatorQueries is the narrow DB surface the manager needs.
type TypingIndicatorQueries interface {
	GetLarkInstallation(ctx context.Context, id pgtype.UUID) (Installation, error)
}

// TypingIndicatorManager owns the "processing" reaction lifecycle for
// inbound Lark messages. When a message is successfully ingested it adds
// a Typing reaction; when the run ends — with a reply, a failure or a
// cancellation — it clears the reaction(s) for that chat session.
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
		payload, encodeErr := json.Marshal(TypingIndicatorState{
			MessageID:       messageID,
			ReactionID:      reactionID,
			InstallSnapshot: snapshotInstallation(inst),
		})
		if encodeErr != nil {
			m.log.Warn("lark typing indicator: encode target failed", "chat_session_id", key, "err", encodeErr)
		} else if storeErr := m.store.AddChannelTypingIndicator(ctx, db.AddChannelTypingIndicatorParams{
			ChatSessionID: chatSessionID, ChannelType: "feishu", InstallationID: inst.ID, Target: payload,
		}); storeErr != nil {
			m.log.Warn("lark typing indicator: persist target failed", "chat_session_id", key, "message_id", messageID, "err", storeErr)
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
//
// Credentials come from the installation each state recorded, not from the
// session's binding, because a clear can outlive that binding: deleting a chat
// session drops the binding row inside the same transaction that cancels the
// session's tasks, and the task:cancelled events that reach the Patcher are
// broadcast after that transaction commits. A binding lookup would miss, and
// since the state has already been taken here, there would be nothing left to
// clear from. Installation rows survive a session delete.
//
// They do NOT survive a runtime teardown, which deletes them in the same
// transaction — so each state also carries the installation as it stood at add
// time, consulted only when the row is gone. See TypingIndicatorState.
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
	type persistedState struct {
		TypingIndicatorState
		installationID pgtype.UUID
	}
	states := make([]persistedState, 0, len(rows))
	for _, row := range rows {
		var st TypingIndicatorState
		if err := json.Unmarshal(row.Target, &st); err != nil {
			m.log.Warn("lark typing indicator: decode target failed",
				"chat_session_id", key, "err", err)
			continue
		}
		states = append(states, persistedState{
			TypingIndicatorState: st,
			installationID:       row.InstallationID,
		})
	}

	resolved := make(map[string]*InstallationCredentials, 1)
	for _, s := range states {
		if s.ReactionID == "" {
			continue
		}
		instKey := uuidString(s.installationID)
		creds, seen := resolved[instKey]
		if !seen {
			creds = m.credentialsForInstallation(
				ctx,
				key,
				s.installationID,
				s.InstallSnapshot.installation(s.installationID),
			)
			resolved[instKey] = creds
		}
		if creds == nil {
			continue
		}
		if err := m.client.DeleteMessageReaction(ctx, DeleteReactionParams{
			InstallationID: *creds,
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

func (m *TypingIndicatorManager) credentialsForInstallation(
	ctx context.Context,
	sessionKey string,
	id pgtype.UUID,
	snapshot Installation,
) *InstallationCredentials {
	inst, err := m.queries.GetLarkInstallation(ctx, id)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) || !snapshot.ID.Valid {
			m.log.Warn("lark typing indicator: failed to lookup installation for clear",
				"chat_session_id", sessionKey,
				"installation_id", uuidString(id),
				"err", err,
			)
			return nil
		}
		inst = snapshot
	}
	creds, err := m.resolveCredentials(inst)
	if err != nil {
		m.log.Warn("lark typing indicator: failed to resolve credentials for clear",
			"chat_session_id", sessionKey,
			"installation_id", uuidString(id),
			"err", err,
		)
		return nil
	}
	return &creds
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
