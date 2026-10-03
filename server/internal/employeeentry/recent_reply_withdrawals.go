package employeeentry

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/service/employeememory"
)

type withdrawnReplyIDs struct {
	Actions, Messages map[string]bool
	Sources           map[string]bool
	Reasons           map[string]int
}

type replyMemoryProvenance struct {
	ID               string
	Withdrawn        bool
	SnapshotKnown    bool
	History          []RecentConversationMessage
	Sources          []string
	AssistantSources []string
	QuarantineReason string
}

// withdrawnMemoryReplyIDs follows only Host-recorded references. It never
// compares reply text with a learning's insight. A job that saw a withdrawn
// record or an associated earlier reply loses its whole delivered reply;
// splitting a reply into attributable words would require guessing.
func (s *Store) withdrawnMemoryReplyIDs(ctx context.Context, scope Scope, memoryPrincipal string, since, before time.Time) (withdrawnReplyIDs, error) {
	out := withdrawnReplyIDs{Actions: map[string]bool{}, Messages: map[string]bool{}, Sources: map[string]bool{}, Reasons: map[string]int{}}
	retired, evidence := map[string]string{}, map[string]bool{}
	// A cross-origin tombstone withdraws previously visible content only.
	// The Host supplies one trusted DM requester; no source scene lookup or
	// content read is needed, even if that original directory row was deleted.
	owner := ""
	if employeememory.PersonViewRef(scope.TenantOrgID, memoryPrincipal) {
		owner = memoryPrincipal
	}
	rows, err := s.db.Query(ctx, `SELECT id::text,scene_id::text,CASE WHEN scene_id=$4::uuid AND scope_kind='scene' AND record->>'source_id'='dingtalk-message:'||scene_id::text THEN COALESCE(record->>'evidence_id','') ELSE '' END
 FROM employee_learning WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3
 AND (scene_id=$4::uuid OR (scope_kind='private' AND $5<>'' AND principal_id=$5
  AND EXISTS(SELECT 1 FROM agent_scene s WHERE s.id=$4::uuid AND s.workspace_id=$1::uuid AND s.agent_id=$2::uuid AND s.tenant_org_id=$3 AND s.scene_kind='dm')))
 AND (forgotten_at IS NOT NULL OR superseded_by IS NOT NULL) LIMIT $6`, append(scopeArgs(scope), owner, transcriptEvidenceCap+1)...)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var id, originScene, message string
		if err = rows.Scan(&id, &originScene, &message); err != nil {
			rows.Close()
			return out, err
		}
		retired[id] = originScene
		if message != "" {
			evidence[message] = true
			out.Sources[message] = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(retired) > transcriptEvidenceCap {
		if err == nil {
			err = errTranscriptEvidenceBound
		}
		return out, err
	}
	if len(retired) == 0 {
		return out, nil
	}
	// These are projection candidates, not a read of arbitrary old jobs. Keep
	// the same scene/time bound as the history being assembled and fail closed
	// rather than accepting a partial provenance graph.
	rows, err = s.db.Query(ctx, `SELECT id::text,input_snapshot,tool_journal FROM employee_scene_job
 WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND scene_id=$4::uuid
 AND created_at>=$5 AND created_at<$6 ORDER BY created_at,id LIMIT $7`, append(scopeArgs(scope), since, before, transcriptEvidenceCap+1)...)
	if err != nil {
		return out, err
	}
	nodes := map[string]*replyMemoryProvenance{}
	for rows.Next() {
		var id string
		var snapshot, journal []byte
		if err = rows.Scan(&id, &snapshot, &journal); err != nil {
			rows.Close()
			return out, err
		}
		node, decodeErr := replyProvenance(id, snapshot, journal, retired)
		if decodeErr != nil {
			node = replyMemoryProvenance{ID: id, Withdrawn: true, QuarantineReason: "unsupported_snapshot"}
			out.Reasons["unsupported_snapshot"]++
		} else if !node.SnapshotKnown && !node.Withdrawn {
			node.Withdrawn, node.QuarantineReason = true, "unknown_snapshot"
			out.Reasons["unknown_snapshot"]++
		}
		nodes[id] = &node
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(nodes) > transcriptEvidenceCap {
		if err == nil {
			err = errTranscriptEvidenceBound
		}
		return out, err
	}
	if len(nodes) == 0 {
		return out, nil
	}
	notices, callbacks := map[string]string{}, map[recentCallback]string{}
	ids := make([]string, 0, len(nodes))
	for id := range nodes {
		ids = append(ids, id)
		notices[id] = id
	}
	rows, err = s.db.Query(ctx, `SELECT receipt_id::text,job_id::text,COALESCE(payload#>>'{command,completionCallback,responseUrl}','') FROM employee_event_consumption
 WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND scene_id=$4::uuid AND job_id::text=ANY($5::text[]) LIMIT $6`, append(scopeArgs(scope), ids, transcriptEvidenceCap+1)...)
	if err != nil {
		return out, err
	}
	bindings := 0
	for rows.Next() {
		var receipt, job, callback string
		if err = rows.Scan(&receipt, &job, &callback); err != nil {
			rows.Close()
			return out, err
		}
		notices[uuid.NewSHA1(uuid.MustParse(job), []byte("receipt:"+receipt)).String()] = job
		bindings++
		if pair, ok := recentReplyCallback(callback); ok {
			callbacks[pair] = job
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil || bindings > transcriptEvidenceCap {
		if err == nil {
			err = errTranscriptEvidenceBound
		}
		return out, err
	}
	replies := []deliveredMemoryReply{}
	rows, err = s.db.Query(ctx, `SELECT a.id,a.provider_message_id,COALESCE(a.input->>'scene_notice_id',''),CASE WHEN a.input->>'request_id'=a.request_id THEN COALESCE(a.input->>'callback_url','') ELSE '' END,a.request_id,COALESCE(n.job_id::text,''),COALESCE(h.source_id,'')
 FROM response_action a JOIN agent_scene sc ON sc.id=$4::uuid AND sc.workspace_id=$1::uuid AND sc.agent_id=$2::uuid AND sc.tenant_org_id=$3
 LEFT JOIN employee_run_notice n ON n.action_id=a.id AND n.workspace_id=a.workspace_id AND n.agent_id=a.agent_id AND n.tenant_org_id=$3 AND n.scene_id=$4::uuid
 LEFT JOIN employee_host_notice h ON h.action_id=a.id AND h.workspace_id=a.workspace_id AND h.agent_id=a.agent_id AND h.tenant_org_id=$3 AND h.scene_id=$4::uuid
 WHERE a.workspace_id=$1::uuid AND a.agent_id=$2::uuid AND a.kind='message.send' AND a.state='delivered' AND a.error_code='' AND a.provider_message_id<>'' AND a.provider_conversation_id=sc.external_scene_id
 AND a.input->>'workspace_id'=$1::text AND a.input->>'agent_id'=$2::text AND a.input->>'scene_id'=$4::text AND a.input->>'dws_org_id'=$3 AND a.input->>'conversation_id'=sc.external_scene_id
 AND a.updated_at>=$5 AND a.updated_at<$6 ORDER BY a.updated_at,a.id LIMIT $7`, append(scopeArgs(scope), since, before, transcriptEvidenceCap+1)...)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var reply deliveredMemoryReply
		var notice, callback, request, runJob, hostJob string
		if err = rows.Scan(&reply.action, &reply.message, &notice, &callback, &request, &runJob, &hostJob); err != nil {
			rows.Close()
			return out, err
		}
		reply.job = notices[notice]
		if reply.job == "" {
			reply.job = callbacks[recentCallback{URL: callback, RequestID: request}]
		}
		// Independent Run provenance wins over a reused callback URL.
		if nodes[runJob] != nil {
			reply.job = runJob
		} else if nodes[hostJob] != nil {
			reply.job = hostJob
		}
		replies = append(replies, reply)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(replies) > transcriptEvidenceCap {
		if err == nil {
			err = errTranscriptEvidenceBound
		}
		return out, err
	}
	replies, err = s.closeReplyAncestors(ctx, scope, before, nodes, replies, retired, &out)
	if err != nil {
		return out, err
	}
	for _, node := range nodes {
		if node.QuarantineReason != "" {
			slog.WarnContext(ctx, "employee assistant history quarantined", "event", "employee_history_assistant_quarantined", "workspace_id", scope.WorkspaceID, "agent_id", scope.AgentID, "scene_id", scope.SceneID, "job_id", node.ID, "reason", node.QuarantineReason)
		}
	}
	for changed := true; changed; {
		changed = false
		for _, node := range nodes {
			if node.Withdrawn {
				continue
			}
			for _, source := range node.Sources {
				if evidence[source] || out.Messages[source] {
					node.Withdrawn = true
				}
			}
			for _, source := range node.History {
				if out.Actions[source.ActionID] || out.Messages[source.MessageID] || evidence[source.MessageID] {
					node.Withdrawn = true
				}
			}
		}
		for _, reply := range replies {
			if node := nodes[reply.job]; node != nil && node.Withdrawn && !out.Actions[reply.action] {
				out.Actions[reply.action], out.Messages[reply.message] = true, true
				changed = true
			}
		}
	}
	return out, nil
}

func replyProvenance(id string, snapshot, journal []byte, retired map[string]string) (replyMemoryProvenance, error) {
	node := replyMemoryProvenance{ID: id, SnapshotKnown: knownReplySnapshot(snapshot)}
	var saved struct {
		Manifest []struct {
			ID      string `json:"id"`
			SceneID string `json:"scene_id"`
		} `json:"memory_manifest"`
		Input      struct{ RecentConversation string } `json:"input"`
		Transcript map[string]struct {
			MessageID string `json:"message_id"`
			Class     string `json:"sender_class"`
		} `json:"transcript_refs"`
	}
	// Empty legacy snapshots contain no structured provenance; never infer it
	// by scanning their prompt or reply text.
	if len(snapshot) > 0 && string(snapshot) != "null" {
		if err := json.Unmarshal(snapshot, &saved); err != nil {
			return node, err
		}
	}
	for _, record := range saved.Manifest {
		if origin := retired[record.ID]; origin != "" && record.SceneID == origin {
			node.Withdrawn = true
		}
	}
	if history := strings.TrimSpace(saved.Input.RecentConversation); strings.HasPrefix(history, "{") {
		var prior RecentConversation
		if err := json.Unmarshal([]byte(history), &prior); err != nil {
			return node, err
		}
		node.History = prior.Messages
	}
	for _, source := range saved.Transcript {
		node.Sources = append(node.Sources, source.MessageID)
		if source.Class == "self" {
			node.AssistantSources = append(node.AssistantSources, source.MessageID)
		}
	}
	var tools map[string]struct {
		Input  struct{ Name string } `json:"input"`
		Result struct {
			Result  struct{ Content, Receipt string } `json:"result"`
			Failure string                            `json:"failure"`
			Refused bool                              `json:"refused"`
		} `json:"result"`
	}
	if len(journal) > 0 && string(journal) != "null" {
		if err := json.Unmarshal(journal, &tools); err != nil {
			return node, err
		}
	}
	for _, tool := range tools {
		if tool.Result.Failure != "" || tool.Result.Refused || (tool.Input.Name != "memory_lookup" && tool.Input.Name != "memory_capture" && tool.Input.Name != "memory_forget") {
			continue
		}
		if retired[tool.Result.Result.Receipt] != "" {
			node.Withdrawn = true
		}
		var result any
		if json.Unmarshal([]byte(tool.Result.Result.Content), &result) == nil && memoryResultReferencesRetired(result, retired) {
			node.Withdrawn = true
		}
	}
	return node, nil
}

// Inspect only typed result reference fields, never a result's text value.
func memoryResultReferencesRetired(value any, retired map[string]string) bool {
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			if memoryResultReferencesRetired(item, retired) {
				return true
			}
		}
	case map[string]any:
		for _, key := range []string{"id", "record_ref"} {
			if id, ok := typed[key].(string); ok && retired[id] != "" {
				return true
			}
		}
		for _, key := range []string{"records", "record", "conflicting_statements"} {
			if memoryResultReferencesRetired(typed[key], retired) {
				return true
			}
		}
	}
	return false
}
