package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	asbTenantCapacityLockClass     = int32(0x41534243) // "ASBC"
	asbCapacityReleaseTimeout      = 30 * time.Second
	asbCapacityReleasePollInterval = time.Second
	asbCapacityReclaimAttempts     = 3
)

const (
	asbCapacityUnavailableMessage = "Aone Sandbox 实例额度已满，当前 Runtime 没有可安全回收的空闲任务沙箱；请等待正在处理的任务结束后重试"
	asbCapacityWaitingStage       = "sandbox_capacity_waiting"
	asbCapacityWaitingErrorCode   = "ASB-CAPACITY-WAITING"
)

var ErrASBCapacityUnavailable = withRuntimeStartUserDetail(
	errors.New(asbCapacityUnavailableMessage),
	asbCapacityUnavailableMessage,
)

type ASBSandboxCapacity interface {
	Create(
		context.Context,
		pgtype.UUID,
		*ASBClient,
		ASBCreateSandboxInput,
	) (*ASBSandbox, error)
}

type ASBSandboxCapacityManager struct {
	Pool        *pgxpool.Pool
	Credentials *ASBRuntimeClientProvider
}

func lockASBTenantCapacityInTransaction(
	ctx context.Context,
	tx pgx.Tx,
	keys ...int32,
) error {
	if tx == nil || len(keys) == 0 {
		return errors.New("ASB tenant capacity transaction lock is unavailable")
	}
	unique := make(map[int32]struct{}, len(keys))
	ordered := make([]int32, 0, len(keys))
	for _, key := range keys {
		if _, exists := unique[key]; exists {
			continue
		}
		unique[key] = struct{}{}
		ordered = append(ordered, key)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	for _, key := range ordered {
		if _, err := tx.Exec(
			ctx,
			"SELECT pg_advisory_xact_lock($1, $2)",
			asbTenantCapacityLockClass,
			key,
		); err != nil {
			return fmt.Errorf("acquire ASB tenant capacity transaction lock: %w", err)
		}
	}
	return nil
}

func (m *ASBSandboxCapacityManager) Create(
	ctx context.Context,
	runtimeID pgtype.UUID,
	client *ASBClient,
	input ASBCreateSandboxInput,
) (*ASBSandbox, error) {
	if m == nil || m.Pool == nil || m.Credentials == nil {
		return nil, errors.New("ASB tenant capacity coordination is unavailable")
	}
	conn, err := m.Pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire ASB tenant capacity connection: %w", err)
	}
	defer conn.Release()
	return createASBSandboxWithCapacityOnConnection(
		ctx,
		db.New(conn),
		m.Credentials,
		client,
		runtimeID,
		pgtype.UUID{},
		conn,
		input,
	)
}

func lockASBTenantCapacityOnConnection(
	ctx context.Context,
	conn *pgxpool.Conn,
	client *ASBClient,
) (func(), error) {
	if conn == nil || client == nil {
		return nil, errors.New("ASB tenant capacity coordination is unavailable")
	}
	if _, err := conn.Exec(
		ctx,
		"SELECT pg_advisory_lock($1, $2)",
		asbTenantCapacityLockClass,
		client.capacityLockKey,
	); err != nil {
		return nil, fmt.Errorf("acquire ASB tenant capacity lock: %w", err)
	}
	return func() {
		releaseFCE2BAdvisoryLock(
			conn,
			false,
			asbTenantCapacityLockClass,
			client.capacityLockKey,
			"ASB tenant capacity",
		)
	}, nil
}

