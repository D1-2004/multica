package scenememory

import (
	"encoding/json"
	"time"
)

// historyProgress belongs to a dirty revision, not to a worker or a lease.
// CommitBatch stores it atomically with the memory and monotonic source cursor.
// A new inbound revision restarts from pending_from so late messages survive.
type historyProgress struct {
	DirtyRevision int64     `json:"dirty_revision"`
	After         time.Time `json:"after"`
	CutoffSeen    bool      `json:"cutoff_seen"`
	TriggerSeen   bool      `json:"trigger_seen"`
	PendingSeen   bool      `json:"pending_seen"`
}

func claimedDirtyRevision(row Memory) int64 {
	if row.LeaseTargetDirtyRevision.Valid {
		return row.LeaseTargetDirtyRevision.Int64
	}
	return row.DirtyRevision
}

func storedHistoryProgress(row Memory) (historyProgress, bool) {
	var meta struct {
		Progress *historyProgress `json:"history_progress"`
	}
	if json.Unmarshal(row.LastFlushMeta, &meta) != nil || meta.Progress == nil ||
		meta.Progress.After.IsZero() || meta.Progress.DirtyRevision <= 0 {
		return historyProgress{}, false
	}
	return *meta.Progress, true
}

func restoredHistoryProgress(row Memory) (historyProgress, bool) {
	progress, ok := storedHistoryProgress(row)
	return progress, ok && progress.DirtyRevision == claimedDirtyRevision(row)
}
