package employeeentry

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

var errReplyAncestorUnavailable = errors.New("reply ancestor provenance unavailable")

const replyAncestorDepthLimit = 64

type deliveredMemoryReply struct{ action, message, job string }

// closeReplyAncestors reads only exact IDs referenced by frozen assistant
// turns. The presentation window bounds candidates, not their provenance.
func (s *Store) closeReplyAncestors(ctx context.Context, scope Scope, before time.Time, nodes map[string]*replyMemoryProvenance, replies []deliveredMemoryReply, retired map[string]bool) ([]deliveredMemoryReply, error) {
	for depth := 0; depth <= replyAncestorDepthLimit; depth++ {
		byAction, byMessage := map[string]deliveredMemoryReply{}, map[string][]deliveredMemoryReply{}
		for _, reply := range replies {
			byAction[reply.action] = reply
			byMessage[reply.message] = append(byMessage[reply.message], reply)
		}
		pending := map[string]RecentConversationMessage{}
		for _, node := range nodes {
			// An exact withdrawn record already proves this entire reply unusable.
			if node.Withdrawn {
				continue
			}
			dependencies := append([]RecentConversationMessage{}, node.History...)
			for _, id := range node.AssistantSources {
				dependencies = append(dependencies, RecentConversationMessage{Role: "assistant", MessageID: id})
			}
			for _, ref := range dependencies {
				if ref.Role != "assistant" {
					continue
				}
				if (ref.ActionID == "" && ref.MessageID == "") || len(ref.ActionID) > 256 || len(ref.MessageID) > 256 {
					return nil, errReplyAncestorUnavailable
				}
				var known deliveredMemoryReply
				if ref.ActionID != "" {
					known = byAction[ref.ActionID]
					if known.action != "" && ref.MessageID != "" && known.message != ref.MessageID {
						return nil, errReplyAncestorUnavailable
					}
				} else if candidates := byMessage[ref.MessageID]; len(candidates) == 1 {
					known = candidates[0]
				} else if len(candidates) > 1 {
					return nil, errReplyAncestorUnavailable
				}
				if source := nodes[known.job]; known.action != "" && source != nil {
					if !source.SnapshotKnown && !source.Withdrawn {
						return nil, errReplyAncestorUnavailable
					}
					continue
				}
				pending[ref.ActionID+"\x00"+ref.MessageID] = ref
			}
		}
		if len(pending) == 0 {
			return replies, nil
		}
		if depth == replyAncestorDepthLimit {
			return nil, errReplyAncestorUnavailable
		}
		if len(pending)+len(replies) > transcriptEvidenceCap {
			return nil, errReplyAncestorUnavailable
		}
		for _, ref := range pending {
			reply, node, err := s.readReplyAncestor(ctx, scope, before, ref, retired)
			if err != nil {
				return nil, err
			}
			if prior, exists := byAction[reply.action]; exists {
				if prior.message != reply.message {
					return nil, errReplyAncestorUnavailable
				}
				// A window action can have an older source job not loaded yet.
				for index := range replies {
					if replies[index].action == reply.action {
						replies[index] = reply
					}
				}
			} else {
				replies = append(replies, reply)
				byAction[reply.action] = reply
			}
			nodes[node.ID] = &node
			if len(nodes) > transcriptEvidenceCap || len(replies) > transcriptEvidenceCap {
				return nil, errReplyAncestorUnavailable
			}
		}
	}
	return nil, errReplyAncestorUnavailable
}