func createASBSandboxWithCapacityOnConnection(
	ctx context.Context,
	queries *db.Queries,
	credentials *ASBRuntimeClientProvider,
	client *ASBClient,
	runtimeID pgtype.UUID,
	excludedTaskID pgtype.UUID,
	conn *pgxpool.Conn,
	input ASBCreateSandboxInput,
) (sandboxResult *ASBSandbox, resultErr error) {
	if queries == nil || credentials == nil || client == nil || conn == nil {
		return nil, errors.New("ASB tenant capacity coordination is unavailable")
	}
	releaseCapacity, err := lockASBTenantCapacityOnConnection(ctx, conn, client)
	if err != nil {
		return nil, err
	}
	defer releaseCapacity()
	if client.CapacityGate != nil {
		delay, gateErr := client.CapacityGate.admit(ctx, client)
		if gateErr != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			// Fail closed: loss of shared coordination must not cause a burst
			// of upstream requests from every replica. Keep the task queued.
			return nil, errors.Join(ErrASBCapacityUnavailable, fmt.Errorf("check ASB shared capacity cooldown: %w", gateErr))
		}
		if delay > 0 {
			slog.Info("ASB task reused tenant capacity wait", "event", "asb_capacity_cooldown_hit",
				"runtime_id", util.UUIDToString(runtimeID), "retry_after_ms", delay.Milliseconds())
			return nil, ErrASBCapacityUnavailable
		}
	}
	// Run before releasing the tenant lock, so the next replica observes the
	// cached verdict. A cache hit must not extend its own cooldown indefinitely.
	defer func() {
		resultErr = recordASBCapacityResult(ctx, client, util.UUIDToString(runtimeID), resultErr)
	}()

	quotas, err := client.ListQuotas(ctx)
	if err != nil {
		return nil, fmt.Errorf("check Aone Sandbox quota before create: %w", err)
	}
	for _, allocation := range quotas {
		slog.Info("queried ASB quota before sandbox create",
			"event", "asb_capacity_quota_queried",
			"runtime_id", util.UUIDToString(runtimeID),
			"network_zone", allocation.NetworkZone,
			"region", allocation.Region,
			"quota", allocation.Quota,
			"usage", allocation.Usage,
		)
	}
	sandbox, err := createASBSandboxInAvailableRegion(ctx, client, runtimeID, input, quotas)
	if !errors.Is(err, ErrASBCapacityUnavailable) {
		return sandbox, err
	}
	if isASBQuotaExceeded(err) {
		// Another caller may consume a slot, or an allocation may be added
		// after the initial read. Refresh once before reclaiming any sandbox.
		quotas, quotaErr := client.ListQuotas(ctx)
		if quotaErr != nil {
			return nil, errors.Join(err, quotaErr)
		}
		sandbox, err = createASBSandboxInAvailableRegion(ctx, client, runtimeID, input, quotas)
		if !errors.Is(err, ErrASBCapacityUnavailable) {
			return sandbox, err
		}
	}

	reclaimed, reclaimErr := reclaimASBSandboxForCredential(
		ctx, queries, credentials, client, runtimeID, excludedTaskID, conn,
	)
	if reclaimErr != nil {
		return nil, errors.Join(err, reclaimErr)
	}
	if !reclaimed {
		return nil, err
	}
	// Reclaim can release a slot in any region. Select from a fresh snapshot,
	// rather than retrying the original, possibly still full region.
	quotas, err = client.ListQuotas(ctx)
	if err != nil {
		return nil, errors.Join(ErrASBCapacityUnavailable, err)
	}
	return createASBSandboxInAvailableRegion(ctx, client, runtimeID, input, quotas)
}

func asbTenantQuotaExhausted(ctx context.Context, client *ASBClient) (bool, error) {
	quotas, err := client.ListQuotas(ctx)
	if err != nil {
		return false, fmt.Errorf("check Aone Sandbox quota before create: %w", err)
	}
	exhausted, _, _, err := summarizeASBQuotas(quotas)
	return exhausted, err
}

