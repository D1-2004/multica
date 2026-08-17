package service

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	a2aintegration "github.com/multica-ai/multica/server/internal/integrations/a2a"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	a2aRequestBoundControlSignal     = "request_bound"
	a2aRequestBoundTurnLeaseDuration = 30 * time.Second
	a2aRequestBoundTurnLeaseRenewal  = 10 * time.Second
	a2aRequestBoundPromotionInterval = time.Second
	a2aRequestBoundReleaseTimeout    = 5 * time.Second
)

type a2aQueuedIdentityDisposition uint8

const (
	a2aQueuedIdentityReady a2aQueuedIdentityDisposition = iota
	a2aQueuedIdentityWait
	a2aQueuedIdentityAuthRequired
)

type a2aRequestBoundTurnClaim struct {
	requestFingerprint string
	localTaskID        pgtype.UUID
	chatSessionID      pgtype.UUID
	nextLeaseRenewal   time.Time
	nextPromotion      time.Time
	promoted           bool
	stopped            bool
}

type a2aRequestBoundTurnClaimHolder struct {
	claim *a2aRequestBoundTurnClaim
}

type a2aRequestBoundTurnClaimKey struct{}

func withA2ARequestBoundTurnClaimHolder(ctx context.Context) (context.Context, *a2aRequestBoundTurnClaimHolder) {
	if holder, ok := ctx.Value(a2aRequestBoundTurnClaimKey{}).(*a2aRequestBoundTurnClaimHolder); ok && holder != nil {
		return ctx, holder
	}
	holder := &a2aRequestBoundTurnClaimHolder{}
	return context.WithValue(ctx, a2aRequestBoundTurnClaimKey{}, holder), holder
}

func ensureA2ARequestBoundTurnClaim(
	ctx context.Context,
	request validatedA2ASend,
) (context.Context, *a2aRequestBoundTurnClaim) {
	if strings.TrimSpace(request.Identity.DEAPDWSToken) == "" {
		return ctx, nil
	}
	ctx, holder := withA2ARequestBoundTurnClaimHolder(ctx)
	if holder.claim == nil {
		holder.claim = &a2aRequestBoundTurnClaim{requestFingerprint: request.Fingerprint}
	}
	if holder.claim.requestFingerprint != request.Fingerprint {
		return ctx, nil
	}
	return ctx, holder.claim
}

func a2aRequestBoundTurnClaimFromContext(ctx context.Context) *a2aRequestBoundTurnClaim {
	holder, _ := ctx.Value(a2aRequestBoundTurnClaimKey{}).(*a2aRequestBoundTurnClaimHolder)
	if holder == nil {
		return nil
	}
	return holder.claim
}

func (claim *a2aRequestBoundTurnClaim) bindTask(task db.AgentTaskQueue, now time.Time) {
	if claim == nil {
		return
	}
	claim.localTaskID = task.ID
	claim.chatSessionID = task.ChatSessionID
	claim.nextLeaseRenewal = now.Add(a2aRequestBoundTurnLeaseRenewal)
	claim.nextPromotion = time.Time{}
}

func (claim *a2aRequestBoundTurnClaim) matches(ctx context.Context, requestFingerprint string) bool {
	if claim == nil || claim.stopped || claim.promoted ||
		claim.requestFingerprint == "" || claim.requestFingerprint != requestFingerprint {
		return false
	}
	identity, ok := a2aintegration.InvocationIdentityFromContext(ctx)
	return ok && strings.TrimSpace(identity.DEAPDWSToken) != "" && identity.ContextToken == ""
}

func (s *A2AService) maintainA2ARequestBoundTurn(ctx context.Context) {
	claim := a2aRequestBoundTurnClaimFromContext(ctx)
	if s == nil || s.Queries == nil || claim == nil || claim.stopped || claim.promoted || !claim.localTaskID.Valid {
		return
	}
	now := time.Now()
	if claim.nextLeaseRenewal.IsZero() || !now.Before(claim.nextLeaseRenewal) || !claim.chatSessionID.Valid {
		leased, err := s.Queries.SetA2ARequestBoundTurnLease(ctx, db.SetA2ARequestBoundTurnLeaseParams{
			LeaseExpiresAt:     pgtype.Timestamptz{Time: now.Add(a2aRequestBoundTurnLeaseDuration), Valid: true},
			LocalTaskID:        claim.localTaskID,
			RequestFingerprint: claim.requestFingerprint,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			claim.stopped = true
			return
		}
		if err != nil {
			slog.Warn("renew A2A request-bound turn lease failed", "local_task_id", claim.localTaskID, "error", err)
			return
		}
		claim.chatSessionID = leased.ChatSessionID
		claim.nextLeaseRenewal = now.Add(a2aRequestBoundTurnLeaseRenewal)
	}
	if claim.chatSessionID.Valid && (claim.nextPromotion.IsZero() || !now.Before(claim.nextPromotion)) {
		claim.nextPromotion = now.Add(a2aRequestBoundPromotionInterval)
		s.notifyNextA2ATask(ctx, claim.chatSessionID)
	}
}

func (s *A2AService) releaseA2ARequestBoundTurn(claim *a2aRequestBoundTurnClaim) {
	if s == nil || s.Queries == nil || claim == nil || claim.stopped || claim.promoted || !claim.localTaskID.Valid {
		return
	}
	releaseCtx, cancel := context.WithTimeout(context.Background(), a2aRequestBoundReleaseTimeout)
	defer cancel()
	released, err := s.Queries.SetA2ARequestBoundTurnLease(releaseCtx, db.SetA2ARequestBoundTurnLeaseParams{
		LeaseExpiresAt:     pgtype.Timestamptz{Time: time.Now(), Valid: true},
		LocalTaskID:        claim.localTaskID,
		RequestFingerprint: claim.requestFingerprint,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		claim.stopped = true
		return
	}
	if err != nil {
		slog.Warn("release A2A request-bound turn lease failed", "local_task_id", claim.localTaskID, "error", err)
		return
	}
	claim.stopped = true
	s.notifyNextA2ATask(releaseCtx, released.ChatSessionID)
}

func a2AQueuedExternalIdentityDispositionFor(
	ctx context.Context,
	taskContext []byte,
	requestFingerprint string,
	requestBoundLease pgtype.Timestamptz,
	now time.Time,
) a2aQueuedIdentityDisposition {
	if requiresA2ADEAPDWSToken(taskContext) {
		if claim := a2aRequestBoundTurnClaimFromContext(ctx); claim.matches(ctx, requestFingerprint) {
			return a2aQueuedIdentityReady
		}
		if requestBoundLease.Valid && requestBoundLease.Time.After(now) {
			return a2aQueuedIdentityWait
		}
		return a2aQueuedIdentityAuthRequired
	}
	var envelope struct {
		Origin    string  `json:"multica_origin"`
		Token     *string `json:"agent_identity_context_token"`
		ExpiresAt *int64  `json:"agent_identity_context_token_expires_at"`
		Source    string  `json:"agent_identity_context_token_source"`
	}
	if err := json.Unmarshal(taskContext, &envelope); err != nil {
		return a2aQueuedIdentityAuthRequired
	}
	if envelope.Source != protocol.AgentIdentityContextTokenSourceExternal {
		return a2aQueuedIdentityReady
	}
	if envelope.Origin != a2aTaskOriginValue || envelope.Token == nil || strings.TrimSpace(*envelope.Token) == "" ||
		envelope.ExpiresAt == nil || !time.UnixMilli(*envelope.ExpiresAt).After(now.Add(time.Minute)) {
		return a2aQueuedIdentityAuthRequired
	}
	return a2aQueuedIdentityReady
}