func (s *Store) readReplyAncestor(ctx context.Context, scope Scope, before time.Time, ref RecentConversationMessage, retired map[string]bool) (deliveredMemoryReply, replyMemoryProvenance, error) {
	var reply deliveredMemoryReply
	var notice, callback, request, runJob, hostJob string
	rows, err := s.db.Query(ctx, `SELECT a.id,a.provider_message_id,COALESCE(a.input->>'scene_notice_id',''),CASE WHEN a.input->>'request_id'=a.request_id THEN COALESCE(a.input->>'callback_url','') ELSE '' END,a.request_id,COALESCE(n.job_id::text,''),COALESCE(h.source_id,'')
 FROM response_action a JOIN agent_scene sc ON sc.id=$4::uuid AND sc.workspace_id=$1::uuid AND sc.agent_id=$2::uuid AND sc.tenant_org_id=$3
 LEFT JOIN employee_run_notice n ON n.action_id=a.id AND n.workspace_id=a.workspace_id AND n.agent_id=a.agent_id AND n.tenant_org_id=$3 AND n.scene_id=$4::uuid
 LEFT JOIN employee_host_notice h ON h.action_id=a.id AND h.workspace_id=a.workspace_id AND h.agent_id=a.agent_id AND h.tenant_org_id=$3 AND h.scene_id=$4::uuid
 WHERE a.workspace_id=$1::uuid AND a.agent_id=$2::uuid AND a.kind='message.send' AND a.state='delivered' AND a.error_code='' AND a.provider_message_id<>'' AND a.provider_conversation_id=sc.external_scene_id
 AND a.input->>'workspace_id'=$1::text AND a.input->>'agent_id'=$2::text AND a.input->>'scene_id'=$4::text AND a.input->>'dws_org_id'=$3 AND a.input->>'conversation_id'=sc.external_scene_id AND a.updated_at<$5
 AND (($6<>'' AND a.id=$6) OR ($6='' AND a.provider_message_id=$7)) LIMIT 2`, append(scopeArgs(scope), before, ref.ActionID, ref.MessageID)...)
	if err != nil {
		return reply, replyMemoryProvenance{}, err
	}
	count := 0
	for rows.Next() {
		count++
		if err = rows.Scan(&reply.action, &reply.message, &notice, &callback, &request, &runJob, &hostJob); err != nil {
			rows.Close()
			return reply, replyMemoryProvenance{}, err
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return reply, replyMemoryProvenance{}, err
	}
	if count != 1 || (ref.MessageID != "" && ref.MessageID != reply.message) {
		return reply, replyMemoryProvenance{}, errReplyAncestorUnavailable
	}
	job := runJob
	if job == "" && validID(hostJob) {
		job = hostJob
	}
	if job == "" && validID(notice) {
		job = notice
	}
	var snapshot, journal []byte
	if validID(job) {
		err = s.db.QueryRow(ctx, `SELECT id::text,input_snapshot,tool_journal FROM employee_scene_job WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND scene_id=$4::uuid AND id=$5::uuid AND created_at<$6`, append(scopeArgs(scope), job, before)...).Scan(&reply.job, &snapshot, &journal)
	} else {
		err = pgx.ErrNoRows
	}
	if errors.Is(err, pgx.ErrNoRows) {
		pair, ok := recentReplyCallback(callback)
		if !ok || pair.RequestID != request {
			return reply, replyMemoryProvenance{}, errReplyAncestorUnavailable
		}
		// An exact synchronous callback identifies its original admitted job;
		// the URL alone never establishes a source.
		rows, queryErr := s.db.Query(ctx, `SELECT DISTINCT j.id::text,j.input_snapshot,j.tool_journal FROM employee_scene_job j JOIN employee_event_consumption c ON c.job_id=j.id AND c.workspace_id=j.workspace_id AND c.agent_id=j.agent_id AND c.tenant_org_id=j.tenant_org_id AND c.scene_id=j.scene_id
 WHERE j.workspace_id=$1::uuid AND j.agent_id=$2::uuid AND j.tenant_org_id=$3 AND j.scene_id=$4::uuid AND j.created_at<$5 AND c.payload#>>'{command,completionCallback,responseUrl}'=$6 LIMIT 2`, append(scopeArgs(scope), before, pair.URL)...)
		if queryErr != nil {
			return reply, replyMemoryProvenance{}, queryErr
		}
		count = 0
		for rows.Next() {
			count++
			if err = rows.Scan(&reply.job, &snapshot, &journal); err != nil {
				rows.Close()
				return reply, replyMemoryProvenance{}, err
			}
		}
		err = rows.Err()
		rows.Close()
		if err == nil && count != 1 {
			err = errReplyAncestorUnavailable
		}
	}
	if err != nil {
		return reply, replyMemoryProvenance{}, err
	}
	node, err := replyProvenance(reply.job, scope.SceneID, snapshot, journal, retired)
	if err == nil && !node.SnapshotKnown && !node.Withdrawn {
		err = errReplyAncestorUnavailable
	}
	return reply, node, err
}

// Known snapshots may have no records. A missing/empty legacy snapshot cannot
// establish a safe ancestor, unless an actual retired result already withdraws it.
func knownReplySnapshot(snapshot []byte) bool {
	var saved map[string]json.RawMessage
	if json.Unmarshal(snapshot, &saved) != nil {
		return false
	}
	if strings.HasPrefix(strings.TrimSpace(string(saved["memory_manifest"])), "[") {
		return true
	}
	var input map[string]json.RawMessage
	if json.Unmarshal(saved["input"], &input) != nil || input == nil {
		return false
	}
	var memory string
	if raw, present := input["Memory"]; present {
		if strings.TrimSpace(string(raw)) == "null" || json.Unmarshal(raw, &memory) != nil {
			return false
		}
		if memory == "" {
			return true
		}
		// Current snapshots omit an empty manifest, but persist explicit Host
		// counts. Accept only proof of zero injected records, never legacy text.
		var stats map[string]json.RawMessage
		if json.Unmarshal(saved["memory_stats"], &stats) != nil {
			return false
		}
		for _, key := range []string{"pinned", "retrieved", "verified"} {
			var count int
			if raw, exists := stats[key]; !exists || strings.TrimSpace(string(raw)) == "null" || json.Unmarshal(raw, &count) != nil || count != 0 {
				return false
			}
		}
		var personView bool
		if raw, present := stats["person_view"]; present && (strings.TrimSpace(string(raw)) == "null" || json.Unmarshal(raw, &personView) != nil || personView) {
			return false
		}
		return true
	}
	// A supported history-only projection must actually have a structured
	// message array; arbitrary nonempty input fields are not provenance.
	var history string
	if json.Unmarshal(input["RecentConversation"], &history) != nil {
		return false
	}
	var prior map[string]json.RawMessage
	if json.Unmarshal([]byte(history), &prior) != nil || !strings.HasPrefix(strings.TrimSpace(string(prior["messages"])), "[") {
		return false
	}
	var messages []RecentConversationMessage
	if json.Unmarshal(prior["messages"], &messages) != nil {
		return false
	}
	for _, message := range messages {
		if message.Role != "user" && message.Role != "assistant" {
			return false
		}
	}
	return true
}
