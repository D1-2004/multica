package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const (
	// pendingReportsFileName is the on-disk queue of terminal task callbacks
	// (complete/fail) that exhausted their inline retry schedule. It lives
	// next to daemon.id in the profile dir so it survives daemon restarts.
	pendingReportsFileName = "pending_reports.json"

	// pendingReportTTL bounds how long a queued report is retried. A task
	// unreachable for this long has already been finalized by the server-side
	// stale-task sweeper (runningTimeoutSeconds = 2.5h), so redelivering the
	// original result past that point only produces "already finalized" noise.
	// 24h is generous headroom above the sweeper while still guaranteeing the
	// queue drains itself.
	pendingReportTTL = 24 * time.Hour

	// pendingReportMaxEntries caps the queue so a pathological loop can't grow
	// the file without bound. At one terminal report per task run, 200 covers
	// far more concurrent runs than a single daemon ever executes.
	pendingReportMaxEntries = 200

	// pendingReportsDrainInterval is how often the background loop attempts
	// redelivery while the queue is non-empty. Each drain pass stops at the
	// first transient failure, so during an extended server outage the loop
	// costs one round of CompleteTask/FailTask backoff per interval.
	pendingReportsDrainInterval = 60 * time.Second

	// pendingReportKindComplete / pendingReportKindFail discriminate which
	// terminal callback a queued entry replays.
	pendingReportKindComplete = "complete"
	pendingReportKindFail     = "fail"
)

