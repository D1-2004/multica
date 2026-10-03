package deploymentfence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type State string

const (
	StateNormal   State = "normal"
	StateDraining State = "draining"
	StateFrozen   State = "frozen"

	transitionLockKey   int64 = 7244554146635925502
	defaultPollInterval       = 2 * time.Second
	defaultLiveWindow         = 15 * time.Second
)

type Snapshot struct {
	State     State     `json:"state"`
	Revision  int64     `json:"revision"`
	Reason    string    `json:"reason"`
	UpdatedBy string    `json:"updated_by"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Replica struct {
	InstanceID string    `json:"instance_id"`
	BuildID    string    `json:"build_id"`
	State      State     `json:"state"`
	Revision   int64     `json:"revision"`
	StartedAt  time.Time `json:"started_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
	Live       bool      `json:"live"`
}

type WorkCounts struct {
	ActiveTasks              int64 `json:"active_tasks"`
	CompletionOutbox         int64 `json:"completion_outbox"`
	ExecutionUpdateOutbox    int64 `json:"execution_update_outbox"`
	DingTalkStreamProcessing int64 `json:"dingtalk_stream_processing"`
	WebhookLeases            int64 `json:"webhook_leases"`
	DispatchAcceptances      int64 `json:"dispatch_acceptances"`
	SceneMemoryLeases        int64 `json:"scene_memory_leases"`
	ResponseActions          int64 `json:"response_actions"`
	SandboxSendReceipts      int64 `json:"sandbox_send_receipts"`
}

func (c WorkCounts) Total() int64 {
	return c.ActiveTasks + c.CompletionOutbox + c.ExecutionUpdateOutbox +
		c.DingTalkStreamProcessing + c.WebhookLeases + c.DispatchAcceptances +
		c.SceneMemoryLeases + c.ResponseActions + c.SandboxSendReceipts
}

type Status struct {
	Fence          Snapshot   `json:"fence"`
	Work           WorkCounts `json:"work"`
	Replicas       []Replica  `json:"replicas"`
	UnfencedTables []string   `json:"unfenced_tables"`
}

type TransitionError struct {
	Message string
}

func (e *TransitionError) Error() string { return e.Message }

type Service struct {
	pool         *pgxpool.Pool
	instanceID   string
	buildID      string
	pollInterval time.Duration
	liveWindow   time.Duration
	snapshot     atomic.Pointer[Snapshot]
}

func New(ctx context.Context, pool *pgxpool.Pool, instanceID, buildID string) (*Service, error) {
	if pool == nil {
		return nil, errors.New("deployment fence requires a database pool")
	}
	instanceID = strings.TrimSpace(instanceID)
	if instanceID == "" {
		return nil, errors.New("deployment fence instance id is empty")
	}
	s := &Service{
		pool:         pool,
		instanceID:   instanceID,
		buildID:      strings.TrimSpace(buildID),
		pollInterval: defaultPollInterval,
		liveWindow:   defaultLiveWindow,
	}
	if err := s.Refresh(ctx); err != nil {
		return nil, fmt.Errorf("initialize deployment fence: %w", err)
	}
	return s, nil
}

func (s *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(s.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			refreshCtx, cancel := context.WithTimeout(ctx, s.pollInterval)
			err := s.Refresh(refreshCtx)
			cancel()
			if err != nil && ctx.Err() == nil {
				slog.Error("deployment fence refresh failed", "error", err)
			}
		}
	}
}

