package digest

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"time"
)

// Maintain is the zero-model periodic pass (GawkBot's 10-minute reconcile
// cadence). Decay stays read-time (effective confidence); this pass only
// retracts the writer's own outputs that are stale, superseded by a member's
// record of the same subject, or over the per-scene cap. Each scene is
// claimed with SKIP LOCKED and skipped while a digest lease is live.
func (w *Writer) Maintain(ctx context.Context, limit int) (int, error) {
	if !w.Enabled() || limit <= 0 {
		return 0, nil
	}
	if err := w.Ready(ctx); err != nil {
		return 0, nil
	}
	rows, err := w.DB.Query(ctx, `UPDATE employee_scene_digest_state s SET maintained_at=now()
FROM (SELECT workspace_id,agent_id,scene_id FROM employee_scene_digest_state
 WHERE maintained_at<now()-interval '10 minutes' AND (lease_until IS NULL OR lease_until<now())
 ORDER BY maintained_at LIMIT $1 FOR UPDATE SKIP LOCKED) c
WHERE s.workspace_id=c.workspace_id AND s.agent_id=c.agent_id AND s.scene_id=c.scene_id
RETURNING s.workspace_id::text,s.agent_id::text,s.tenant_org_id,s.scene_id::text`, limit)
	if err != nil {
		return 0, err
	}
	var keys []SceneKey
	for rows.Next() {
		var k SceneKey
		if err = rows.Scan(&k.WorkspaceID, &k.AgentID, &k.TenantOrgID, &k.SceneID); err != nil {
			rows.Close()
			return 0, err
		}
		keys = append(keys, k)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return 0, err
	}
	retired := 0
	var failures []error
	for _, key := range keys {
		n, err := w.maintainScene(ctx, key)
		retired += n
		if err != nil {
			if ctx.Err() != nil {
				return retired, ctx.Err()
			}
			failures = append(failures, err)
		}
	}
	return retired, errors.Join(failures...)
}

// retireReasons decides which own outputs to retract. Pure, for tests.
func retireReasons(facts []Fact, actor string, now time.Time) map[string]string {
	out := map[string]string{}
	member := map[string]bool{}
	var own []Fact
	for _, f := range facts {
		if f.CaptureOrigin == OriginFlush && f.CreatedBy == actor {
			own = append(own, f)
			continue
		}
		member[f.Type+"\x00"+f.Key] = true
	}
	sort.SliceStable(own, func(i, j int) bool { return own[i].CreatedAt.After(own[j].CreatedAt) })
	kept := 0
	for _, f := range own {
		switch {
		case member[f.Type+"\x00"+f.Key]:
			out[f.ID] = "member_record"
		case f.Type == KindOpenItem && now.Sub(f.CreatedAt) > OpenItemTTL:
			out[f.ID] = "open_item_expired"
		case kept >= MaxActiveFlushItems:
			out[f.ID] = "capacity"
		default:
			kept++
		}
	}
	return out
}

func (w *Writer) maintainScene(ctx context.Context, key SceneKey) (int, error) {
	tx, err := w.DB.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	reason, err := w.Fence(ctx, tx, key)
	if err != nil || reason != "" {
		return 0, err
	}
	facts, err := w.Facts.ActiveFacts(ctx, tx, key, 200)
	if err != nil {
		return 0, err
	}
	actor := FlushActor(key.AgentID)
	retire := retireReasons(facts, actor, time.Now())
	retired := 0
	for id, why := range retire {
		sp, err := tx.Begin(ctx)
		if err != nil {
			return 0, err
		}
		if err = w.Facts.Retract(ctx, sp, key, id, actor); err != nil {
			_ = sp.Rollback(ctx)
			var refused *OpRejectedError
			if errors.As(err, &refused) {
				continue
			}
			return 0, err
		}
		if err = sp.Commit(ctx); err != nil {
			return 0, err
		}
		retired++
		slog.InfoContext(ctx, "employee scene digest retired", "event", "employee_scene_digest_retired", "scene_id", key.SceneID, "agent_id", key.AgentID, "learning_id", id, "reason", why)
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	return retired, nil
}
