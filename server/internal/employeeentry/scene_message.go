package employeeentry

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Persistent group transcript (M8, docs/employee-loop.md "Group transcript").
//
// employee_scene_message keeps the human lines of a group scene for
// SceneMessageRetention: every group message the agent's account can see is
// observed (GawkBot's "observe all"), only addressed ones wake the Loop. Rows
// exist only for groups that already have an agent_scene for the agent and
// tenant; an observation never registers a scene. Reads exclude withdrawn
// rows, lines before the scene memory reset and the evidence of forgotten or
// superseded memory records, so nothing forgotten comes back through the
// transcript. The transcript is material, never authority or a request.

const (
	SceneMessageRetention = 14 * 24 * time.Hour
	// SceneMessageBodyLimit bounds one stored body in bytes.
	SceneMessageBodyLimit = 4096
	// SceneMessageReadLimit bounds one read of the transcript.
	SceneMessageReadLimit = 500
	// SceneMessageInsertLimit bounds one write batch.
	SceneMessageInsertLimit = 64

	SceneMessageSourceWakeRead    = "wake_read"
	SceneMessageSourceNativeGroup = "native_group"

	SceneSenderHuman   = "human"
	SceneSenderSelf    = "self"
	SceneSenderBot     = "bot"
	SceneSenderUnknown = "unknown"

	ProactivePending  = "pending"
	ProactiveAdmitted = "admitted"
	ProactiveSkipped  = "skipped"
)

// SceneMessageKey is the exact scene a transcript row belongs to.
type SceneMessageKey = Scope

// SceneMessageDB is a pgx pool or transaction.
type SceneMessageDB interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// SceneMessageRow is one provider message to persist. Body is the raw
// (already redacted) text; the store clips it to SceneMessageBodyLimit.
type SceneMessageRow struct {
	SceneMessageInput
	// ReceiptID is the observation's scene_event_receipt (native_group only).
	ReceiptID string
	// ProactiveDueAt marks an unaddressed question as a proactive wake
	// candidate decided at that time; zero means no candidate.
	ProactiveDueAt time.Time
}

// SceneMessage is one stored transcript line.
type SceneMessage struct {
	ProviderMessageID string    `json:"provider_message_id"`
	SentAt            time.Time `json:"sent_at"`
	SenderClass       string    `json:"sender_class"`
	SenderRef         string    `json:"sender_ref"`
	SenderName        string    `json:"sender_name"`
	QuotedMessageID   string    `json:"quoted_message_id,omitempty"`
	Body              string    `json:"body"`
	Truncated         bool      `json:"truncated,omitempty"`
	Source            string    `json:"source"`
}

// SceneMessageInsertResult reports rows that were new, so a caller can mark
// downstream state dirty only for new human evidence.
type SceneMessageInsertResult struct {
	Inserted      int
	HumanInserted int
	LastHumanAt   time.Time
}

func validSceneSender(class string) bool {
	switch class {
	case SceneSenderHuman, SceneSenderSelf, SceneSenderBot, SceneSenderUnknown:
		return true
	}
	return false
}

func validProviderToken(s string, limit int) bool {
	return s != "" && len(s) <= limit && strings.TrimSpace(s) == s && !strings.ContainsAny(s, "\x00\r\n")
}

