package digest

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// journalEntry is one model attempt. It is appended (and the budget charged)
// before provider I/O; the response is saved before validation, so a crash
// anywhere after the save replays the same response with zero generations.
type journalEntry struct {
	Ordinal    int             `json:"ordinal"`
	RequestSHA string          `json:"request_sha256"`
	Model      string          `json:"model,omitempty"`
	Response   json.RawMessage `json:"response,omitempty"`
	Failure    string          `json:"failure,omitempty"`
	ReservedAt time.Time       `json:"reserved_at"`
	FinishedAt *time.Time      `json:"finished_at,omitempty"`
}

func (e journalEntry) settled() bool { return len(e.Response) > 0 || e.Failure != "" }

type runRow struct {
	ID       string
	PageHash string
	Calls    int
	Journal  []journalEntry
	Resumed  bool
}

type pageBounds struct {
	FromAt, ToAt time.Time
	FromID, ToID string
	Messages     int
	Human        int
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

// openRun resumes the scene's unfinished run when it read the same page, or
// closes it and opens a new one. Must run with the lease locked in tx.
func openRun(ctx context.Context, tx pgx.Tx, c claim, trigger, pageHash string, b pageBounds) (runRow, error) {
	if c.LastRunID != "" {
		var hash string
		var calls int
		var raw []byte
		err := tx.QueryRow(ctx, `SELECT page_hash,calls,model_journal FROM employee_scene_digest_run WHERE id=$1::uuid AND workspace_id=$2::uuid AND agent_id=$3::uuid AND scene_id=$4::uuid AND outcome IS NULL FOR UPDATE`, c.LastRunID, c.Key.WorkspaceID, c.Key.AgentID, c.Key.SceneID).Scan(&hash, &calls, &raw)
		switch {
		case err == nil && hash == pageHash && pageHash != "":
			var journal []journalEntry
			if err = json.Unmarshal(raw, &journal); err != nil {
				return runRow{}, err
			}
			if _, err = tx.Exec(ctx, `UPDATE employee_scene_digest_run SET lease_generation=$2 WHERE id=$1::uuid`, c.LastRunID, c.Generation); err != nil {
				return runRow{}, err
			}
			return runRow{ID: c.LastRunID, PageHash: hash, Calls: calls, Journal: journal, Resumed: true}, nil
		case err == nil:
			if _, err = tx.Exec(ctx, `UPDATE employee_scene_digest_run SET outcome='error',error='superseded: page changed',finished_at=now() WHERE id=$1::uuid AND outcome IS NULL`, c.LastRunID); err != nil {
				return runRow{}, err
			}
		case !errors.Is(err, pgx.ErrNoRows):
			return runRow{}, err
		}
	}
	id := uuid.NewString()
	if _, err := tx.Exec(ctx, `INSERT INTO employee_scene_digest_run(id,workspace_id,agent_id,tenant_org_id,scene_id,lease_generation,trigger,page_hash,cursor_from_at,cursor_from_id,cursor_to_at,cursor_to_id,page_messages,page_human,langfuse_trace_id)
VALUES($1::uuid,$2::uuid,$3::uuid,$4,$5::uuid,$6,$7,$8,$9::timestamptz,$10,$11::timestamptz,$12,$13,$14,$15)`,
		id, c.Key.WorkspaceID, c.Key.AgentID, c.Key.TenantOrgID, c.Key.SceneID, c.Generation, trigger, pageHash,
		nullTime(b.FromAt), clip(b.FromID, 256), nullTime(b.ToAt), clip(b.ToID, 256), b.Messages, b.Human, strings.ReplaceAll(id, "-", "")); err != nil {
		return runRow{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE employee_scene_digest_state SET last_run_id=$5::uuid,updated_at=now() WHERE `+stateKey, append(c.Key.args(), id)...); err != nil {
		return runRow{}, err
	}
	return runRow{ID: id, PageHash: pageHash}, nil
}

// errBudgetExhausted is returned by reserveCall when a daily cap is reached.
var errBudgetExhausted = errors.New("employee digest: daily model budget exhausted")

// budgetSQL is the Asia/Shanghai calendar day of the database clock.
const budgetDaySQL = `(now() AT TIME ZONE 'Asia/Shanghai')::date`

// reserveCall charges one call to the scene and agent daily budgets and
// appends a pending journal entry, before any provider I/O. The agent sum is
// serialized by a transaction-scoped advisory lock so two replicas cannot
// both take the last call.
func reserveCall(ctx context.Context, tx pgx.Tx, c claim, run *runRow, ordinal int, requestSHA string) error {
	if run.Calls >= MaxCallsPerClaim || ordinal >= MaxCallsPerClaim {
		return errBudgetExhausted
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('employee_scene_digest_budget:'||$1, 0))`, c.Key.AgentID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE employee_scene_digest_state SET budget_day=`+budgetDaySQL+`,budget_calls=0 WHERE `+stateKey+` AND budget_day IS DISTINCT FROM `+budgetDaySQL, c.Key.args()...); err != nil {
		return err
	}
	var sceneCalls, agentCalls int
	if err := tx.QueryRow(ctx, `SELECT budget_calls FROM employee_scene_digest_state WHERE `+stateKey, c.Key.args()...).Scan(&sceneCalls); err != nil {
		return err
	}
	if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(budget_calls),0) FROM employee_scene_digest_state WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND budget_day=`+budgetDaySQL, c.Key.WorkspaceID, c.Key.AgentID).Scan(&agentCalls); err != nil {
		return err
	}
	if sceneCalls >= SceneDailyCalls || agentCalls >= AgentDailyCalls {
		return errBudgetExhausted
	}
	entry := journalEntry{Ordinal: ordinal, RequestSHA: requestSHA, ReservedAt: time.Now().UTC()}
	raw, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE employee_scene_digest_state SET budget_calls=budget_calls+1,updated_at=now() WHERE `+stateKey, c.Key.args()...); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE employee_scene_digest_run SET calls=calls+1,model_journal=model_journal||jsonb_build_array($2::jsonb) WHERE id=$1::uuid AND outcome IS NULL`, run.ID, string(raw)); err != nil {
		return err
	}
	run.Calls++
	run.Journal = append(run.Journal, entry)
	return nil
}

// saveCall records the provider result of one reserved attempt.
func saveCall(ctx context.Context, tx pgx.Tx, run *runRow, ordinal int, model string, response json.RawMessage, failure string, promptTokens, completionTokens int64) error {
	if ordinal < 0 || ordinal >= len(run.Journal) {
		return ErrInvalid
	}
	now := time.Now().UTC()
	entry := run.Journal[ordinal]
	entry.Model, entry.Response, entry.Failure, entry.FinishedAt = clip(model, 256), response, clip(failure, 512), &now
	raw, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE employee_scene_digest_run SET model_journal=jsonb_set(model_journal,ARRAY[$2::text],$3::jsonb),
 model=CASE WHEN $4<>'' THEN $4 ELSE model END,prompt_tokens=prompt_tokens+$5,completion_tokens=completion_tokens+$6 WHERE id=$1::uuid AND outcome IS NULL`,
		run.ID, strconv.Itoa(ordinal), string(raw), clip(model, 256), promptTokens, completionTokens); err != nil {
		return err
	}
	run.Journal[ordinal] = entry
	return nil
}

type runResult struct {
	Outcome     string
	Error       string
	Proposed    int
	Accepted    int
	Rejected    []Rejection
	PageHash    string
	Bounds      *pageBounds
	TraceID     string
	ModelUsed   string
	FinishedNow bool
}

func finishRun(ctx context.Context, tx pgx.Tx, runID string, r runResult) error {
	rejected := boundRejections(r.Rejected)
	raw, err := json.Marshal(rejected)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE employee_scene_digest_run SET outcome=$2,error=$3,ops_proposed=$4,ops_accepted=$5,ops_rejected=$6::jsonb,finished_at=now() WHERE id=$1::uuid AND outcome IS NULL`,
		runID, r.Outcome, clip(r.Error, 512), r.Proposed, r.Accepted, string(raw))
	return err
}