func (s *Service) Refresh(ctx context.Context) error {
	var snapshot Snapshot
	err := s.pool.QueryRow(ctx, `
		WITH current_fence AS MATERIALIZED (
			SELECT state, revision, reason, updated_by, updated_at
			FROM deployment_fence
			WHERE singleton_id = 1
		), acknowledged AS (
			INSERT INTO deployment_fence_replica_ack (
				instance_id, build_id, state, revision, last_seen_at
			)
			SELECT $1, $2, state, revision, now()
			FROM current_fence
			ON CONFLICT (instance_id) DO UPDATE SET
				build_id = EXCLUDED.build_id,
				state = EXCLUDED.state,
				revision = EXCLUDED.revision,
				last_seen_at = EXCLUDED.last_seen_at
			RETURNING 1
		)
		SELECT state, revision, reason, updated_by, updated_at
		FROM current_fence, acknowledged
	`, s.instanceID, s.buildID).Scan(
		&snapshot.State,
		&snapshot.Revision,
		&snapshot.Reason,
		&snapshot.UpdatedBy,
		&snapshot.UpdatedAt,
	)
	if err != nil {
		return err
	}
	s.snapshot.Store(&snapshot)
	return nil
}

func (s *Service) Snapshot() Snapshot {
	if current := s.snapshot.Load(); current != nil {
		return *current
	}
	panic("deployment fence snapshot accessed before initialization")
}

func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isDeploymentOperationalPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		snapshot := s.Snapshot()
		if snapshot.State == StateNormal ||
			(snapshot.State == StateDraining && (strings.HasPrefix(r.URL.Path, "/api/daemon/") || isTaskSendReceipt(r))) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "5")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error":    "deployment fence rejects writes",
			"state":    snapshot.State,
			"revision": snapshot.Revision,
		})
	})
}

func isTaskSendReceipt(r *http.Request) bool {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	return r.Method == http.MethodPost && len(parts) == 4 && parts[0] == "api" && parts[1] == "tasks" && parts[2] != "" && parts[3] == "dingtalk-send-receipts"
}

func isDeploymentOperationalPath(path string) bool {
	switch path {
	case "/health", "/readyz", "/healthz", "/health/realtime",
		"/api/internal/logs/tail", "/api/internal/deployment-fence":
		return true
	default:
		return false
	}
}