// clipUTF8 cuts s to at most limit bytes on a rune boundary.
func clipUTF8(s string, limit int) string {
	s = strings.ToValidUTF8(s, "")
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// InsertSceneMessages persists rows idempotently: a provider message is
// stored once per scene (ON CONFLICT DO NOTHING), whichever path saw it
// first. A later native observation only attaches its receipt to a row the
// wake-time read stored, so the retention purge can remove that receipt too.
func InsertSceneMessages(ctx context.Context, q SceneMessageDB, key SceneMessageKey, source string, rows []SceneMessageRow) (SceneMessageInsertResult, error) {
	var result SceneMessageInsertResult
	if q == nil || !validScope(key) || (source != SceneMessageSourceWakeRead && source != SceneMessageSourceNativeGroup) || len(rows) > SceneMessageInsertLimit {
		return result, ErrInvalid
	}
	for _, row := range rows {
		body := strings.TrimSpace(strings.ToValidUTF8(row.Body, ""))
		if !validProviderToken(row.ProviderMessageID, 256) || row.SentAt.IsZero() || !validSceneSender(row.SenderClass) || body == "" ||
			len(row.SenderRef) > 320 || (row.ReceiptID != "" && !validID(row.ReceiptID)) {
			return result, ErrInvalid
		}
		stored := clipUTF8(body, SceneMessageBodyLimit)
		quoted := strings.TrimSpace(row.QuotedMessageID)
		if !validProviderToken(quoted, 256) {
			quoted = ""
		}
		var due any
		if !row.ProactiveDueAt.IsZero() && row.SenderClass == SceneSenderHuman && source == SceneMessageSourceNativeGroup {
			due = row.ProactiveDueAt.UTC()
		}
		var inserted bool
		err := q.QueryRow(ctx, `INSERT INTO employee_scene_message
 (workspace_id,agent_id,tenant_org_id,scene_id,provider_message_id,sent_at,source,receipt_id,sender_class,sender_ref,sender_name,quoted_message_id,body,original_bytes,truncated,proactive_state,proactive_due_at)
 VALUES($1::uuid,$2::uuid,$3,$4::uuid,$5,$6,$7,NULLIF($8,'')::uuid,$9,$10,$11,$12,$13,$14,$15,CASE WHEN $16::timestamptz IS NULL THEN '' ELSE 'pending' END,$16::timestamptz)
 ON CONFLICT (workspace_id,agent_id,scene_id,provider_message_id) DO UPDATE SET receipt_id=EXCLUDED.receipt_id
 WHERE employee_scene_message.receipt_id IS NULL AND EXCLUDED.receipt_id IS NOT NULL
 RETURNING (xmax=0)`,
			key.WorkspaceID, key.AgentID, key.TenantOrgID, key.SceneID, row.ProviderMessageID, row.SentAt.UTC(), source, row.ReceiptID,
			row.SenderClass, row.SenderRef, clipUTF8(strings.TrimSpace(row.SenderName), 128), quoted, stored, len(body), len(stored) < len(body), due).Scan(&inserted)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return result, err
		}
		if !inserted {
			continue
		}
		result.Inserted++
		if row.SenderClass == SceneSenderHuman {
			result.HumanInserted++
			if row.SentAt.After(result.LastHumanAt) {
				result.LastHumanAt = row.SentAt.UTC()
			}
		}
	}
	return result, nil
}

// sceneMessageVisible is the read fence shared by every transcript read:
// rows of the exact scene, not withdrawn, after the scene memory reset, and
// not the evidence of a forgotten or superseded memory record of the scene.
const sceneMessageVisible = `m.workspace_id=$1::uuid AND m.agent_id=$2::uuid AND m.tenant_org_id=$3 AND m.scene_id=$4::uuid AND m.withdrawn_at IS NULL
 AND m.sent_at > COALESCE((SELECT s.reset_at FROM employee_memory_state s WHERE s.workspace_id=m.workspace_id AND s.agent_id=m.agent_id AND s.tenant_org_id=m.tenant_org_id AND s.scene_id=m.scene_id AND s.scope_kind='scene' AND s.principal_id='' AND s.reset_at IS NOT NULL),'-infinity'::timestamptz)
 AND NOT EXISTS (SELECT 1 FROM employee_learning l WHERE l.workspace_id=m.workspace_id AND l.agent_id=m.agent_id AND l.tenant_org_id=m.tenant_org_id AND l.scene_id=m.scene_id
   AND (l.forgotten_at IS NOT NULL OR l.superseded_by IS NOT NULL) AND l.record->>'evidence_id'=m.provider_message_id)`

const sceneMessageColumns = `m.provider_message_id,m.sent_at,m.sender_class,m.sender_ref,m.sender_name,m.quoted_message_id,m.body,m.truncated,m.source`

func scanSceneMessages(rows pgx.Rows) ([]SceneMessage, error) {
	defer rows.Close()
	out := []SceneMessage{}
	for rows.Next() {
		var m SceneMessage
		if err := rows.Scan(&m.ProviderMessageID, &m.SentAt, &m.SenderClass, &m.SenderRef, &m.SenderName, &m.QuotedMessageID, &m.Body, &m.Truncated, &m.Source); err != nil {
			return nil, err
		}
		m.SentAt = m.SentAt.UTC()
		out = append(out, m)
	}
	return out, rows.Err()
}

func readLimit(limit int) int {
	if limit <= 0 || limit > SceneMessageReadLimit {
		return SceneMessageReadLimit
	}
	return limit
}

// SceneMessagesAfter returns visible rows strictly after the cursor
// (sentAt, providerMessageID), oldest first. A zero afterAt starts at the
// retention boundary.
func SceneMessagesAfter(ctx context.Context, q SceneMessageDB, key SceneMessageKey, afterAt time.Time, afterID string, limit int) ([]SceneMessage, error) {
	if q == nil || !validScope(key) {
		return nil, ErrInvalid
	}
	if afterAt.IsZero() {
		afterAt, afterID = time.Unix(0, 0), ""
	}
	rows, err := q.Query(ctx, `SELECT `+sceneMessageColumns+` FROM employee_scene_message m WHERE `+sceneMessageVisible+`
 AND (m.sent_at,m.provider_message_id) > ($5::timestamptz,$6::text)
 ORDER BY m.sent_at,m.provider_message_id LIMIT $7`, key.WorkspaceID, key.AgentID, key.TenantOrgID, key.SceneID, afterAt.UTC(), afterID, readLimit(limit))
	if err != nil {
		return nil, err
	}
	return scanSceneMessages(rows)
}

