package contextcap

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
)

// DirectScenePerson returns the person a DingTalk 1:1 chat scene of the agent
// is bound to: the counterpart's staffId and a display title. A 1:1 chat
// scene has no configuration of its own; its bindings, credentials and custom
// MCP servers are that person's (docs/context-capabilities.md §1.1).
//
// Sources, org-scoped like the scene list:
//
//   - the newest inbound Coordinator job of the conversation that carries a
//     sender staffId (command event.data.sender.staffId, the same
//     server-written dispatch sender ScopeFromTaskContext reads as
//     PersonKey), recorded under orgID (a job that recorded no agent org
//     belongs to identityOrgID, the agent's DingTalk identity org);
//   - a live person grant redeemed from a personal link minted in that 1:1
//     chat (context_config_link.extra_scene_key = sceneKey): a DM link grants
//     both the person and the DM scene.
//
// The title is the sender's display name or the grant's title. staffID is ""
// (with a nil error) when neither source names a person.
func DirectScenePerson(ctx context.Context, db DBTX, workspaceID, agentID, orgID, identityOrgID, sceneKey string) (string, string, error) {
	if !ValidOpenConversationID(sceneKey) {
		return "", "", ErrInvalidInput
	}
	var jobStaff, jobTitle string
	// The conversation key filter matches inbound_coordinator_job_agent_conversation_idx.
	err := db.QueryRow(ctx, `SELECT BTRIM(job.command #>> '{event,data,sender,staffId}'),
		  BTRIM(COALESCE(job.command #>> '{event,data,sender,displayName}', ''))
		FROM inbound_coordinator_job job
		WHERE job.agent_id = $2::uuid AND job.workspace_id = $1::uuid
		  AND BTRIM(job.command #>> '{event,data,conversation,openConversationId}') = $4::text
		  AND lower(COALESCE(NULLIF(BTRIM(job.command #>> '{source,platform}'), ''), 'dingtalk')) = 'dingtalk'
		  AND ($3::text = '' OR `+jobOrgExpr("$5")+` = $3::text)
		  AND NULLIF(BTRIM(job.command #>> '{event,data,sender,staffId}'), '') IS NOT NULL
		ORDER BY job.created_at DESC, job.id DESC
		LIMIT 1`, workspaceID, agentID, orgID, sceneKey, identityOrgID).Scan(&jobStaff, &jobTitle)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", "", err
	}
	if !ValidStaffID(jobStaff) {
		jobStaff, jobTitle = "", ""
	}

	// Live person grants redeemed from a personal link minted in this 1:1
	// chat, newest first. When the job named the person, only that person's
	// grant supplies a missing title.
	rows, err := db.Query(ctx, `SELECT g.scope_key, COALESCE(NULLIF(g.scope_title, ''), l.scope_title)
		FROM context_config_link l
		JOIN context_config_grant g ON g.user_id = l.consumed_by AND g.agent_id = l.agent_id AND g.workspace_id = l.workspace_id
		  AND g.scope_type = 'person' AND g.org_id = l.org_id AND g.scope_key = l.scope_key AND g.expires_at > now()
		WHERE l.workspace_id = $1::uuid AND l.agent_id = $2::uuid AND l.org_id = $3::text
		  AND l.scope_type = 'person' AND l.extra_scene_key = $4::text AND l.consumed_by IS NOT NULL
		ORDER BY g.updated_at DESC, l.created_at DESC
		LIMIT 20`, workspaceID, agentID, orgID, sceneKey)
	if err != nil {
		return "", "", err
	}
	defer rows.Close()
	staffID, title := jobStaff, jobTitle
	for rows.Next() {
		var grantStaff, grantTitle string
		if err := rows.Scan(&grantStaff, &grantTitle); err != nil {
			return "", "", err
		}
		grantStaff = strings.TrimSpace(grantStaff)
		if !ValidStaffID(grantStaff) {
			continue
		}
		if staffID == "" {
			staffID = grantStaff
		}
		if grantStaff == staffID && title == "" {
			title = strings.TrimSpace(grantTitle)
		}
	}
	if err := rows.Err(); err != nil {
		return "", "", err
	}
	return staffID, title, nil
}