// recordShortRun writes a finished run row for a claim that made no model
// request (trivial page, empty page, budget, block), so every claim leaves a
// journal row.
func recordShortRun(ctx context.Context, tx pgx.Tx, c claim, trigger, outcome, reason string, b pageBounds) (string, error) {
	id := uuid.NewString()
	_, err := tx.Exec(ctx, `INSERT INTO employee_scene_digest_run(id,workspace_id,agent_id,tenant_org_id,scene_id,lease_generation,trigger,cursor_from_at,cursor_from_id,cursor_to_at,cursor_to_id,page_messages,page_human,outcome,error,finished_at)
VALUES($1::uuid,$2::uuid,$3::uuid,$4,$5::uuid,$6,$7,$8::timestamptz,$9,$10::timestamptz,$11,$12,$13,$14,$15,now())`,
		id, c.Key.WorkspaceID, c.Key.AgentID, c.Key.TenantOrgID, c.Key.SceneID, c.Generation, trigger,
		nullTime(b.FromAt), clip(b.FromID, 256), nullTime(b.ToAt), clip(b.ToID, 256), b.Messages, b.Human, outcome, clip(reason, 512))
	return id, err
}

// pruneScene keeps per-scene retention bounded without a global scan: each
// commit deletes this scene's runs and ledger entries older than Retention.
func pruneScene(ctx context.Context, tx pgx.Tx, key SceneKey) error {
	if _, err := tx.Exec(ctx, `DELETE FROM employee_scene_digest_run WHERE id IN (SELECT id FROM employee_scene_digest_run WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND scene_id=$3::uuid AND started_at<now()-interval '30 days' AND outcome IS NOT NULL ORDER BY started_at LIMIT 100)`, key.WorkspaceID, key.AgentID, key.SceneID); err != nil {
		return err
	}
	return pruneLedger(ctx, tx, key)
}

func clip(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	s = s[:limit]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

func clipRunes(s string, n int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
