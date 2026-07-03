package dingtalk

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// This file is the DingTalk ResolverSet: the platform-specific seams the
// channel-agnostic engine.Router runs the inbound pipeline through. It
// mirrors the Slack ResolverSet and is built entirely on the generic
// channel_* queries (no new query, no schema change) plus the shared
// engine.ChatSession.

// originDingTalkChat is the issue.origin_type label for issues created via
// the DingTalk /issue command (migration 132 widens the CHECK).
const originDingTalkChat = "dingtalk_chat"

// NewDingTalkResolverSet assembles the DingTalk ResolverSet over the
// generated queries + a tx starter (for the shared session service). The
// replier delivers the outbound binding-prompt / status / issue-created
// notices; pass nil to disable them.
func NewDingTalkResolverSet(q *db.Queries, tx engine.TxStarter, replier engine.OutboundReplier) engine.ResolverSet {
	return engine.ResolverSet{
		Installation: &installationResolver{q: q},
		Identity:     &identityResolver{q: q},
		Dedup:        &deduper{q: q},
		Session: &sessionBinder{session: engine.NewChatSession(q, tx, TypeDingtalk, engine.SessionTitles{
			Group:    "DingTalk group chat",
			Direct:   "DingTalk direct message",
			Fallback: "DingTalk chat",
		})},
		Audit:      &auditor{q: q},
		Replier:    replier,
		OriginType: originDingTalkChat,
	}
}

var (
	_ engine.InstallationResolver = (*installationResolver)(nil)
	_ engine.IdentityResolver     = (*identityResolver)(nil)
	_ engine.Deduper              = (*deduper)(nil)
	_ engine.SessionBinder        = (*sessionBinder)(nil)
	_ engine.Auditor              = (*auditor)(nil)
)

// dingtalkBindingConfig is the opaque outbound routing persisted on the
// chat-session binding's config. For a DM the robot API addresses the
// recipient by staff id (not by conversation id), so the sender's staff id
// is captured at session creation; a DM session has exactly one human, so
// the value is stable for the session's life. Group sessions leave it
// empty — the binding key (conversationId) IS the openConversationId the
// group send API wants.
type dingtalkBindingConfig struct {
	SenderStaffID string `json:"sender_staff_id,omitempty"`
}

// dingtalkSessionRouting derives the session-isolation key and the outbound
// routing config from one inbound message. DingTalk has no threads, so the
// key is simply the conversation id for both chat types.
func dingtalkSessionRouting(msg channel.InboundMessage) (bindingKey string, config []byte) {
	var cfg dingtalkBindingConfig
	if msg.Source.ChatType == channel.ChatTypeP2P {
		if raw, err := decodeDingTalkRaw(msg); err == nil {
			cfg.SenderStaffID = raw.SenderStaffID
		}
	}
	out, _ := json.Marshal(cfg)
	return msg.Source.ChatID, out
}

func nullText(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}

// ---- installation routing ----

type installationResolver struct{ q *db.Queries }

func (r *installationResolver) ResolveInstallation(ctx context.Context, msg channel.InboundMessage) (engine.ResolvedInstallation, error) {
	raw, err := decodeDingTalkRaw(msg)
	if err != nil {
		return engine.ResolvedInstallation{}, err
	}
	// Route by the client_id the per-installation connection stamped on
	// the message: each Stream Mode connection only ever delivers its own
	// app's callbacks, so the app id uniquely identifies the installation.
	inst, err := r.q.GetChannelInstallationByAppID(ctx, db.GetChannelInstallationByAppIDParams{
		ChannelType: string(TypeDingtalk),
		AppID:       raw.ClientID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return engine.ResolvedInstallation{}, engine.ErrInstallationNotFound
		}
		return engine.ResolvedInstallation{}, err
	}
	return engine.ResolvedInstallation{
		ID:              inst.ID,
		WorkspaceID:     inst.WorkspaceID,
		AgentID:         inst.AgentID,
		InstallerUserID: inst.InstallerUserID,
		Active:          inst.Status == "active",
		Platform:        inst,
	}, nil
}

// ---- identity ----

type identityResolver struct{ q *db.Queries }

