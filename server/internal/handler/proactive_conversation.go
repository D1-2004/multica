package handler

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

func dispatchMentionsEmployee(c DispatchCommand) bool {
	if c.ExternalIdentity.DWS == nil {
		return false
	}
	for _, m := range c.Event.Data.Mentions {
		if m.UID != "" && m.UID == c.ExternalIdentity.DWS.UID {
			return true
		}
	}
	return false
}

// Persist message identity in the same transaction as the Coordinator job.
// The legacy inbox remains authoritative for messages accepted before cutover.
func (h *Handler) deduplicateObservedMessages(ctx context.Context, tx pgx.Tx, c DispatchCommand, dc agentDispatchContext) (DispatchCommand, error) {
	identity := c.ExternalIdentity.DWS
	if identity == nil {
		return c, fmt.Errorf("observed message identity is unavailable")
	}
	source := strings.Join([]string{c.Source.Platform, identity.OrgID, identity.UID}, ":")
	// The same lock also serializes legacy dispatches using a different Router idempotency key.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, uuidToString(dc.WorkspaceID)+":"+uuidToString(dc.AgentID)+":"+dispatchConversationID(c)); err != nil {
		return c, err
	}
	messages := make([]DispatchMessage, 0, len(c.Event.Data.Messages))
	for _, m := range c.Event.Data.Messages {
		if m.OpenMsgID == "" {
			return c, fmt.Errorf("observed message id is required")
		}
		tag, err := tx.Exec(ctx, `INSERT INTO coordinator_observed_message(workspace_id,agent_id,source_key,conversation_id,message_id)
   SELECT $1,$2,$3,$4,$5 WHERE NOT EXISTS(
    SELECT 1 FROM agent_event e JOIN agent_event_stream st ON st.id=e.stream_id
    WHERE st.workspace_id=$1 AND st.agent_id=$2 AND st.source_key=$3 AND st.resource_key=$4 AND e.event_id=$5)
   ON CONFLICT(workspace_id,agent_id,source_key,conversation_id,message_id) DO NOTHING`, dc.WorkspaceID, dc.AgentID, source, dispatchConversationID(c), m.OpenMsgID)
		if err != nil {
			return c, err
		}
		if tag.RowsAffected() > 0 {
			messages = append(messages, m)
		}
	}
	c.Event.Data.Messages = messages
	return c, nil
}
