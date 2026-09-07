package agentmessagerouter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type ResponsePolicyNotifier interface {
	NotifyResponsePolicyChanged()
}

type ResponsePolicySyncConfig struct {
	// Resolve from trusted runtime metadata, never from agent custom_env. The
	// argument contains only ID, WorkspaceID, and RuntimeID from the source row.
	RuntimeSupportsPolicy func(context.Context, db.Agent) (bool, error)
}

type ResponsePolicySyncStore interface {
	EnsureAgentResponsePolicyTarget(context.Context, string) (db.DingtalkResponsePolicyRollout, error)
	ListResponsePolicySyncCandidates(context.Context, db.ListResponsePolicySyncCandidatesParams) ([]db.ListResponsePolicySyncCandidatesRow, error)
	UpsertResponsePolicySync(context.Context, db.UpsertResponsePolicySyncParams) (db.DingtalkResponsePolicySync, error)
	ClaimResponsePolicySync(context.Context, string) (db.DingtalkResponsePolicySync, error)
	CompleteResponsePolicySync(context.Context, db.CompleteResponsePolicySyncParams) (int64, error)
	RetryResponsePolicySync(context.Context, db.RetryResponsePolicySyncParams) (int64, error)
}

type ResponsePolicySyncWorker struct {
	store          ResponsePolicySyncStore
	client         *Client
	config         ResponsePolicySyncConfig
	targetIdentity string
	notify         chan struct{}
	done           chan struct{}
}

func NewResponsePolicySyncWorker(store ResponsePolicySyncStore, client *Client, config ResponsePolicySyncConfig) *ResponsePolicySyncWorker {
	return &ResponsePolicySyncWorker{
		store: store, client: client, config: config, targetIdentity: client.TargetIdentity(),
		notify: make(chan struct{}, 1), done: make(chan struct{}),
	}
}

func (w *ResponsePolicySyncWorker) NotifyResponsePolicyChanged() {
	if w == nil {
		return
	}
	select {
	case w.notify <- struct{}{}:
	default:
	}
}

func (w *ResponsePolicySyncWorker) Run(ctx context.Context) {
	if w == nil {
		return
	}
	defer close(w.done)
	if w.store == nil || w.client == nil {
		return
	}
	poll := time.NewTicker(time.Second)
	defer poll.Stop()
	reconcile := time.NewTicker(30 * time.Second)
	defer reconcile.Stop()
	w.reconcileAndLog(ctx)
	for {
		worked, err := w.ProcessNext(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("dingtalk response policy sync failed", "error", err)
		}
		if worked && ctx.Err() == nil {
			select {
			case <-w.notify:
				w.reconcileAndLog(ctx)
			case <-reconcile.C:
				w.reconcileAndLog(ctx)
			default:
			}
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-w.notify:
			w.reconcileAndLog(ctx)
		case <-reconcile.C:
			w.reconcileAndLog(ctx)
		case <-poll.C:
		}
	}
}

func (w *ResponsePolicySyncWorker) WaitWithTimeout(timeout time.Duration) bool {
	if w == nil {
		return true
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-w.done:
		return true
	case <-timer.C:
		return false
	}
}

func (w *ResponsePolicySyncWorker) reconcileAndLog(ctx context.Context) {
	if err := w.Reconcile(ctx); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("dingtalk response policy reconcile failed", "error", err)
	}
}