func reclaimASBSandboxForCredential(
	ctx context.Context,
	queries *db.Queries,
	credentials *ASBRuntimeClientProvider,
	client *ASBClient,
	requestingRuntimeID pgtype.UUID,
	excludedTaskID pgtype.UUID,
	conn *pgxpool.Conn,
) (bool, error) {
	started := time.Now()
	defer func() {
		slog.Info("ASB tenant capacity reclaim finished",
			"event", "asb_capacity_reclaim_finished",
			"requesting_runtime_id", util.UUIDToString(requestingRuntimeID),
			"duration_ms", time.Since(started).Milliseconds(),
		)
	}()
	scopedCredentials := *credentials
	scopedCredentials.Store = queries
	runtimeIDs, err := scopedCredentials.RuntimeIDsSharingAPIKey(ctx, requestingRuntimeID)
	if err != nil {
		return false, err
	}
	// Query the live lifecycle states explicitly. ASB has returned an empty page
	// for unscoped and metadata-only inventory reads in prepub while quota and
	// the console both show active instances. Candidate classification remains
	// local so identity sources, release validation, and unrelated workloads are
	// excluded before any delete.
	liveSandboxes, err := client.ListLiveSandboxes(ctx)
	if err != nil {
		return false, fmt.Errorf("query real-time ASB sandboxes before capacity reclaim: %w", err)
	}
	runningCount := 0
	// An empty but non-nil filter must match no sessions. Missing inventory
	// entries are not proof of termination and must not change stored state.
	liveSandboxIDs := make([]string, 0, len(liveSandboxes))
	for _, sandbox := range liveSandboxes {
		liveSandboxIDs = append(liveSandboxIDs, sandbox.ID)
		if strings.EqualFold(strings.TrimSpace(sandbox.Status.State), "running") {
			runningCount++
		}
	}
	slog.Info(
		"queried live ASB sandboxes before tenant capacity reclaim",
		"requesting_runtime_id", util.UUIDToString(requestingRuntimeID),
		"sandbox_count", len(liveSandboxes),
		"running_count", runningCount,
	)
	candidates, err := queries.ListIdleASBSandboxSessionsByRuntimes(
		ctx,
		db.ListIdleASBSandboxSessionsByRuntimesParams{
			RuntimeIds:     runtimeIDs,
			ExcludedTaskID: excludedTaskID,
			SandboxIds:     liveSandboxIDs,
		},
	)
	if err != nil {
		return false, fmt.Errorf("list idle ASB task sandboxes: %w", err)
	}
	slog.Info(
		"evaluated idle ASB task sandbox candidates for tenant capacity reclaim",
		"requesting_runtime_id", util.UUIDToString(requestingRuntimeID),
		"shared_runtime_count", len(runtimeIDs),
		"idle_candidate_count", len(candidates),
	)
	var reclaimErr error
	for _, candidate := range candidates {
		if !candidate.ScopeID.Valid ||
			(candidate.ScopeType != fcE2BScopeTypeChat &&
				candidate.ScopeType != fcE2BScopeTypeIssue) {
			continue
		}
		releaseCandidate, locked, err := tryLockASBSandboxScopeOnConnection(
			ctx,
			conn,
			candidate.RuntimeID,
			fcE2BTaskScope{typ: candidate.ScopeType, id: candidate.ScopeID},
		)
		if err != nil {
			return false, fmt.Errorf("lock idle ASB task sandbox: %w", err)
		}
		if !locked {
			continue
		}
		stillIdle, err := isIdleASBSandboxCandidate(
			ctx,
			queries,
			runtimeIDs,
			excludedTaskID,
			candidate,
		)
		if err != nil {
			releaseCandidate()
			return false, err
		}
		if !stillIdle {
			releaseCandidate()
			continue
		}
		liveCandidate, exists, err := getLiveASBSandbox(ctx, client, candidate.SandboxID)
		if err != nil {
			releaseCandidate()
			return false, fmt.Errorf("query idle ASB task sandbox state: %w", err)
		}
		if !exists {
			if err := markASBSandboxSessionStale(ctx, queries, candidate); err != nil {
				releaseCandidate()
				return false, err
			}
			releaseCandidate()
			slog.Info(
				"discarded missing ASB task sandbox capacity record",
				"requesting_runtime_id", util.UUIDToString(requestingRuntimeID),
				"runtime_id", util.UUIDToString(candidate.RuntimeID),
				"sandbox_id", candidate.SandboxID,
			)
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(liveCandidate.Status.State), "running") {
			if isTerminalASBSandboxState(liveCandidate.Status.State) {
				if err := markASBSandboxSessionStale(ctx, queries, candidate); err != nil {
					releaseCandidate()
					return false, err
				}
			}
			releaseCandidate()
			slog.Info(
				"skipped non-running ASB task sandbox during tenant capacity reclaim",
				"requesting_runtime_id", util.UUIDToString(requestingRuntimeID),
				"runtime_id", util.UUIDToString(candidate.RuntimeID),
				"sandbox_id", candidate.SandboxID,
				"sandbox_state", liveCandidate.Status.State,
			)
			continue
		}
		// Invalidate reuse before attempting termination, even if the API fails.
		// Keep the scope lock through all attempts so another launch cannot reuse it.
		if err := markASBSandboxSessionStale(ctx, queries, candidate); err != nil {
			releaseCandidate()
			return false, err
		}
		reclaimed, err := reclaimASBSandboxWithRetries(ctx, client, candidate.SandboxID)
		if err != nil {
			releaseCandidate()
			if stopASBCapacityReclaim(ctx, err) {
				return false, err
			}
			reclaimErr = errors.Join(reclaimErr, err)
			continue
		}
		releaseCandidate()
		if !reclaimed {
			// Another deletion or a quota increase made creation possible.
			return true, nil
		}
		slog.Info(
			"reclaimed idle ASB task sandbox for tenant capacity",
			"requesting_runtime_id", util.UUIDToString(requestingRuntimeID),
			"runtime_id", util.UUIDToString(candidate.RuntimeID),
			"sandbox_id", candidate.SandboxID,
			"scope_type", candidate.ScopeType,
			"scope_id", util.UUIDToString(candidate.ScopeID),
			"last_used_at", candidate.LastUsedAt,
		)
		return true, nil
	}
	reclaimed, err := reclaimUntrackedIdleASBSandbox(
		ctx,
		queries,
		client,
		requestingRuntimeID,
		runtimeIDs,
		excludedTaskID,
		liveSandboxes,
	)
	if reclaimed {
		return true, nil
	}
	if stopASBCapacityReclaim(ctx, err) {
		return false, err
	}
	return false, errors.Join(reclaimErr, err)
}

