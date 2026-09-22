package userdecision

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type Alert struct {
	WorkspaceID string
	DecisionID  string
	Reason      string
	Item        json.RawMessage
}

// Monitor persists one owner notification per decision/problem. The primary
// key is the cross-replica deduplication gate; publishing is only a wakeup hint.
// Operational ownership does not change who may answer the decision card.
func (s *Store) Monitor(ctx context.Context) ([]Alert, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT d.id::text,d.workspace_id::text,a.owner_id::text,a.name,d.agent_id::text,problem.reason
 FROM coordinator_user_decision d JOIN agent a ON a.id=d.agent_id AND a.workspace_id=d.workspace_id
 JOIN member m ON m.workspace_id=a.workspace_id AND m.user_id=a.owner_id
 LEFT JOIN coordinator_user_decision_consumer c ON c.environment=d.environment AND c.sender_uid=d.sender_uid AND c.sender_org_id=d.sender_org_id
 CROSS JOIN LATERAL (VALUES
 (CASE WHEN d.state IN ('prepared','sending','waiting','accepted','resuming') AND d.created_at<now()-interval '5 minutes' AND (c.owner IS NULL OR c.lease_expires_at<=now() OR NOT c.ready) THEN 'consumer_unavailable' END),
 (CASE WHEN d.state='send_unknown' AND d.updated_at<now()-interval '5 minutes' THEN 'send_unknown' END),
 (CASE WHEN d.state IN ('accepted','resuming') AND d.accepted_at<now()-interval '5 minutes' THEN 'resume_delayed' END),
 (CASE WHEN d.card_update_pending AND d.sent_at IS NOT NULL AND d.updated_at<now()-interval '5 minutes' THEN 'card_update_delayed' END)
 ) AS problem(reason)
 WHERE d.environment=$1 AND a.archived_at IS NULL AND (d.state IN ('prepared','sending','send_unknown','waiting','accepted','resuming') OR d.card_update_pending) AND problem.reason IS NOT NULL
 AND NOT EXISTS(SELECT 1 FROM inbox_item i WHERE i.workspace_id=d.workspace_id AND i.recipient_type='member' AND i.recipient_id=a.owner_id AND i.type='coordinator_decision_alert' AND i.details->>'decision_id'=d.id::text AND i.details->>'reason'=problem.reason AND i.details->>'environment'=d.environment)
 ORDER BY d.created_at LIMIT 100`, s.Environment)
	if err != nil {
		return nil, err
	}
	type candidate struct{ id, workspace, owner, name, agent, reason string }
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err = rows.Scan(&c.id, &c.workspace, &c.owner, &c.name, &c.agent, &c.reason); err != nil {
			rows.Close()
			return nil, err
		}
		candidates = append(candidates, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	var alerts []Alert
	for _, c := range candidates {
		id := uuid.NewSHA1(uuid.NameSpaceOID, []byte("coordinator-decision-alert:"+s.Environment+":"+c.id+":"+c.reason+":"+c.owner)).String()
		details, _ := json.Marshal(map[string]string{"decision_id": c.id, "agent_id": c.agent, "reason": c.reason, "environment": s.Environment})
		body := fmt.Sprintf("%s。该状态已持续或等待超过 5 分钟，请检查数字员工的消息订阅和处理记录。决策记录：%s。此提醒不会代替发起人选择，也不会重新派发工作。", alertDescription(c.reason), c.id)
		var item json.RawMessage
		err = tx.QueryRow(ctx, `INSERT INTO inbox_item(id,workspace_id,recipient_type,recipient_id,type,severity,title,body,actor_type,details)
 VALUES($1,$2,'member',$3,'coordinator_decision_alert','attention',$4,$5,'system',$6)
 ON CONFLICT(id) DO NOTHING RETURNING to_jsonb(inbox_item)`, id, c.workspace, c.owner, "处理方式选择异常 · "+c.name, body, details).Scan(&item)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		alerts = append(alerts, Alert{WorkspaceID: c.workspace, DecisionID: c.id, Reason: c.reason, Item: item})
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return alerts, nil
}

func alertDescription(reason string) string {
	switch reason {
	case "consumer_unavailable":
		return "选择卡片的事件订阅尚未就绪"
	case "send_unknown":
		return "选择卡片的发送结果尚未确认，系统未重复发送"
	case "resume_delayed":
		return "已经收到用户选择，但处理流程尚未恢复"
	case "card_update_delayed":
		return "卡片状态更新尚未完成，已接受的选择仍然保留"
	default:
		return "处理方式选择需要检查"
	}
}

// Metrics uses authoritative rows, not per-process counters. Rejection reasons
// and duplicate deliveries remain distinct from accepted human selections.
func (s *Store) Metrics(ctx context.Context, workspaceID, agentID string) (json.RawMessage, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var raw json.RawMessage
	err = tx.QueryRow(ctx, `WITH scoped AS (SELECT * FROM coordinator_user_decision WHERE environment=$1 AND workspace_id=$2 AND agent_id=$3),
 outcomes AS (SELECT e.outcome,count(*) AS n FROM coordinator_user_decision_event e JOIN scoped d ON d.id=e.decision_id WHERE e.created_at>=now()-interval '24 hours' GROUP BY e.outcome)
 SELECT jsonb_build_object(
 'waiting',count(*) FILTER(WHERE state='waiting'),
 'send_unknown',count(*) FILTER(WHERE state='send_unknown'),
 'pending_card_updates',count(*) FILTER(WHERE card_update_pending),
 'pending_resumes',count(*) FILTER(WHERE state IN ('accepted','resuming')),
 'oldest_wait_seconds',COALESCE(max(extract(epoch FROM now()-sent_at)) FILTER(WHERE state='waiting'),0),
 'accepted_wait_seconds_24h',avg(extract(epoch FROM accepted_at-sent_at)) FILTER(WHERE accepted_at>=now()-interval '24 hours'),
 'outcomes_24h',COALESCE((SELECT jsonb_object_agg(outcome,n) FROM outcomes),'{}'::jsonb),
 'duplicate_deliveries_total',COALESCE((SELECT sum(e.delivery_count-1) FROM coordinator_user_decision_event e JOIN scoped d ON d.id=e.decision_id),0)) FROM scoped`, s.Environment, workspaceID, agentID).Scan(&raw)
	return raw, err
}
