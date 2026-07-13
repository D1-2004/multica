package daemonws

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
)

// PGNotifyChannel is the Postgres NOTIFY channel wakeups travel on.
const PGNotifyChannel = "daemon_task_available"

// pgWakeupPayload is what crosses the channel. NOTIFY payloads are capped at
// 8000 bytes; two ids and a dedupe key are nowhere near that.
type pgWakeupPayload struct {
	RuntimeID string `json:"runtime_id"`
	TaskID    string `json:"task_id"`
	EventID   string `json:"event_id"`
	// Origin is the sender's node id. A node ignores its own notifications:
	// it already delivered locally before publishing.
	Origin string `json:"origin"`
}

// PGNotifier is a cross-node wakeup relay built on Postgres LISTEN/NOTIFY.
//
// A daemon's wakeup WebSocket is pinned to whichever replica it happened to
// connect to, while the task that should wake it is enqueued by whichever
// replica served the HTTP request. With an in-process hub as the only
// notifier, those two are the same node only by luck, and every miss costs
// the daemon a full poll interval (30s) before it notices the task.
//
// RelayNotifier already solves this — through Redis. This is the same shape
// for deployments that have Postgres but no Redis: publish the hint on a
// NOTIFY channel, and have every node LISTEN and attempt local delivery.
// Delivery stays best-effort: a wakeup is a hint that a task is claimable,
// never the source of truth (the task row is), so a dropped notification
// degrades to the poll interval rather than losing work.
type PGNotifier struct {
	local  *Hub
	pool   *pgxpool.Pool
	nodeID string
	logger *slog.Logger
}

func NewPGNotifier(local *Hub, pool *pgxpool.Pool, logger *slog.Logger) *PGNotifier {
	if logger == nil {
		logger = slog.Default()
	}
	return &PGNotifier{
		local:  local,
		pool:   pool,
		nodeID: ulid.Make().String(),
		logger: logger,
	}
}

// NotifyTaskAvailable delivers locally first (the common case when the daemon
// is connected to this node), then publishes so the other nodes can try too.
func (n *PGNotifier) NotifyTaskAvailable(runtimeID, taskID string) {
	if n == nil || runtimeID == "" {
		return
	}
	eventID := ulid.Make().String()
	if n.local != nil {
		n.local.notifyTaskAvailable(runtimeID, taskID, eventID)
	}
	if n.pool == nil {
		return
	}
	payload, err := json.Marshal(pgWakeupPayload{
		RuntimeID: runtimeID,
		TaskID:    taskID,
		EventID:   eventID,
		Origin:    n.nodeID,
	})
	if err != nil {
		M.WakeupPublishErrors.Add(1)
		return
	}
	// Bounded: a slow publish must not hold up the enqueue path. Losing the
	// hint costs a poll interval, not the task.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := n.pool.Exec(ctx, "SELECT pg_notify($1, $2)", PGNotifyChannel, string(payload)); err != nil {
		M.WakeupPublishErrors.Add(1)
		n.logger.Warn("daemon wakeup publish failed",
			"error", err, "runtime_id", runtimeID, "task_id", taskID)
	}
}

// Listen runs the subscriber loop until ctx is cancelled. It holds one
// dedicated connection (LISTEN is session state, so it cannot ride the shared
// pool) and reconnects with backoff — a dropped listener silently downgrades
// every daemon on this node to poll-only, which is the failure this type
// exists to prevent, so it must never quit on a transient error.
func (n *PGNotifier) Listen(ctx context.Context) {
	if n == nil || n.pool == nil {
		return
	}
	backoff := time.Second
	const maxBackoff = 30 * time.Second

	for ctx.Err() == nil {
		err := n.listenOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		n.logger.Warn("daemon wakeup listener dropped; reconnecting",
			"error", err, "retry_in", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < maxBackoff {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

func (n *PGNotifier) listenOnce(ctx context.Context) error {
	// A dedicated connection, not one from the pool: LISTEN is session state,
	// and a pooled connection carrying it would hand that state to whatever
	// query borrowed it next. One extra connection per replica.
	conn, err := pgx.ConnectConfig(ctx, n.pool.Config().ConnConfig)
	if err != nil {
		return err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = conn.Close(closeCtx)
	}()

	if _, err := conn.Exec(ctx, "LISTEN "+PGNotifyChannel); err != nil {
		return err
	}
	n.logger.Info("daemon wakeup listener connected",
		"channel", PGNotifyChannel, "node_id", n.nodeID)

	for {
		notification, err := conn.WaitForNotification(ctx)
		if err != nil {
			return err
		}
		n.handle(notification.Payload)
	}
}

func (n *PGNotifier) handle(raw string) {
	var payload pgWakeupPayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		n.logger.Warn("daemon wakeup: undecodable notification", "error", err)
		return
	}
	if payload.Origin == n.nodeID {
		// We already delivered this locally before publishing.
		return
	}
	if payload.RuntimeID == "" || n.local == nil {
		return
	}
	// Best-effort: if the daemon is not on this node the hub simply counts a
	// miss — the node that does hold the socket gets the same notification.
	n.local.notifyTaskAvailable(payload.RuntimeID, payload.TaskID, payload.EventID)
}