func reclaimUntrackedIdleASBSandbox(
	ctx context.Context,
	queries *db.Queries,
	client *ASBClient,
	requestingRuntimeID pgtype.UUID,
	runtimeIDs []pgtype.UUID,
	excludedTaskID pgtype.UUID,
	liveSandboxes []ASBSandbox,
) (bool, error) {
	runtimeSet := make(map[pgtype.UUID]struct{}, len(runtimeIDs))
	trackedSandboxIDs := make(map[string]struct{})
	for _, runtimeID := range runtimeIDs {
		runtimeSet[runtimeID] = struct{}{}
		sessions, err := queries.ListActiveCloudSandboxSessionsByRuntimeAndBackend(
			ctx,
			db.ListActiveCloudSandboxSessionsByRuntimeAndBackendParams{
				RuntimeID:      runtimeID,
				SandboxBackend: string(SandboxBackendASB),
			},
		)
		if err != nil {
			return false, fmt.Errorf("list tracked ASB task sandboxes: %w", err)
		}
		for _, session := range sessions {
			trackedSandboxIDs[session.SandboxID] = struct{}{}
		}
	}

	sort.SliceStable(liveSandboxes, func(i, j int) bool {
		return liveSandboxes[i].CreatedAt.Before(liveSandboxes[j].CreatedAt)
	})
	var reclaimErr error
	for _, sandbox := range liveSandboxes {
		if !strings.EqualFold(strings.TrimSpace(sandbox.Status.State), "running") {
			continue
		}
		if _, tracked := trackedSandboxIDs[sandbox.ID]; tracked {
			continue
		}
		runtimeID, err := util.ParseUUID(strings.TrimSpace(sandbox.Metadata["multica.runtime_id"]))
		if err != nil {
			continue
		}
		if _, sharedCredential := runtimeSet[runtimeID]; !sharedCredential {
			continue
		}
		taskID, err := util.ParseUUID(strings.TrimSpace(sandbox.Metadata["multica.task_id"]))
		if err != nil {
			// Pre-fix sandboxes do not carry a task fence. Do not infer idleness
			// from absence of a session while a launch may still be in progress.
			continue
		}
		if excludedTaskID.Valid && taskID == excludedTaskID {
			continue
		}
		task, err := queries.GetAgentTask(ctx, taskID)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("query ASB task fence before orphan reclaim: %w", err)
		}
		if task.RuntimeID != runtimeID || !isTerminalASBTaskStatus(task.Status) {
			continue
		}
		live, exists, err := getLiveASBSandbox(ctx, client, sandbox.ID)
		if err != nil {
			return false, fmt.Errorf("recheck untracked ASB task sandbox state: %w", err)
		}
		if !exists || !strings.EqualFold(strings.TrimSpace(live.Status.State), "running") {
			continue
		}
		reclaimed, err := reclaimASBSandboxWithRetries(ctx, client, sandbox.ID)
		if err != nil {
			if stopASBCapacityReclaim(ctx, err) {
				return false, err
			}
			reclaimErr = errors.Join(reclaimErr, err)
			continue
		}
		if !reclaimed {
			return true, nil
		}
		slog.Info(
			"reclaimed untracked idle ASB task sandbox for tenant capacity",
			"requesting_runtime_id", util.UUIDToString(requestingRuntimeID),
			"runtime_id", util.UUIDToString(runtimeID),
			"task_id", util.UUIDToString(taskID),
			"sandbox_id", sandbox.ID,
			"task_status", task.Status,
		)
		return true, nil
	}
	return false, reclaimErr
}

