package digest

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Ledger entry kinds.
const (
	LedgerWake         = "wake"
	LedgerTaskTerminal = "task_terminal"
)

// Ledger body bounds (GawkBot task_ledger.go keeps said <= 600).
const (
	ledgerRequestRunes = 200
	ledgerReplyRunes   = 600
	ledgerResultRunes  = 600
	ledgerListCap      = 10
	ledgerMaxBytes     = 8000
)

// LedgerRequest is one source message of a wake: who asked what.
type LedgerRequest struct {
	RequesterRef string `json:"requester_ref,omitempty"`
	SpeakerName  string `json:"speaker_name,omitempty"`
	MessageID    string `json:"message_id,omitempty"`
	Text         string `json:"text,omitempty"`
}

// LedgerTool is one tool call of a wake and whether the Host executed it.
type LedgerTool struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Receipt string `json:"receipt,omitempty"`
}

// LedgerBody is assembled from Host-observable facts only.
type LedgerBody struct {
	Version  int             `json:"v"`
	JobID    string          `json:"job_id,omitempty"`
	JobKind  string          `json:"job_kind,omitempty"`
	WakeKind string          `json:"wake_kind,omitempty"`
	Requests []LedgerRequest `json:"requests,omitempty"`
	// Outcome: reply | quiet | dispatched | waiting | failed.
	Outcome string `json:"outcome"`
	// Failure is a category, never the raw provider error text.
	Failure        string       `json:"failure,omitempty"`
	Rescue         string       `json:"rescue,omitempty"`
	Reply          string       `json:"reply,omitempty"`
	Tools          []LedgerTool `json:"tools,omitempty"`
	SendActionIDs  []string     `json:"send_action_ids,omitempty"`
	TaskRefs       []string     `json:"task_refs,omitempty"`
	NoDurableTrace bool         `json:"no_durable_trace,omitempty"`
	// Task terminal fields.
	TaskID       string `json:"task_id,omitempty"`
	Goal         string `json:"goal,omitempty"`
	TaskState    string `json:"task_state,omitempty"`
	RunState     string `json:"run_state,omitempty"`
	RunResult    string `json:"run_result,omitempty"`
	Verification string `json:"verification,omitempty"`
	RequesterRef string `json:"requester_ref,omitempty"`
}

// LedgerEntry is one scene journal row.
type LedgerEntry struct {
	ID         string     `json:"id,omitempty"`
	Kind       string     `json:"kind"`
	SourceID   string     `json:"source_id"`
	OccurredAt time.Time  `json:"occurred_at"`
	Body       LedgerBody `json:"body"`
}

// boundLedger clips texts and lists so one entry stays within the column
// bound; the last resort drops free text, never identities.
func boundLedger(b LedgerBody) LedgerBody {
	b.Version = 1
	if len(b.Requests) > ledgerListCap {
		b.Requests = b.Requests[len(b.Requests)-ledgerListCap:]
	}
	for i := range b.Requests {
		b.Requests[i].Text = clipRunes(b.Requests[i].Text, ledgerRequestRunes)
		b.Requests[i].SpeakerName = clip(b.Requests[i].SpeakerName, 128)
		b.Requests[i].RequesterRef = clip(b.Requests[i].RequesterRef, 256)
		b.Requests[i].MessageID = clip(b.Requests[i].MessageID, 256)
	}
	if len(b.Tools) > 2*ledgerListCap {
		b.Tools = b.Tools[:2*ledgerListCap]
	}
	for i := range b.Tools {
		b.Tools[i].Name = clip(b.Tools[i].Name, 64)
		b.Tools[i].Receipt = clip(b.Tools[i].Receipt, 128)
	}
	if len(b.SendActionIDs) > ledgerListCap {
		b.SendActionIDs = b.SendActionIDs[:ledgerListCap]
	}
	if len(b.TaskRefs) > ledgerListCap {
		b.TaskRefs = b.TaskRefs[:ledgerListCap]
	}
	b.Reply = clipRunes(b.Reply, ledgerReplyRunes)
	b.Goal = clipRunes(b.Goal, ledgerRequestRunes)
	b.RunResult = clipRunes(b.RunResult, ledgerResultRunes)
	b.Failure, b.Rescue = clip(b.Failure, 64), clip(b.Rescue, 64)
	if raw, err := json.Marshal(b); err == nil && len(raw) > ledgerMaxBytes {
		b.Reply, b.RunResult = clipRunes(b.Reply, 120), clipRunes(b.RunResult, 120)
		for i := range b.Requests {
			b.Requests[i].Text = clipRunes(b.Requests[i].Text, 60)
		}
	}
	return b
}

// AppendLedgerTx inserts one entry in the caller's transaction. A replayed
// job or a repeated Task scan inserts nothing. It reports whether a row was
// added; each insert also prunes this scene's entries past Retention.
func AppendLedgerTx(ctx context.Context, tx Execer, key SceneKey, e LedgerEntry) (bool, error) {
	if tx == nil || !key.valid() || (e.Kind != LedgerWake && e.Kind != LedgerTaskTerminal) || strings.TrimSpace(e.SourceID) == "" || len(e.SourceID) > 160 {
		return false, ErrInvalid
	}
	if e.OccurredAt.IsZero() {
		e.OccurredAt = time.Now()
	}
	raw, err := json.Marshal(boundLedger(e.Body))
	if err != nil {
		return false, err
	}
	if len(raw) > ledgerMaxBytes+150 {
		return false, ErrInvalid
	}
	tag, err := tx.Exec(ctx, `INSERT INTO employee_scene_ledger(workspace_id,agent_id,tenant_org_id,scene_id,entry_kind,source_id,occurred_at,entry)
VALUES($1::uuid,$2::uuid,$3,$4::uuid,$5,$6,$7,$8::jsonb) ON CONFLICT (workspace_id,agent_id,scene_id,entry_kind,source_id) DO NOTHING`,
		append(key.args(), e.Kind, e.SourceID, e.OccurredAt, string(raw))...)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	return true, pruneLedger(ctx, tx, key)
}

func pruneLedger(ctx context.Context, tx Execer, key SceneKey) error {
	_, err := tx.Exec(ctx, `DELETE FROM employee_scene_ledger WHERE ctid=ANY(ARRAY(SELECT ctid FROM employee_scene_ledger WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND scene_id=$3::uuid AND occurred_at<now()-interval '30 days' ORDER BY occurred_at LIMIT 50))`, key.WorkspaceID, key.AgentID, key.SceneID)
	return err
}

// ListLedger returns this scene's entries at or after since, oldest first,
// at most limit (newest kept). The tenant is part of the key: entries of a
// rebound tenant are never returned.
func ListLedger(ctx context.Context, q Queryer, key SceneKey, since time.Time, limit int) ([]LedgerEntry, error) {
	if q == nil || !key.valid() {
		return nil, ErrInvalid
	}
	if limit <= 0 || limit > 50 {
		limit = 50
	}
	rows, err := q.Query(ctx, `SELECT id::text,entry_kind,source_id,occurred_at,entry FROM employee_scene_ledger WHERE `+stateKey+` AND occurred_at>=$5 ORDER BY occurred_at DESC,id DESC LIMIT $6`, append(key.args(), since, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LedgerEntry
	for rows.Next() {
		var e LedgerEntry
		var raw []byte
		if err = rows.Scan(&e.ID, &e.Kind, &e.SourceID, &e.OccurredAt, &raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &e.Body); err != nil {
			return nil, errors.Join(ErrInvalid, err)
		}
		out = append(out, e)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}
