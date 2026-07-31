package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const asbTenantCapacityLockClass = int32(0x41534243) // "ASBC"

var ErrASBCapacityUnavailable = errors.New(
	"Aone Sandbox 实例额度已满，当前 Runtime 没有可安全回收的空闲任务沙箱；请等待正在处理的任务结束后重试",
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
	conn *pgxpool.Conn,
	input ASBCreateSandboxInput,
) (*ASBSandbox, error) {
	if queries == nil || credentials == nil || client == nil || conn == nil {
		return nil, errors.New("ASB tenant capacity coordination is unavailable")
	}
	releaseCapacity, err := lockASBTenantCapacityOnConnection(ctx, conn, client)
	if err != nil {
		return nil, err
	}
	defer releaseCapacity()

	quotaExhausted, err := asbTenantQuotaExhausted(ctx, client)
	if err != nil {
		return nil, err
	}
	if quotaExhausted {
		reclaimed, err := reclaimIdleASBSandboxForCredential(
			ctx,
			queries,
			credentials,
			client,
			runtimeID,
			conn,
		)
		if err != nil {
			return nil, err
		}
		if !reclaimed {
			return nil, ErrASBCapacityUnavailable
		}
	}

	sandbox, err := client.CreateSandbox(ctx, input)
	if err == nil {
		return sandbox, nil
	}
	// The quota can change between its read and the create call. Re-read it
	// while still holding the tenant lock, reclaim one now-idle task sandbox,
	// and repeat the create operation once.
	quotaExhausted, quotaErr := asbTenantQuotaExhausted(ctx, client)
	if quotaErr != nil {
		return nil, errors.Join(fmt.Errorf("create ASB sandbox: %w", err), quotaErr)
	}
	if !quotaExhausted {
		return nil, fmt.Errorf("create ASB sandbox: %w", err)
	}
	reclaimed, reclaimErr := reclaimIdleASBSandboxForCredential(
		ctx,
		queries,
		credentials,
		client,
		runtimeID,
		conn,
	)
	if reclaimErr != nil {
		return nil, errors.Join(fmt.Errorf("create ASB sandbox: %w", err), reclaimErr)
	}
	if !reclaimed {
		return nil, ErrASBCapacityUnavailable
	}
	sandbox, err = client.CreateSandbox(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("create ASB sandbox after idle-instance reclaim: %w", err)
	}
	return sandbox, nil
}

func asbTenantQuotaExhausted(ctx context.Context, client *ASBClient) (bool, error) {
	quotas, err := client.ListQuotas(ctx)
	if err != nil {
		return false, fmt.Errorf("check Aone Sandbox quota before create: %w", err)
	}
	if len(quotas) == 0 {
		return false, errors.New("Aone Sandbox quota endpoint returned no allocations")
	}
	for _, quota := range quotas {
		if quota.Quota > quota.Usage {
			return false, nil
		}
	}
	return true, nil
}

func reclaimIdleASBSandboxForCredential(
	ctx context.Context,
	queries *db.Queries,
	credentials *ASBRuntimeClientProvider,
	client *ASBClient,
	requestingRuntimeID pgtype.UUID,
	conn *pgxpool.Conn,
) (bool, error) {
	scopedCredentials := *credentials
	scopedCredentials.Store = queries
	runtimeIDs, err := scopedCredentials.RuntimeIDsSharingAPIKey(ctx, requestingRuntimeID)
	if err != nil {
		return false, err
	}
	candidates, err := queries.ListIdleASBSandboxSessionsByRuntimes(ctx, runtimeIDs)
	if err != nil {
		return false, fmt.Errorf("list idle ASB task sandboxes: %w", err)
	}
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
			releaseCandidate()
			return false, fmt.Errorf("mark idle ASB task sandbox stale: %w", err)
		}
		if err := deleteASBSandboxIfExists(ctx, client, candidate.SandboxID); err != nil {
			releaseCandidate()
			return false, fmt.Errorf("delete idle ASB task sandbox: %w", err)
		}
		releaseCandidate()
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
	return false, nil
}

func isIdleASBSandboxCandidate(
	ctx context.Context,
	queries *db.Queries,
	runtimeIDs []pgtype.UUID,
	candidate db.FcE2bSandboxSession,
) (bool, error) {
	candidates, err := queries.ListIdleASBSandboxSessionsByRuntimes(ctx, runtimeIDs)
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