func (s *Service) Transition(ctx context.Context, target State, reason, updatedBy string) (Snapshot, error) {
	if !target.Valid() {
		return Snapshot{}, &TransitionError{Message: fmt.Sprintf("invalid deployment fence state %q", target)}
	}
	reason = strings.TrimSpace(reason)
	updatedBy = strings.TrimSpace(updatedBy)
	if target != StateNormal && reason == "" {
		return Snapshot{}, &TransitionError{Message: "a reason is required when restricting writes"}
	}
	if len(reason) > 500 {
		return Snapshot{}, &TransitionError{Message: "deployment fence reason exceeds 500 characters"}
	}
	if updatedBy == "" {
		return Snapshot{}, &TransitionError{Message: "deployment fence operator is empty"}
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Snapshot{}, fmt.Errorf("begin deployment fence transition: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", transitionLockKey); err != nil {
		return Snapshot{}, fmt.Errorf("acquire deployment fence transition lock: %w", err)
	}

	var current Snapshot
	if err := tx.QueryRow(ctx, `
		SELECT state, revision, reason, updated_by, updated_at
		FROM deployment_fence
		WHERE singleton_id = 1
		FOR UPDATE
	`).Scan(&current.State, &current.Revision, &current.Reason, &current.UpdatedBy, &current.UpdatedAt); err != nil {
		return Snapshot{}, fmt.Errorf("load deployment fence: %w", err)
	}
	if current.State == target {
		if err := tx.Commit(ctx); err != nil {
			return Snapshot{}, fmt.Errorf("commit idempotent deployment fence transition: %w", err)
		}
		if err := s.Refresh(ctx); err != nil {
			return Snapshot{}, fmt.Errorf("acknowledge idempotent deployment fence transition: %w", err)
		}
		return s.Snapshot(), nil
	}
	if !validTransition(current.State, target) {
		return Snapshot{}, &TransitionError{Message: fmt.Sprintf("invalid deployment fence transition %s -> %s", current.State, target)}
	}

	if target == StateFrozen {
		if err := s.verifyReplicasAcknowledged(ctx, tx, current); err != nil {
			return Snapshot{}, err
		}
		unfencedTables, err := queryUnfencedTables(ctx, tx)
		if err != nil {
			return Snapshot{}, fmt.Errorf("verify deployment fence trigger coverage: %w", err)
		}
		if len(unfencedTables) != 0 {
			return Snapshot{}, &TransitionError{Message: fmt.Sprintf("deployment fence does not cover %d business tables", len(unfencedTables))}
		}
		counts, err := queryWorkCounts(ctx, tx)
		if err != nil {
			return Snapshot{}, fmt.Errorf("count deployment fence work: %w", err)
		}
		if counts.Total() != 0 {
			return Snapshot{}, &TransitionError{Message: fmt.Sprintf("deployment fence is not drained; %d active or pending records remain", counts.Total())}
		}
	}

	var updated Snapshot
	if err := tx.QueryRow(ctx, `
		UPDATE deployment_fence
		SET state = $1,
			revision = revision + 1,
			reason = $2,
			updated_by = $3,
			updated_at = now()
		WHERE singleton_id = 1
		RETURNING state, revision, reason, updated_by, updated_at
	`, target, reason, updatedBy).Scan(
		&updated.State,
		&updated.Revision,
		&updated.Reason,
		&updated.UpdatedBy,
		&updated.UpdatedAt,
	); err != nil {
		return Snapshot{}, fmt.Errorf("update deployment fence: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Snapshot{}, fmt.Errorf("commit deployment fence transition: %w", err)
	}
	if err := s.Refresh(ctx); err != nil {
		return Snapshot{}, fmt.Errorf("acknowledge deployment fence transition: %w", err)
	}
	return updated, nil
}

func (s State) Valid() bool {
	return s == StateNormal || s == StateDraining || s == StateFrozen
}

func validTransition(current, target State) bool {
	switch current {
	case StateNormal:
		return target == StateDraining
	case StateDraining:
		return target == StateNormal || target == StateFrozen
	case StateFrozen:
		return target == StateNormal
	default:
		return false
	}
}

func (s *Service) verifyReplicasAcknowledged(ctx context.Context, tx pgx.Tx, current Snapshot) error {
	cutoff := time.Now().Add(-s.liveWindow)
	var live, acknowledged int64
	if err := tx.QueryRow(ctx, `
		SELECT
			count(*),
			count(*) FILTER (WHERE state = $1 AND revision = $2)
		FROM deployment_fence_replica_ack
		WHERE last_seen_at >= $3
	`, current.State, current.Revision, cutoff).Scan(&live, &acknowledged); err != nil {
		return fmt.Errorf("check deployment fence replica acknowledgements: %w", err)
	}
	if live == 0 {
		return &TransitionError{Message: "no live replica has acknowledged the draining fence"}
	}
	if acknowledged != live {
		return &TransitionError{Message: fmt.Sprintf("deployment fence acknowledgements incomplete: %d/%d live replicas", acknowledged, live)}
	}
	return nil
}

type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type rowsQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func queryUnfencedTables(ctx context.Context, q rowsQuerier) ([]string, error) {
	rows, err := q.Query(ctx, `
		SELECT namespace.nspname || '.' || class.relname
		FROM pg_class AS class
		JOIN pg_namespace AS namespace ON namespace.oid = class.relnamespace
		WHERE namespace.nspname = 'public'
		  AND class.relkind IN ('r', 'p')
		  AND class.relname NOT IN (
		      'schema_migrations',
		      'deployment_fence',
		      'deployment_fence_replica_ack'
		  )
		  AND NOT EXISTS (
		      SELECT 1
		      FROM pg_trigger AS trigger
		      WHERE trigger.tgrelid = class.oid
		        AND trigger.tgname = 'trg_deployment_fence_00_global'
		        AND NOT trigger.tgisinternal
		  )
		ORDER BY namespace.nspname, class.relname
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tables := make([]string, 0)
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			return nil, err
		}
		tables = append(tables, table)
	}
	return tables, rows.Err()
}

func queryWorkCounts(ctx context.Context, q rowQuerier) (WorkCounts, error) {
	var counts WorkCounts
	err := q.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM agent_task_queue
			 WHERE status IN ('dispatched', 'running', 'waiting_local_directory')),
			(SELECT count(*) FROM task_completion_outbox WHERE status = 'queued'),
			(SELECT count(*) FROM task_execution_update_outbox WHERE status = 'queued'),
			(SELECT count(*) FROM dingtalk_stream_inbox WHERE status = 'processing'),
			(SELECT count(*) FROM webhook_delivery
			 WHERE status IN ('queued', 'queued_frozen') AND lease_token IS NOT NULL),
			(SELECT count(*) FROM agent_dispatch_acceptance WHERE status = 'pending'),
			(SELECT count(*) FROM agent_scene_memory
			 WHERE lease_token IS NOT NULL AND lease_expires_at > now()),
			(SELECT count(*) FROM response_action
			 WHERE next_attempt_at IS NOT NULL OR lease_token IS NOT NULL),
			(SELECT count(*) FROM sandbox_send_receipt
			 WHERE next_attempt_at IS NOT NULL OR lease_token IS NOT NULL)
	`).Scan(
		&counts.ActiveTasks,
		&counts.CompletionOutbox,
		&counts.ExecutionUpdateOutbox,
		&counts.DingTalkStreamProcessing,
		&counts.WebhookLeases,
		&counts.DispatchAcceptances,
		&counts.SceneMemoryLeases,
		&counts.ResponseActions,
		&counts.SandboxSendReceipts,
	)
	return counts, err
}

func (s *Service) Status(ctx context.Context) (Status, error) {
	if err := s.Refresh(ctx); err != nil {
		return Status{}, fmt.Errorf("refresh deployment fence status: %w", err)
	}
	status := Status{Fence: s.Snapshot()}
	counts, err := queryWorkCounts(ctx, s.pool)
	if err != nil {
		return Status{}, fmt.Errorf("count deployment fence status work: %w", err)
	}
	status.Work = counts
	unfencedTables, err := queryUnfencedTables(ctx, s.pool)
	if err != nil {
		return Status{}, fmt.Errorf("inspect deployment fence trigger coverage: %w", err)
	}
	status.UnfencedTables = unfencedTables

	cutoff := time.Now().Add(-s.liveWindow)
	rows, err := s.pool.Query(ctx, `
		SELECT instance_id, build_id, state, revision, started_at, last_seen_at
		FROM deployment_fence_replica_ack
		WHERE last_seen_at >= now() - interval '24 hours'
		ORDER BY last_seen_at DESC, instance_id
	`)
	if err != nil {
		return Status{}, fmt.Errorf("list deployment fence replicas: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var replica Replica
		if err := rows.Scan(
			&replica.InstanceID,
			&replica.BuildID,
			&replica.State,
			&replica.Revision,
			&replica.StartedAt,
			&replica.LastSeenAt,
		); err != nil {
			return Status{}, fmt.Errorf("scan deployment fence replica: %w", err)
		}
		replica.Live = !replica.LastSeenAt.Before(cutoff)
		status.Replicas = append(status.Replicas, replica)
	}
	if err := rows.Err(); err != nil {
		return Status{}, fmt.Errorf("iterate deployment fence replicas: %w", err)
	}
	return status, nil
}

// AllLiveReplicasSupport gates a new persisted format until old writers have
// left the cluster. No workspace or model data can assert this build marker.
func (s *Service) AllLiveReplicasSupport(ctx context.Context, marker string) (bool, error) {
	if s == nil || marker == "" {
		return false, fmt.Errorf("replica compatibility marker required")
	}
	if s.Snapshot().State != StateNormal {
		return false, nil
	}
	var ready bool
	err := s.pool.QueryRow(ctx, `SELECT count(*)>0 AND bool_and(position($1 in build_id)>0) FROM deployment_fence_replica_ack WHERE last_seen_at >= $2`, marker, time.Now().Add(-s.liveWindow)).Scan(&ready)
	return ready, err
}