// pendingTerminalReport is one CompleteTask/FailTask callback whose inline
// retry schedule was exhausted while the server was unreachable or degraded.
//
// This is the "daemon-side persistent pending queue" that reportTaskResult's
// leave-in-running path always assumed would exist: without it, a lost
// complete strands the task in running (and its issue in in_progress) until
// the server's coarse 2.5h sweeper fails it — discarding the agent's actual
// result. The queue preserves the true terminal state across daemon restarts
// and replays it once the server is reachable again; the server treats
// "already terminal" as idempotent success, so duplicate replays are safe.
type pendingTerminalReport struct {
	Kind          string    `json:"kind"`
	TaskID        string    `json:"task_id"`
	Output        string    `json:"output,omitempty"`
	BranchName    string    `json:"branch_name,omitempty"`
	Error         string    `json:"error,omitempty"`
	FailureReason string    `json:"failure_reason,omitempty"`
	SessionID     string    `json:"session_id,omitempty"`
	WorkDir       string    `json:"work_dir,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	Attempts      int       `json:"attempts"`
}

// pendingReportStore is a small disk-backed queue keyed by task ID. All
// mutations persist synchronously; an empty path degrades to memory-only
// (used when the profile dir is unavailable, and by tests).
type pendingReportStore struct {
	mu      sync.Mutex
	path    string
	reports []pendingTerminalReport
}

// loadPendingReportStore reads the queue file at path, tolerating a missing
// or corrupt file (a corrupt queue is dropped with a warning rather than
// blocking daemon startup — the server sweeper remains the backstop for
// whatever it contained).
func loadPendingReportStore(path string, logger *slog.Logger) *pendingReportStore {
	s := &pendingReportStore{path: path}
	if path == "" {
		return s
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) && logger != nil {
			logger.Warn("pending reports: read queue file failed", "path", path, "error", err)
		}
		return s
	}
	if err := json.Unmarshal(data, &s.reports); err != nil {
		if logger != nil {
			logger.Warn("pending reports: corrupt queue file dropped", "path", path, "error", err)
		}
		s.reports = nil
	}
	return s
}

// Enqueue adds a report, replacing any existing entry for the same task —
// one task has exactly one terminal state, so the latest report wins.
func (s *pendingReportStore) Enqueue(r pendingTerminalReport) {
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now().UTC()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removeLocked(r.TaskID)
	s.reports = append(s.reports, r)
	if len(s.reports) > pendingReportMaxEntries {
		s.reports = s.reports[len(s.reports)-pendingReportMaxEntries:]
	}
	s.persistLocked()
}

// Remove deletes the entry for taskID, if any.
func (s *pendingReportStore) Remove(taskID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removeLocked(taskID)
	s.persistLocked()
}

// Update replaces the entry for r.TaskID in place (attempt bump or
// complete→fail conversion) without changing its queue position.
func (s *pendingReportStore) Update(r pendingTerminalReport) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.reports {
		if s.reports[i].TaskID == r.TaskID {
			s.reports[i] = r
			break
		}
	}
	s.persistLocked()
}

// Snapshot returns the queued reports ordered oldest-first.
func (s *pendingReportStore) Snapshot() []pendingTerminalReport {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]pendingTerminalReport, len(s.reports))
	copy(out, s.reports)
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// Len returns the number of queued reports.
func (s *pendingReportStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.reports)
}

func (s *pendingReportStore) removeLocked(taskID string) {
	kept := s.reports[:0]
	for _, r := range s.reports {
		if r.TaskID != taskID {
			kept = append(kept, r)
		}
	}
	s.reports = kept
}

// persistLocked writes the queue atomically (tmp+rename, 0600 — task output
// can contain user content). Persistence failures are deliberately silent at
// this layer: the in-memory queue still drives redelivery for the lifetime of
// this process, which is strictly better than not queueing at all.
func (s *pendingReportStore) persistLocked() {
	if s.path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return
	}
	data, err := json.Marshal(s.reports)
	if err != nil {
		return
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, s.path)
}

// queueTerminalReport enqueues a lost terminal callback for background
// redelivery. Safe on a Daemon constructed without a store (tests).
func (d *Daemon) queueTerminalReport(r pendingTerminalReport, taskLog *slog.Logger) {
	if d.pendingReports == nil {
		return
	}
	d.pendingReports.Enqueue(r)
	taskLog.Info("terminal report queued for redelivery",
		"kind", r.Kind, "queue_len", d.pendingReports.Len())
}

// pendingReportsLoop drains the persistent queue shortly after startup (the
// daemon-restart recovery path) and then on a fixed interval for reports
// queued while running.
func (d *Daemon) pendingReportsLoop(ctx context.Context) {
	if d.pendingReports == nil {
		return
	}
	// Small settle delay so startup drains don't race registration.
	select {
	case <-ctx.Done():
		return
	case <-time.After(5 * time.Second):
	}
	d.drainPendingReports(ctx)

	ticker := time.NewTicker(pendingReportsDrainInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.drainPendingReports(ctx)
		}
	}
}

// drainPendingReports replays queued reports oldest-first. A transient
// failure aborts the pass (the server is unhealthy; later entries would burn
// their own full retry schedules for nothing) — the next tick resumes.
func (d *Daemon) drainPendingReports(ctx context.Context) {
	for _, r := range d.pendingReports.Snapshot() {
		if ctx.Err() != nil {
			return
		}
		log := d.logger.With("task", shortID(r.TaskID), "kind", r.Kind, "attempts", r.Attempts)
		if time.Since(r.CreatedAt) > pendingReportTTL {
			log.Warn("pending reports: entry expired without successful redelivery; dropping")
			d.pendingReports.Remove(r.TaskID)
			continue
		}

		// nil schedule = single attempt: the drain ticker is the retry loop,
		// so nesting the inline terminal backoff here would only multiply it.
		var err error
		switch r.Kind {
		case pendingReportKindComplete:
			err = d.client.completeTaskWithSchedule(ctx, r.TaskID, r.Output, r.BranchName, r.SessionID, r.WorkDir, nil)
		case pendingReportKindFail:
			err = d.client.failTaskWithSchedule(ctx, r.TaskID, r.Error, r.SessionID, r.WorkDir, r.FailureReason, nil)
		default:
			log.Warn("pending reports: unknown kind; dropping")
			d.pendingReports.Remove(r.TaskID)
			continue
		}

		switch {
		case err == nil:
			log.Info("pending reports: redelivered")
			d.pendingReports.Remove(r.TaskID)
		case isTransientError(err):
			r.Attempts++
			d.pendingReports.Update(r)
			log.Info("pending reports: server still unreachable; will retry", "error", err)
			return
		case r.Kind == pendingReportKindComplete:
			// Permanent rejection of a complete mirrors reportTaskResult's
			// inline fallback: the server refused the result, so the only
			// useful signal left is a concrete failure.
			log.Warn("pending reports: complete rejected by server; converting to fail", "error", err)
			r.Kind = pendingReportKindFail
			r.Error = fmt.Sprintf("complete task failed: %s", err.Error())
			r.FailureReason = "agent_error.unknown"
			r.Attempts++
			d.pendingReports.Update(r)
		default:
			// Permanent rejection of a fail: the server has refused both
			// terminal shapes (or the task row is gone). Nothing further to
			// deliver.
			log.Warn("pending reports: fail rejected by server; dropping", "error", err)
			d.pendingReports.Remove(r.TaskID)
		}
	}
}
