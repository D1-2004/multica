package employeeentry

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// Proactive wake candidates (DS-09): an unaddressed human question in a group
// scene waits a short time in employee_scene_message (proactive_state
// pending). The Host gate then decides deterministically, with no model call,
// whether to admit a normal unaddressed employee wake for it. These reads
// give the gate its facts; the decision itself lives in the handler.

// ProactiveCandidate is one claimed pending question.
type ProactiveCandidate struct {
	Key     SceneMessageKey
	Message SceneMessage
	DueAt   time.Time
	// Withdrawn: the row was withdrawn after it became a candidate.
	Withdrawn bool
}

// ClaimDueProactive locks up to limit due candidates in tx. Other replicas
// skip the locked rows; the caller decides each one before committing.
func ClaimDueProactive(ctx context.Context, tx pgx.Tx, now time.Time, limit int) ([]ProactiveCandidate, error) {
	if tx == nil || now.IsZero() || limit <= 0 || limit > 100 {
		return nil, ErrInvalid
	}
	rows, err := tx.Query(ctx, `SELECT m.workspace_id::text,m.agent_id::text,m.tenant_org_id,m.scene_id::text,`+sceneMessageColumns+`,m.proactive_due_at,m.withdrawn_at IS NOT NULL
 FROM employee_scene_message m WHERE m.proactive_state='pending' AND m.proactive_due_at <= $1
 ORDER BY m.proactive_due_at LIMIT $2 FOR UPDATE OF m SKIP LOCKED`, now.UTC(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProactiveCandidate
	for rows.Next() {
		var c ProactiveCandidate
		m := &c.Message
		if err := rows.Scan(&c.Key.WorkspaceID, &c.Key.AgentID, &c.Key.TenantOrgID, &c.Key.SceneID,
			&m.ProviderMessageID, &m.SentAt, &m.SenderClass, &m.SenderRef, &m.SenderName, &m.QuotedMessageID, &m.Body, &m.Truncated, &m.Source,
			&c.DueAt, &c.Withdrawn); err != nil {
			return nil, err
		}
		m.SentAt, c.DueAt = m.SentAt.UTC(), c.DueAt.UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}

// DecideProactive settles a pending candidate once; a settled one is never
// reopened.
func DecideProactive(ctx context.Context, q SceneMessageDB, key SceneMessageKey, providerMessageID, state, reason string, now time.Time) error {
	if q == nil || !validScope(key) || (state != ProactiveAdmitted && state != ProactiveSkipped) || len(reason) > 64 || now.IsZero() {
		return ErrInvalid
	}
	_, err := q.Exec(ctx, `UPDATE employee_scene_message SET proactive_state=$6,proactive_reason=$7,proactive_decided_at=$8
 WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND scene_id=$4::uuid AND provider_message_id=$5 AND proactive_state='pending'`,
		key.WorkspaceID, key.AgentID, key.TenantOrgID, key.SceneID, providerMessageID, state, reason, now.UTC())
	return err
}

// ProactiveAdmittedSince counts proactive wakes the gate admitted in the scene
// since the given time (the per-scene rate limit).
func ProactiveAdmittedSince(ctx context.Context, q SceneMessageDB, key SceneMessageKey, since time.Time) (int, error) {
	if q == nil || !validScope(key) {
		return 0, ErrInvalid
	}
	var n int
	err := q.QueryRow(ctx, `SELECT count(*) FROM employee_scene_message WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND scene_id=$4::uuid
 AND proactive_state='admitted' AND proactive_decided_at >= $5`, key.WorkspaceID, key.AgentID, key.TenantOrgID, key.SceneID, since.UTC()).Scan(&n)
	return n, err
}

// HumanRepliesAfter counts visible human lines of other senders that came
// after the question and no later than until.
func HumanRepliesAfter(ctx context.Context, q SceneMessageDB, key SceneMessageKey, question SceneMessage, until time.Time) (int, error) {
	if q == nil || !validScope(key) || question.SentAt.IsZero() {
		return 0, ErrInvalid
	}
	var n int
	err := q.QueryRow(ctx, `SELECT count(*) FROM employee_scene_message m WHERE `+sceneMessageVisible+`
 AND m.sender_class='human' AND (m.sent_at,m.provider_message_id) > ($5::timestamptz,$6::text) AND m.sent_at <= $7
 AND ($8='' OR m.sender_ref<>$8)`, key.WorkspaceID, key.AgentID, key.TenantOrgID, key.SceneID,
		question.SentAt.UTC(), question.ProviderMessageID, until.UTC(), question.SenderRef).Scan(&n)
	return n, err
}

// AdmittedText is a user message the scene already admitted to a work
// owner, with whether it addressed this agent's account.
type AdmittedText struct {
	MessageID string
	Text      string
	At        time.Time
	Addressed bool
}

// RecentAdmittedTexts returns user messages admitted in the scene since the
// given time, newest first. Addressed is true for a line that @-mentions the
// receiving account (a 1:1 scene is not read here).
func RecentAdmittedTexts(ctx context.Context, q SceneMessageDB, key SceneMessageKey, since time.Time, limit int) ([]AdmittedText, error) {
	if q == nil || !validScope(key) || limit <= 0 || limit > 200 {
		return nil, ErrInvalid
	}
	rows, err := q.Query(ctx, `SELECT COALESCE(m.value->>'openMsgId',''),left(COALESCE(m.value->>'text',''),2048),r.created_at,
 EXISTS (SELECT 1 FROM jsonb_array_elements(CASE WHEN jsonb_typeof(m.value->'mentions')='array' THEN m.value->'mentions' ELSE '[]'::jsonb END) x
   WHERE COALESCE(x->>'uid','')<>'' AND x->>'uid'=c.payload#>>'{command,externalIdentity,dws,uid}')
 FROM scene_event_receipt r JOIN employee_event_consumption c ON c.receipt_id=r.id AND c.workspace_id=r.workspace_id AND c.agent_id=r.agent_id
 CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(c.payload#>'{command,event,data,messages}')='array' THEN c.payload#>'{command,event,data,messages}' ELSE '[]'::jsonb END) m(value)
 WHERE r.workspace_id=$1::uuid AND r.agent_id=$2::uuid AND r.tenant_org_id=$3 AND r.scene_id=$4::uuid AND r.created_at >= $5
 AND r.envelope->>'category'='user_message' AND c.tenant_org_id=$3 AND c.scene_id=$4::uuid AND c.reason=''
 ORDER BY r.created_at DESC LIMIT $6`, key.WorkspaceID, key.AgentID, key.TenantOrgID, key.SceneID, since.UTC(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AdmittedText
	for rows.Next() {
		var a AdmittedText
		if err := rows.Scan(&a.MessageID, &a.Text, &a.At, &a.Addressed); err != nil {
			return nil, err
		}
		a.At = a.At.UTC()
		out = append(out, a)
	}
	return out, rows.Err()
}

// MessageAdmitted reports whether the provider message was already admitted
// as a user message of the scene (it was @-addressed, or a wake already took
// it), looking at receipts created since the given time.
func MessageAdmitted(ctx context.Context, q SceneMessageDB, key SceneMessageKey, providerMessageID string, since time.Time) (bool, error) {
	if q == nil || !validScope(key) || !validProviderToken(providerMessageID, 256) {
		return false, ErrInvalid
	}
	var found bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM scene_event_receipt r JOIN employee_event_consumption c ON c.receipt_id=r.id AND c.workspace_id=r.workspace_id AND c.agent_id=r.agent_id
 WHERE r.workspace_id=$1::uuid AND r.agent_id=$2::uuid AND r.tenant_org_id=$3 AND r.scene_id=$4::uuid AND r.created_at >= $6
 AND r.envelope->>'category'='user_message'
 AND jsonb_typeof(c.payload#>'{command,event,data,messages}')='array'
 AND EXISTS (SELECT 1 FROM jsonb_array_elements(c.payload#>'{command,event,data,messages}') m(value) WHERE m.value->>'openMsgId'=$5))`,
		key.WorkspaceID, key.AgentID, key.TenantOrgID, key.SceneID, providerMessageID, since.UTC()).Scan(&found)
	return found, err
}
