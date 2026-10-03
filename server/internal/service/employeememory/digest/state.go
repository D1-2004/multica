package digest

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const stateKey = `workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND scene_id=$4::uuid`

func (k SceneKey) args() []any { return []any{k.WorkspaceID, k.AgentID, k.TenantOrgID, k.SceneID} }

func (k SceneKey) valid() bool {
	for _, id := range []string{k.WorkspaceID, k.AgentID, k.SceneID} {
		if _, err := uuid.Parse(id); err != nil {
			return false
		}
	}
	org := strings.TrimSpace(k.TenantOrgID)
	return org != "" && org == k.TenantOrgID && len(org) <= 128
}

// dueSQL schedules a scene from its current row: the threshold path debounces
// on observation time and is bounded by MaxWait from the first undigested
// line; otherwise the segment closes SegmentIdle after the last human line or
// wake. A backoff or budget hold is never bypassed.
const dueSQL = `GREATEST(
 CASE WHEN pending_human >= 12
  THEN LEAST(now() + interval '60 seconds', COALESCE(first_pending_at, now()) + interval '10 minutes')
  ELSE GREATEST(COALESCE(last_human_at, now()), COALESCE(last_activity_at, '-infinity'::timestamptz)) + interval '30 minutes'
 END,
 COALESCE(hold_until, '-infinity'::timestamptz))`

// MarkSceneDirtyTx records newly persisted human transcript lines in the
// caller's insert transaction. It is the only hook M8 calls; it never reads
// the transcript, calls a model or blocks on another scene. humanRows must
// count only rows actually inserted, so redelivery cannot inflate it; the
// count is a scheduling hint and the claim re-reads the transcript by cursor.
// A new human line also unblocks a blocked scene (one more attempt).
func MarkSceneDirtyTx(ctx context.Context, tx Execer, key SceneKey, humanRows int, lastHumanAt time.Time) error {
	if tx == nil || !key.valid() || humanRows < 0 {
		return ErrInvalid
	}
	if humanRows == 0 {
		return nil
	}
	if humanRows > 10000 {
		humanRows = 10000
	}
	if lastHumanAt.IsZero() {
		lastHumanAt = time.Now()
	}
	if _, err := tx.Exec(ctx, `INSERT INTO employee_scene_digest_state(workspace_id,agent_id,tenant_org_id,scene_id) VALUES($1::uuid,$2::uuid,$3,$4::uuid) ON CONFLICT (workspace_id,agent_id,scene_id) DO NOTHING`, key.args()...); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE employee_scene_digest_state SET dirty_revision=dirty_revision+1,
 pending_human=LEAST(pending_human+$5,1000000), first_pending_at=COALESCE(first_pending_at,now()),
 last_human_at=GREATEST(COALESCE(last_human_at,'-infinity'::timestamptz),LEAST($6::timestamptz,now())),
 blocked_at=NULL, blocked_reason='', updated_at=now() WHERE `+stateKey, append(key.args(), humanRows, lastHumanAt)...); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE employee_scene_digest_state SET due_at=`+dueSQL+` WHERE `+stateKey+` AND pending_human>0`, key.args()...)
	return err
}