// Each candidate has a bounded delete-and-observe budget. A stuck sandbox must
// not prevent later idle candidates from releasing capacity in the same round.
// A nil error means capacity is available; the boolean records whether this
// candidate actually terminated, so a quota increase is not logged as a delete.
func reclaimASBSandboxWithRetries(ctx context.Context, client *ASBClient, sandboxID string) (bool, error) {
	var lastErr error
	for attempt := 1; attempt <= asbCapacityReclaimAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		attemptCtx, cancel := context.WithTimeout(ctx, asbCapacityReleaseTimeout)
		err := client.DeleteSandbox(attemptCtx, sandboxID)
		reclaimed := false
		if err == nil {
			reclaimed, err = waitForASBCapacity(attemptCtx, client, sandboxID, false)
		}
		cancel()
		if err == nil {
			if reclaimed {
				notifyASBReclaimed(ctx)
			}
			return reclaimed, nil
		}
		if stopASBCapacityReclaim(ctx, err) {
			return false, err
		}
		lastErr = err
		slog.Warn("ASB idle sandbox reclaim attempt failed",
			"event", "asb_capacity_reclaim_attempt_failed",
			"sandbox_id", sandboxID,
			"attempt", attempt,
			"max_attempts", asbCapacityReclaimAttempts,
			"error", err,
		)
	}
	slog.Warn("skipping ASB idle sandbox after reclaim attempts exhausted",
		"event", "asb_capacity_reclaim_candidate_exhausted",
		"sandbox_id", sandboxID,
		"attempts", asbCapacityReclaimAttempts,
	)
	return false, fmt.Errorf("reclaim idle ASB sandbox %s after %d attempts: %w", sandboxID, asbCapacityReclaimAttempts, lastErr)
}