func (r *identityResolver) ResolveSender(ctx context.Context, inst engine.ResolvedInstallation, msg channel.InboundMessage) (engine.ResolvedIdentity, error) {
	binding, err := r.q.GetChannelUserBindingByUserID(ctx, db.GetChannelUserBindingByUserIDParams{
		InstallationID: inst.ID,
		ChannelUserID:  msg.Source.SenderID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return engine.ResolvedIdentity{}, engine.ErrSenderUnbound
		}
		return engine.ResolvedIdentity{}, err
	}
	// Binding existence no longer proves membership (no FK); re-check.
	if _, err := r.q.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
		UserID:      binding.MulticaUserID,
		WorkspaceID: inst.WorkspaceID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return engine.ResolvedIdentity{}, engine.ErrSenderNotMember
		}
		return engine.ResolvedIdentity{}, err
	}
	return engine.ResolvedIdentity{UserID: binding.MulticaUserID}, nil
}

// ---- dedup ----

type deduper struct{ q *db.Queries }

func (r *deduper) Claim(ctx context.Context, installationID pgtype.UUID, messageID string) (pgtype.UUID, error) {
	claim, err := r.q.ClaimChannelInboundDedup(ctx, db.ClaimChannelInboundDedupParams{
		InstallationID: installationID,
		MessageID:      messageID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return pgtype.UUID{}, engine.ErrDuplicate
		}
		return pgtype.UUID{}, err
	}
	return claim.ClaimToken, nil
}

func (r *deduper) Mark(ctx context.Context, installationID pgtype.UUID, messageID string, claimToken pgtype.UUID) error {
	_, err := r.q.MarkChannelInboundDedupProcessed(ctx, db.MarkChannelInboundDedupProcessedParams{
		InstallationID: installationID,
		MessageID:      messageID,
		ClaimToken:     claimToken,
	})
	return err
}

func (r *deduper) Release(ctx context.Context, installationID pgtype.UUID, messageID string, claimToken pgtype.UUID) error {
	_, err := r.q.ReleaseChannelInboundDedup(ctx, db.ReleaseChannelInboundDedupParams{
		InstallationID: installationID,
		MessageID:      messageID,
		ClaimToken:     claimToken,
	})
	return err
}

// ---- session bind / append ----

type sessionBinder struct{ session *engine.ChatSession }

func (r *sessionBinder) EnsureSession(ctx context.Context, p engine.EnsureSessionParams) (pgtype.UUID, error) {
	bindingKey, config := dingtalkSessionRouting(p.Message)
	return r.session.EnsureSession(ctx, engine.EnsureSessionInput{
		WorkspaceID:    p.Installation.WorkspaceID,
		AgentID:        p.Installation.AgentID,
		InstallationID: p.Installation.ID,
		Sender:         p.Sender,
		BindingKey:     bindingKey,
		BindingConfig:  config,
		ChatType:       p.Message.Source.ChatType,
	})
}

func (r *sessionBinder) AppendMessage(ctx context.Context, p engine.AppendParams) (engine.AppendResult, error) {
	return r.session.AppendUserMessage(ctx, engine.AppendInput{
		SessionID:      p.SessionID,
		Sender:         p.Sender,
		InstallationID: p.InstallationID,
		Body:           p.Message.Text,
		// DingTalk text is not enriched, so the command source is the body.
		CommandText: p.Message.Text,
		MessageID:   p.Message.MessageID,
		ClaimToken:  p.ClaimToken,
	})
}

// ---- audit ----

type auditor struct{ q *db.Queries }

func (r *auditor) RecordDrop(ctx context.Context, instID pgtype.UUID, msg channel.InboundMessage, reason engine.DropReason) error {
	raw, _ := decodeDingTalkRaw(msg) // best-effort; a decode miss still audits the drop
	return r.q.RecordChannelInboundDrop(ctx, db.RecordChannelInboundDropParams{
		ChannelType:      string(TypeDingtalk),
		EventType:        raw.Msgtype,
		DropReason:       string(reason),
		InstallationID:   instID,
		ChannelChatID:    nullText(msg.Source.ChatID),
		ChannelEventID:   nullText(msg.EventID),
		ChannelMessageID: nullText(msg.MessageID),
	})
}