// TouchSceneActivityTx marks a finished Employee wake: the conversation is
// still active, so an open segment's close moves out. Scenes without
// observed transcript (no state row) are untouched.
func TouchSceneActivityTx(ctx context.Context, tx Execer, key SceneKey) error {
	if tx == nil || !key.valid() {
		return ErrInvalid
	}
	if _, err := tx.Exec(ctx, `UPDATE employee_scene_digest_state SET last_activity_at=now(),updated_at=now() WHERE `+stateKey, key.args()...); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE employee_scene_digest_state SET due_at=`+dueSQL+` WHERE `+stateKey+` AND pending_human>0 AND pending_human<12`, key.args()...)
	return err
}

// claim is one leased scene. Token plus Generation fence every later write:
// a replica whose lease expired and was re-claimed elsewhere cannot commit.
type claim struct {
	Key           SceneKey
	Token         string
	Generation    int64
	Pending       int
	DirtyRevision int64
	CursorAt      time.Time
	CursorID      string
	LastRunID     string
	NoProgress    int
	Trigger       string
}

func claimNext(ctx context.Context, conn DB) (claim, error) {
	token := uuid.NewString()
	var c claim
	var cursorAt *time.Time
	err := conn.QueryRow(ctx, `UPDATE employee_scene_digest_state s SET lease_token=$1::uuid, lease_until=now()+interval '3 minutes', generation=s.generation+1, updated_at=now()
FROM (SELECT workspace_id,agent_id,scene_id FROM employee_scene_digest_state
 WHERE blocked_at IS NULL AND pending_human>0 AND due_at<=now() AND (lease_until IS NULL OR lease_until<now())
 ORDER BY due_at LIMIT 1 FOR UPDATE SKIP LOCKED) c
WHERE s.workspace_id=c.workspace_id AND s.agent_id=c.agent_id AND s.scene_id=c.scene_id
RETURNING s.workspace_id::text,s.agent_id::text,s.tenant_org_id,s.scene_id::text,s.generation,s.pending_human,s.dirty_revision,
 s.cursor_sent_at,s.cursor_message_id,COALESCE(s.last_run_id::text,''),s.no_progress_count,
 CASE WHEN s.pending_human>=12 THEN 'threshold'
  WHEN GREATEST(COALESCE(s.last_human_at,'-infinity'::timestamptz),COALESCE(s.last_activity_at,'-infinity'::timestamptz))+interval '30 minutes'<=now() THEN 'segment_close'
  ELSE 'catch_up' END`, token).Scan(&c.Key.WorkspaceID, &c.Key.AgentID, &c.Key.TenantOrgID, &c.Key.SceneID, &c.Generation, &c.Pending, &c.DirtyRevision, &cursorAt, &c.CursorID, &c.LastRunID, &c.NoProgress, &c.Trigger)
	if err != nil {
		return claim{}, err
	}
	c.Token = token
	if cursorAt != nil {
		c.CursorAt = *cursorAt
	}
	return c, nil
}

// lockLease re-checks the claim inside tx and holds the state row.
func lockLease(ctx context.Context, tx pgx.Tx, c claim) error {
	var generation int64
	err := tx.QueryRow(ctx, `SELECT generation FROM employee_scene_digest_state WHERE `+stateKey+` AND lease_token=$5::uuid FOR UPDATE`, append(c.Key.args(), c.Token)...).Scan(&generation)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && generation != c.Generation {
		return ErrLeaseLost
	}
	return err
}

// settle is how a claim ends: the cursor and counters it leaves behind.
type settle struct {
	Advance       bool
	CursorAt      time.Time
	CursorID      string
	ConsumedHuman int
	MorePages     bool
	Progress      bool
	// Neutral leaves the no-progress streak unchanged (budget holds).
	Neutral bool
	// HoldSeconds > 0 backs off; holdNextDay waits for the budget day.
	HoldSeconds int64
	BlockReason string
	RunID       string
}

const holdNextDay int64 = -1

// release ends a claim in tx (lease already locked). Progress resets the
// no-progress streak; its absence counts toward BlockAfterNoProgress.
func release(ctx context.Context, tx pgx.Tx, c claim, s settle) (blocked bool, err error) {
	noProgress := 0
	switch {
	case s.Neutral:
		noProgress = c.NoProgress
	case !s.Progress:
		noProgress = c.NoProgress + 1
	}
	block := s.BlockReason
	if block == "" && noProgress >= BlockAfterNoProgress {
		block = "no_progress"
	}
	var cursorAt any
	cursorID := c.CursorID
	if !c.CursorAt.IsZero() {
		cursorAt = c.CursorAt
	}
	if s.Advance && !s.CursorAt.IsZero() {
		cursorAt, cursorID = s.CursorAt, s.CursorID
	}
	var runID any
	if s.RunID != "" {
		runID = s.RunID
	}
	args := append(c.Key.args(), cursorAt, cursorID, s.ConsumedHuman, s.MorePages, noProgress, block, s.HoldSeconds, c.DirtyRevision, runID, s.Advance || s.ConsumedHuman > 0)
	if _, err = tx.Exec(ctx, `UPDATE employee_scene_digest_state SET
 cursor_sent_at=$5::timestamptz, cursor_message_id=$6,
 pending_human=CASE WHEN $8::bool THEN GREATEST(pending_human-$7,1) ELSE GREATEST(pending_human-$7,0) END,
 first_pending_at=CASE WHEN $7>0 THEN CASE WHEN $8::bool OR pending_human-$7>0 THEN now() ELSE NULL END ELSE first_pending_at END,
 digested_revision=CASE WHEN $14::bool THEN GREATEST(digested_revision,$12::bigint) ELSE digested_revision END, no_progress_count=$9,
 blocked_at=CASE WHEN $10<>'' THEN now() ELSE NULL END, blocked_reason=$10,
 hold_until=CASE WHEN $11::bigint>0 THEN now()+make_interval(secs=>$11::bigint)
  WHEN $11::bigint<0 THEN (((now() AT TIME ZONE 'Asia/Shanghai')::date+1)::timestamp AT TIME ZONE 'Asia/Shanghai')
  ELSE NULL END,
 last_run_id=COALESCE($13::uuid,last_run_id),
 lease_token=NULL, lease_until=NULL, updated_at=now() WHERE `+stateKey, args...); err != nil {
		return false, err
	}
	if s.MorePages {
		_, err = tx.Exec(ctx, `UPDATE employee_scene_digest_state SET due_at=GREATEST(now(),COALESCE(hold_until,'-infinity'::timestamptz)) WHERE `+stateKey, c.Key.args()...)
	} else {
		_, err = tx.Exec(ctx, `UPDATE employee_scene_digest_state SET due_at=`+dueSQL+` WHERE `+stateKey+` AND pending_human>0`, c.Key.args()...)
	}
	return block != "", err
}
