package service

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	asbCapacityWaitRetryDelay       = 30 * time.Second
	asbCapacityWaitRecoveryInterval = time.Minute
	asbCapacityStaleLaunchAge       = 2 * runtimeLaunchLeaseDuration
	asbCapacityWaitMaxConcurrent    = 4
)

type asbCapacityWaitCoordinator struct {
	mu       sync.Mutex
	inFlight map[string]struct{}
	wakeups  chan struct{}
	cursor   int
	running  int
}

func newASBCapacityWaitCoordinator() *asbCapacityWaitCoordinator {
	return &asbCapacityWaitCoordinator{
		inFlight: make(map[string]struct{}),
		wakeups:  make(chan struct{}, 1),
	}
}

func (coordinator *asbCapacityWaitCoordinator) tryStart(taskID string) bool {
	if coordinator == nil || taskID == "" {
		return false
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if _, exists := coordinator.inFlight[taskID]; exists {
		return false
	}
	if coordinator.running >= asbCapacityWaitMaxConcurrent {
		return false
	}
	coordinator.inFlight[taskID] = struct{}{}
	coordinator.running++
	return true
}

func (coordinator *asbCapacityWaitCoordinator) finish(taskID string) {
	if coordinator == nil || taskID == "" {
		return
	}
	coordinator.mu.Lock()
	if _, exists := coordinator.inFlight[taskID]; exists {
		delete(coordinator.inFlight, taskID)
		if coordinator.running > 0 {
			coordinator.running--
		}
	}
	coordinator.mu.Unlock()
}

func (coordinator *asbCapacityWaitCoordinator) nextStart(total int) int {
	if coordinator == nil || total <= 0 {
		return 0
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	start := coordinator.cursor % total
	coordinator.cursor = (start + 1) % total
	return start
}

func (coordinator *asbCapacityWaitCoordinator) notify() {
	if coordinator == nil {
		return
	}
	select {
	case coordinator.wakeups <- struct{}{}:
	default:
	}
}

func asbCapacityScopeID(scope ASBTenantCredentialScope) string {
	runtimeIDs := make([]string, 0, len(scope.RuntimeIDs))
	for _, runtimeID := range scope.RuntimeIDs {
		if runtimeID.Valid {
			runtimeIDs = append(runtimeIDs, util.UUIDToString(runtimeID))
		}
	}
	sort.Strings(runtimeIDs)
	return strings.Join(runtimeIDs, ",")
}

func (l *ASBLauncher) NotifyRuntimeCapacityMayBeAvailable() {
	if l != nil && l.CapacityWait != nil {
		l.CapacityWait.notify()
	}
}

// RunCapacityWaiter durably retries ASB tasks whose latest startup attempt was
// blocked only by tenant sandbox capacity. Terminal task transitions wake it
// immediately; the periodic pass repairs lost wakeups and abandoned launches.
// Every API replica may run this loop: the task launch lease elects one launcher
// per task, while the ASB tenant advisory lock serializes reclaim/create work.
func (l *ASBLauncher) RunCapacityWaiter(ctx context.Context) {
	if l == nil || l.Queries == nil || l.Tasks == nil || l.Credentials == nil {
		return
	}
	if l.CapacityWait == nil {
		l.CapacityWait = newASBCapacityWaitCoordinator()
	}
	slog.Info("ASB sandbox capacity waiter started",
		"event", "asb_capacity_waiter_started",
		"recovery_interval_ms", asbCapacityWaitRecoveryInterval.Milliseconds(),
	)
	defer slog.Info("ASB sandbox capacity waiter stopped",
		"event", "asb_capacity_waiter_stopped",
	)

	runOnce := func() {
		if _, err := l.retryCapacityWaitingTasks(ctx); err != nil && ctx.Err() == nil {
			slog.Error("ASB sandbox capacity waiter iteration failed",
				"event", "asb_capacity_waiter_failed",
				"error", err,
			)
		}
	}

	// Recover capacity waits left by an older replica immediately after startup.
	runOnce()
	ticker := time.NewTicker(asbCapacityWaitRecoveryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-l.CapacityWait.wakeups:
			runOnce()
		case <-ticker.C:
			runOnce()
		}
	}
}

func (l *ASBLauncher) retryCapacityWaitingTasks(ctx context.Context) (int, error) {
	if l == nil || l.Queries == nil || l.Tasks == nil || l.Credentials == nil {
		return 0, fmt.Errorf("ASB sandbox capacity waiter is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if l.CapacityWait == nil {
		l.CapacityWait = newASBCapacityWaitCoordinator()
	}
	waiting, err := l.Queries.ListASBCapacityWaitingTasks(
		ctx,
		db.ListASBCapacityWaitingTasksParams{
			RetrySeconds:  asbCapacityWaitRetryDelay.Seconds(),
			StaleSeconds:  asbCapacityStaleLaunchAge.Seconds(),
			MaxPerRuntime: asbCapacityWaitMaxConcurrent,
		},
	)
	if err != nil {
		return 0, fmt.Errorf("list ASB capacity-waiting tasks: %w", err)
	}
	sort.SliceStable(waiting, func(i, j int) bool {
		if !waiting[i].CreatedAt.Time.Equal(waiting[j].CreatedAt.Time) {
			return waiting[i].CreatedAt.Time.Before(waiting[j].CreatedAt.Time)
		}
		return util.UUIDToString(waiting[i].ID) < util.UUIDToString(waiting[j].ID)
	})

	// Resolve exact credential scopes, then round-robin their FIFO batches.
	// A tenant may use otherwise idle launch slots while earlier sandboxes boot;
	// the PostgreSQL tenant lock still serializes quota checks/reclaim/create.
	scopeByRuntime := make(map[string]ASBTenantCredentialScope)
	tasksByScope := make(map[string][]db.AgentTaskQueue)
	runtimeCountByScope := make(map[string]int)
	var scopeOrder []string
	for _, task := range waiting {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		runtimeKey := util.UUIDToString(task.RuntimeID)
		scope, cached := scopeByRuntime[runtimeKey]
		scopeID := ""
		if !cached {
			scope, err = l.Credentials.RuntimeCredentialScope(ctx, task.RuntimeID)
			if err != nil {
				if ctx.Err() != nil {
					return 0, ctx.Err()
				}
				slog.Warn("ASB capacity waiter could not resolve tenant scope",
					"task_id", util.UUIDToString(task.ID),
					"runtime_id", runtimeKey,
					"error", err,
				)
				continue
			} else {
				for _, runtimeID := range scope.RuntimeIDs {
					scopeByRuntime[util.UUIDToString(runtimeID)] = scope
				}
			}
		}
		if scopeID == "" {
			scopeID = asbCapacityScopeID(scope)
		}
		if scopeID == "" {
			continue
		}
		if _, exists := tasksByScope[scopeID]; !exists {
			scopeOrder = append(scopeOrder, scopeID)
			runtimeCountByScope[scopeID] = len(scope.RuntimeIDs)
		}
		tasksByScope[scopeID] = append(tasksByScope[scopeID], task)
	}
	scheduled := 0
	start := l.CapacityWait.nextStart(len(scopeOrder))
	for round := 0; ; round++ {
		hasCandidates := false
		for offset := range len(scopeOrder) {
			if err := ctx.Err(); err != nil {
				return scheduled, err
			}
			scopeID := scopeOrder[(start+offset)%len(scopeOrder)]
			batch := tasksByScope[scopeID]
			if round >= len(batch) {
				continue
			}
			hasCandidates = true
			task := batch[round]
			taskID := util.UUIDToString(task.ID)
			if !l.CapacityWait.tryStart(taskID) {
				continue
			}
			l.Tasks.notifyTaskAvailable(task)
			l.Tasks.launchRuntimeForTaskWithCompletion(task, func() {
				l.CapacityWait.finish(taskID)
				// Continue draining eligible waiters without waiting for the next
				// recovery tick. Fresh full/429 waits retain their retry deadline.
				l.CapacityWait.notify()
			})
			scheduled++
			slog.Info("ASB capacity waiter scheduled queued task",
				"event", "asb_capacity_waiter_task_scheduled",
				"task_id", taskID,
				"runtime_id", util.UUIDToString(task.RuntimeID),
				"tenant_runtime_count", runtimeCountByScope[scopeID],
			)
			if scheduled >= asbCapacityWaitMaxConcurrent {
				return scheduled, nil
			}
		}
		if !hasCandidates {
			break
		}
	}
	return scheduled, nil
}