func stopASBCapacityReclaim(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return true
	}
	var httpErr *ASBHTTPError
	if errors.As(err, &httpErr) {
		// These apply to the credential, not one candidate. Preserve the shared
		// rate-limit backoff and do not retry authentication failures.
		switch httpErr.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests:
			return true
		}
	}
	return false
}

func isTerminalASBTaskStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "failed", "cancelled":
		return true
	default:
		return false
	}
}

func isIdleASBSandboxCandidate(
	ctx context.Context,
	queries *db.Queries,
	runtimeIDs []pgtype.UUID,
	excludedTaskID pgtype.UUID,
	candidate db.FcE2bSandboxSession,
) (bool, error) {
	candidates, err := queries.ListIdleASBSandboxSessionsByRuntimes(
		ctx,
		db.ListIdleASBSandboxSessionsByRuntimesParams{
			RuntimeIds:     runtimeIDs,
			ExcludedTaskID: excludedTaskID,
			SandboxIds:     []string{candidate.SandboxID},
		},
	)
	if err != nil {
		return false, fmt.Errorf("recheck idle ASB task sandbox: %w", err)
	}
	for _, current := range candidates {
		if current.ID == candidate.ID &&
			current.SandboxID == candidate.SandboxID &&
			(current.Status == "running" || current.Status == "stale") {
			return true, nil
		}
	}
	return false, nil
}

func getLiveASBSandbox(
	ctx context.Context,
	client *ASBClient,
	sandboxID string,
) (*ASBSandbox, bool, error) {
	sandbox, err := client.GetSandbox(ctx, strings.TrimSpace(sandboxID))
	var httpErr *ASBHTTPError
	if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return sandbox, true, nil
}

func markASBSandboxSessionStale(
	ctx context.Context,
	queries *db.Queries,
	candidate db.FcE2bSandboxSession,
) error {
	if err := queries.MarkCloudSandboxSessionStale(
		ctx,
		db.MarkCloudSandboxSessionStaleParams{
			RuntimeID:           candidate.RuntimeID,
			ScopeType:           candidate.ScopeType,
			ScopeID:             candidate.ScopeID,
			SandboxID:           candidate.SandboxID,
			SandboxBackend:      string(SandboxBackendASB),
			IdentityFingerprint: candidate.IdentityFingerprint,
		},
	); err != nil {
		return fmt.Errorf("mark idle ASB task sandbox stale: %w", err)
	}
	return nil
}

func waitForASBCapacityRelease(
	ctx context.Context,
	client *ASBClient,
	sandboxID string,
) error {
	_, err := waitForASBCapacity(ctx, client, sandboxID, true)
	return err
}

func waitForASBCapacity(
	ctx context.Context,
	client *ASBClient,
	sandboxID string,
	requireTermination bool,
) (bool, error) {
	waitCtx, cancel := context.WithTimeout(ctx, asbCapacityReleaseTimeout)
	defer cancel()
	ticker := time.NewTicker(asbCapacityReleasePollInterval)
	defer ticker.Stop()

	lastState := "unknown"
	lastQuota := 0
	lastUsage := 0
	for {
		live, exists, err := getLiveASBSandbox(waitCtx, client, sandboxID)
		if err != nil {
			if waitCtx.Err() != nil {
				break
			}
			return false, fmt.Errorf("query ASB sandbox after capacity reclaim: %w", err)
		}
		lastState = "not_found"
		if exists {
			lastState = strings.ToLower(strings.TrimSpace(live.Status.State))
		}
		quotas, err := client.ListQuotas(waitCtx)
		if err != nil {
			if waitCtx.Err() != nil {
				break
			}
			return false, fmt.Errorf("query ASB quota after capacity reclaim: %w", err)
		}
		quotaExhausted, quota, usage, err := summarizeASBQuotas(quotas)
		if err != nil {
			return false, err
		}
		lastQuota = quota
		lastUsage = usage
		terminated := !exists || isTerminalASBSandboxState(lastState)
		if !quotaExhausted && (terminated || !requireTermination) {
			if !terminated {
				slog.Info("ASB quota became available while sandbox termination is pending",
					"event", "asb_capacity_available_during_reclaim",
					"sandbox_id", sandboxID,
					"sandbox_state", lastState,
					"quota", quota,
					"usage", usage,
				)
				return false, nil
			}
			slog.Info(
				"ASB tenant capacity released after sandbox reclaim",
				"sandbox_id", sandboxID,
				"sandbox_state", lastState,
				"quota", quota,
				"usage", usage,
			)
			return true, nil
		}

		select {
		case <-waitCtx.Done():
			break
		case <-ticker.C:
			continue
		}
		break
	}
	return false, fmt.Errorf(
		"ASB tenant capacity was not released within %s after deleting sandbox %s (sandbox_state=%s usage=%d quota=%d): %w",
		asbCapacityReleaseTimeout,
		sandboxID,
		lastState,
		lastUsage,
		lastQuota,
		waitCtx.Err(),
	)
}

