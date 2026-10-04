package handler

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/contextcap"
)

// normalizeRoutineOnce resolves an explicit instant, never a recurring rule.
// Keeping fractional seconds prevents relative reminders drifting on retry.
func normalizeRoutineOnce(raw, timezone string, now time.Time) (string, string, error) {
	at, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(raw))
	if err != nil || at.Year() < 1 || at.Year() > 9999 {
		return "", "", routineInvalid("run_at must be an RFC3339 timestamp with an explicit offset")
	}
	if !at.After(now) {
		return "", "", routineInvalid("run_at must be in the future; do not replace a past reminder with a repeating cron")
	}
	timezone = strings.TrimSpace(timezone)
	if timezone == "" {
		timezone = sceneRoutineDefaultTimezone
	}
	if _, err := time.LoadLocation(timezone); err != nil {
		return "", "", routineInvalid("invalid timezone")
	}
	return at.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano), timezone, nil
}

// lockRoutineMutation matches admission, making cancel/edit linearizable.
func lockRoutineMutation(ctx context.Context, tx pgx.Tx, routine contextcap.Routine) error {
	var id string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM workspace WHERE id=$1::uuid FOR KEY SHARE`, routine.WorkspaceID).Scan(&id); err != nil {
		return err
	}
	if err := tx.QueryRow(ctx, `SELECT id::text FROM context_scope_routine WHERE id=$1::uuid AND workspace_id=$2::uuid AND agent_id=$3::uuid FOR UPDATE`, routine.ID, routine.WorkspaceID, routine.AgentID).Scan(&id); err != nil {
		return err
	}
	return nil
}

// Replay returns the persisted resource without editing or rearming it.
func (h *Handler) replayOnceRoutine(ctx context.Context, routine contextcap.Routine) (sceneRoutineResult, error) {
	view, err := h.sceneRoutineView(ctx, routine, nil, nil)
	if isRoutineGone(err) {
		return sceneRoutineResult{}, routineRefusal(http.StatusConflict, "once_cancelled", "this one-shot was cancelled; a creation retry cannot reactivate it")
	}
	if err != nil {
		return sceneRoutineResult{}, err
	}
	message := fmt.Sprintf("这个一次性定时任务「%s」已存在，没有重复创建。", view.Title)
	if view.Trigger.Consumed {
		message += "它已受理执行，不会再次触发。"
	}
	return sceneRoutineResult{Routine: view, Updated: true, TellTheHuman: message}, nil
}