// Reconcile rebuilds desired policies from durable agent and binding state.
// Notifications only reduce latency; a missed notification loses no work.
func (w *ResponsePolicySyncWorker) Reconcile(ctx context.Context) error {
	if w == nil || w.store == nil || w.client == nil {
		return nil
	}
	fence, err := w.store.EnsureAgentResponsePolicyTarget(ctx, w.targetIdentity)
	if err != nil {
		return err
	}
	if fence.Revision < 2 || !fence.Enabled {
		return errors.New("employee response policy migration is not active")
	}
	revision := fence.Revision
	supported, err := w.client.SupportsResponsePolicy(ctx)
	if err != nil {
		return err
	}
	if !supported {
		return nil
	}
	after := pgtype.UUID{Valid: true}
	for {
		rows, err := w.store.ListResponsePolicySyncCandidates(ctx, db.ListResponsePolicySyncCandidatesParams{AfterID: after, BatchSize: 100, TargetIdentity: w.targetIdentity})
		if err != nil {
			return err
		}
		for _, row := range rows {
			agent := db.Agent{ID: row.AgentID, WorkspaceID: row.WorkspaceID, RuntimeID: row.RuntimeID}
			capable := false
			if row.DingtalkResponseEnabled && row.InboundCoordinator && row.RuntimeID.Valid && w.config.RuntimeSupportsPolicy != nil {
				capable, err = w.config.RuntimeSupportsPolicy(ctx, agent)
				if err != nil {
					// An unavailable capability resolver is not evidence for rollback.
					slog.Warn("dingtalk response runtime capability unresolved", "agent_id", util.UUIDToString(agent.ID), "error", err)
					continue
				}
			}
			policy := desiredResponsePolicy(row, capable)
			_, err = w.store.UpsertResponsePolicySync(ctx, db.UpsertResponsePolicySyncParams{
				TargetIdentity: w.targetIdentity, SourceID: row.SourceID, InstallationID: row.InstallationID,
				WorkspaceID: row.WorkspaceID, AgentID: row.AgentID, AgentRevision: row.DingtalkResponsePolicyRevision,
				RolloutRevision: revision, SourceUpdatedAt: row.SourceUpdatedAt,
				DesiredMode: policy.Mode, ShowAiTag: policy.ShowAITag,
			})
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		if len(rows) < 100 {
			return nil
		}
		after = rows[len(rows)-1].InstallationID
	}
}

func desiredResponsePolicy(row db.ListResponsePolicySyncCandidatesRow, runtimeReady bool) protocol.DingTalkResponsePolicy {
	mode := protocol.DingTalkResponseModeLegacy
	if row.DingtalkResponseEnabled && row.InboundCoordinator && runtimeReady {
		mode = protocol.DingTalkResponseModeCoordinator
	}
	return protocol.DingTalkResponsePolicy{
		Version: protocol.DingTalkResponsePolicyVersion, Mode: mode,
		Revision: row.DingtalkResponsePolicyRevision, ShowAITag: row.DingtalkShowAiTag,
	}
}

func (w *ResponsePolicySyncWorker) ProcessNext(ctx context.Context) (bool, error) {
	if w == nil || w.store == nil || w.client == nil {
		return false, nil
	}
	row, err := w.store.ClaimResponsePolicySync(ctx, w.targetIdentity)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	policy := protocol.DingTalkResponsePolicy{
		Version: protocol.DingTalkResponsePolicyVersion, Mode: row.DesiredMode,
		Revision: row.PolicyRevision, ShowAITag: row.ShowAiTag,
	}
	// Three HTTP calls fit within the 30 second claim and are cancelled together.
	deliveryCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	err = w.syncPolicy(deliveryCtx, row.SourceID, util.UUIDToString(row.AgentID), policy)
	if err == nil {
		observed, _ := json.Marshal(policy)
		_, completeErr := w.store.CompleteResponsePolicySync(ctx, db.CompleteResponsePolicySyncParams{
			ID: row.ID, LeaseToken: row.LeaseToken, ObservedPolicy: observed,
		})
		return true, completeErr
	}
	backoff := time.Second * time.Duration(1<<min(row.AttemptCount, 8))
	_, retryErr := w.store.RetryResponsePolicySync(ctx, db.RetryResponsePolicySyncParams{
		ID: row.ID, LeaseToken: row.LeaseToken, LastError: pgtype.Text{String: err.Error(), Valid: true},
		AvailableAt: pgtype.Timestamptz{Time: time.Now().Add(backoff), Valid: true},
	})
	if retryErr != nil {
		return true, retryErr
	}
	slog.Warn("dingtalk response policy retry scheduled", "source_id", row.SourceID, "revision", row.PolicyRevision, "error", err)
	return true, nil
}

func (w *ResponsePolicySyncWorker) syncPolicy(ctx context.Context, sourceID, agentID string, desired protocol.DingTalkResponsePolicy) error {
	pinned, err := w.client.responsePolicyClient()
	if err != nil {
		return err
	}
	current, err := pinned.GetSubscription(ctx, sourceID)
	if err != nil {
		return err
	}
	if current.AgentID != agentID || current.Outbound.Mode != "dws" {
		return ErrSubscriptionDrift
	}
	if current.ResponsePolicy != nil {
		if !current.ResponsePolicy.Valid() {
			return ErrRouterInvalidResponse
		}
		if *current.ResponsePolicy == desired {
			return nil
		}
		if current.ResponsePolicy.Revision >= desired.Revision {
			return fmt.Errorf("response policy revision conflict: Router %d, desired %d", current.ResponsePolicy.Revision, desired.Revision)
		}
	}
	if _, err := pinned.UpdateSubscriptionResponsePolicy(ctx, sourceID, agentID, desired); err != nil {
		return err
	}
	// PATCH acceptance alone does not prove the actual subscription snapshot.
	observed, err := pinned.GetSubscription(ctx, sourceID)
	if err != nil {
		return err
	}
	if observed.AgentID != agentID || observed.Outbound.Mode != "dws" || observed.ResponsePolicy == nil || *observed.ResponsePolicy != desired {
		return ErrSubscriptionDrift
	}
	return nil
}
