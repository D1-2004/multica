package dingtalkresponse

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

type SandboxDelivery struct {
	HasPending, HasUnknown, HasFailure bool
	State                              string
	MessageIDs                         []string
}
type sandboxDeliveryReader interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

// SandboxDeliveryInTx reads only server-verified receipts in the exact task,
// sending identity and destination. Duplicate shim/SDK receipts retain the
// existing delivered-first ordering; another conversation never qualifies.
func (s *Service) SandboxDeliveryInTx(ctx context.Context, tx pgx.Tx, in ActionInput) (SandboxDelivery, error) {
	return sandboxDelivery(ctx, tx, in)
}
func sandboxDelivery(ctx context.Context, db sandboxDeliveryReader, in ActionInput) (SandboxDelivery, error) {
	var out SandboxDelivery
	if in.TaskID == "" || in.ConversationID == "" {
		return out, nil
	}
	rows, err := db.Query(ctx, `SELECT state,provider_conversation_id,provider_message_id FROM sandbox_send_receipt
 WHERE workspace_id=$1 AND agent_id=$2 AND task_id=$3
 AND ($4::text='' OR issue_id=NULLIF($4,'')::uuid)
 AND (COALESCE(NULLIF(provider_conversation_id,''),target_conversation_id)=$5
  OR (provider_conversation_id='' AND target_conversation_id='' AND recipient_open_dingtalk_id='' AND state IN ('pending','provider_accepted','unknown')))
 AND ($6::text='' OR input->>'dws_uid'=$6) AND ($7::text='' OR input->>'dws_org_id'=$7)
 ORDER BY CASE state WHEN 'delivered' THEN 0 WHEN 'unknown' THEN 1 WHEN 'provider_accepted' THEN 2 WHEN 'pending' THEN 3 ELSE 4 END,created_at DESC`, in.WorkspaceID, in.AgentID, in.TaskID, in.IssueID, in.ConversationID, in.DWSUID, in.DWSOrgID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var state, cid, message string
		if err = rows.Scan(&state, &cid, &message); err != nil {
			return out, err
		}
		switch state {
		case "pending", "provider_accepted":
			out.HasPending = true
		case "unknown":
			out.HasUnknown = true
		case "failed":
			out.HasFailure = true
		}
		if out.State == "" {
			out.State = state
		}
		if state == "delivered" && cid == in.ConversationID && message != "" && !seen[message] {
			if len(out.MessageIDs) >= 100 {
				out.HasUnknown = true
				continue
			}
			out.MessageIDs = append(out.MessageIDs, message)
			seen[message] = true
		}
	}
	return out, rows.Err()
}

// VerifySandboxFile performs a provider read, never a send. Call outside a
// database transaction, then recheck the receipt/scope before suppressing a notice.
func (s *Service) VerifySandboxFile(ctx context.Context, in ActionInput, messageID string) (bool, error) {
	verifier, ok := s.provider.(interface {
		VerifyMessageFile(context.Context, ActionInput, string) (bool, error)
	})
	if !ok {
		return false, errors.New("DWS file delivery verification is unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, providerTimeout)
	defer cancel()
	return verifier.VerifyMessageFile(ctx, in, messageID)
}
func (p *dwsProvider) VerifyMessageFile(ctx context.Context, in ActionInput, messageID string) (bool, error) {
	cli, err := p.cliFor(in)
	if err != nil {
		return false, err
	}
	dir, cleanup, err := p.authenticateWith(ctx, cli, in)
	if err != nil {
		return false, err
	}
	defer cleanup()
	return cli.VerifyMessageFile(ctx, dir, in.ConversationID, messageID)
}