// SceneMessagesBefore returns the visible rows strictly before the cursor,
// the newest limit of them, oldest first.
func SceneMessagesBefore(ctx context.Context, q SceneMessageDB, key SceneMessageKey, beforeAt time.Time, beforeID string, limit int) ([]SceneMessage, error) {
	if q == nil || !validScope(key) || beforeAt.IsZero() {
		return nil, ErrInvalid
	}
	rows, err := q.Query(ctx, `SELECT * FROM (SELECT `+sceneMessageColumns+` FROM employee_scene_message m WHERE `+sceneMessageVisible+`
 AND (m.sent_at,m.provider_message_id) < ($5::timestamptz,$6::text)
 ORDER BY m.sent_at DESC,m.provider_message_id DESC LIMIT $7) newest ORDER BY sent_at,provider_message_id`,
		key.WorkspaceID, key.AgentID, key.TenantOrgID, key.SceneID, beforeAt.UTC(), beforeID, readLimit(limit))
	if err != nil {
		return nil, err
	}
	return scanSceneMessages(rows)
}

// SceneRecallCorpus returns the newest visible human rows of the retention
// window, newest first, without the excluded provider message ids (the
// current window and lines already frozen into the snapshot).
func SceneRecallCorpus(ctx context.Context, q SceneMessageDB, key SceneMessageKey, now time.Time, exclude []string, limit int) ([]SceneMessage, error) {
	if q == nil || !validScope(key) || now.IsZero() {
		return nil, ErrInvalid
	}
	if exclude == nil {
		exclude = []string{}
	}
	rows, err := q.Query(ctx, `SELECT `+sceneMessageColumns+` FROM employee_scene_message m WHERE `+sceneMessageVisible+`
 AND m.sender_class='human' AND m.sent_at >= $5 AND m.sent_at <= $6 AND NOT (m.provider_message_id = ANY($7::text[]))
 ORDER BY m.sent_at DESC,m.provider_message_id DESC LIMIT $8`,
		key.WorkspaceID, key.AgentID, key.TenantOrgID, key.SceneID, now.Add(-SceneMessageRetention).UTC(), now.UTC(), exclude, readLimit(limit))
	if err != nil {
		return nil, err
	}
	return scanSceneMessages(rows)
}

// PurgeSceneMessages deletes one batch of rows first seen before cutoff and
// the native observation receipts they carry. Concurrent purges on other
// replicas skip each other's locked rows, so no lease is needed.
func PurgeSceneMessages(ctx context.Context, database DB, cutoff time.Time, batch int) (messages, receipts int, err error) {
	if database == nil || cutoff.IsZero() || batch <= 0 || batch > 5000 {
		return 0, 0, ErrInvalid
	}
	tx, err := database.Begin(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	rows, err := tx.Query(ctx, `DELETE FROM employee_scene_message WHERE ctid = ANY(ARRAY(
 SELECT ctid FROM employee_scene_message WHERE first_seen_at < $1 ORDER BY first_seen_at LIMIT $2 FOR UPDATE SKIP LOCKED))
 RETURNING workspace_id::text,agent_id::text,COALESCE(receipt_id::text,'')`, cutoff.UTC(), batch)
	if err != nil {
		return 0, 0, err
	}
	type receiptRef struct{ workspace, agent, id string }
	var refs []receiptRef
	for rows.Next() {
		var ref receiptRef
		if err := rows.Scan(&ref.workspace, &ref.agent, &ref.id); err != nil {
			rows.Close()
			return 0, 0, err
		}
		messages++
		if ref.id != "" {
			refs = append(refs, ref)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}
	for _, ref := range refs {
		// Only the observation receipts this transcript wrote; a user
		// message receipt is never touched here.
		tag, err := tx.Exec(ctx, `DELETE FROM scene_event_receipt WHERE id=$1::uuid AND workspace_id=$2::uuid AND agent_id=$3::uuid
 AND envelope->>'category'='observation' AND source LIKE 'dws-native-group/%' AND created_at < $4`, ref.id, ref.workspace, ref.agent, cutoff.UTC())
		if err != nil {
			return 0, 0, err
		}
		receipts += int(tag.RowsAffected())
	}
	return messages, receipts, tx.Commit(ctx)
}