func isTerminalASBSandboxState(state string) bool {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "terminated", "error":
		return true
	default:
		return false
	}
}

func summarizeASBQuotas(quotas []ASBSandboxQuota) (bool, int, int, error) {
	if len(quotas) == 0 {
		return false, 0, 0, errors.New("Aone Sandbox quota endpoint returned no allocations")
	}
	exhausted := true
	quota := 0
	usage := 0
	for _, allocation := range quotas {
		quota += allocation.Quota
		usage += allocation.Usage
		if allocation.Quota > allocation.Usage {
			exhausted = false
		}
	}
	return exhausted, quota, usage, nil
}

func asbCapacityUnavailableIfStillExhausted(
	ctx context.Context,
	client *ASBClient,
	cause error,
) error {
	exhausted, err := asbTenantQuotaExhausted(ctx, client)
	if err != nil {
		return errors.Join(cause, err)
	}
	if exhausted {
		return errors.Join(ErrASBCapacityUnavailable, cause)
	}
	return cause
}

func logASBCreateFailure(stage string, runtimeID pgtype.UUID, err error) {
	attributes := []any{
		"stage", stage,
		"runtime_id", util.UUIDToString(runtimeID),
		"error", err,
	}
	var httpErr *ASBHTTPError
	if errors.As(err, &httpErr) {
		attributes = append(
			attributes,
			"http_status", httpErr.StatusCode,
			"request_id", httpErr.RequestID,
			"error_code", httpErr.ErrorCode,
			"error_message", httpErr.ErrorMessage,
		)
	}
	slog.Warn("ASB sandbox create failed", attributes...)
}

func tryLockASBSandboxScopeOnConnection(
	ctx context.Context,
	conn *pgxpool.Conn,
	runtimeID pgtype.UUID,
	scope fcE2BTaskScope,
) (func(), bool, error) {
	if conn == nil {
		return nil, false, errors.New("ASB sandbox coordination is unavailable")
	}
	key := fcE2BScopeLockKey(runtimeID, scope)
	var locked bool
	if err := conn.QueryRow(
		ctx,
		"SELECT pg_try_advisory_lock($1, $2)",
		fcE2BSandboxLockClass,
		key,
	).Scan(&locked); err != nil {
		return nil, false, fmt.Errorf("try ASB sandbox scope lock: %w", err)
	}
	if !locked {
		return nil, false, nil
	}
	return func() {
		releaseFCE2BAdvisoryLock(
			conn,
			false,
			fcE2BSandboxLockClass,
			key,
			"ASB sandbox scope",
		)
	}, true, nil
}

func deleteASBSandboxIfExists(
	ctx context.Context,
	client *ASBClient,
	sandboxID string,
) error {
	err := client.DeleteSandbox(ctx, strings.TrimSpace(sandboxID))
	var httpErr *ASBHTTPError
	if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound {
		return nil
	}
	return err
}
