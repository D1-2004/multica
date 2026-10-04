package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/employeetask"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// employeeQuoteOrigin marks a task candidate the Host resolved from the exact
// message a source quotes. Only snapshots built behind the replica marker
// carry such candidates; older snapshots keep refusing quoted control.
const employeeQuoteOrigin = "quote"

// employeeQuoteCandidateLimit bounds quoted-task candidates per source.
const employeeQuoteCandidateLimit = 3

// employeeQuoteAnchors lists the Tasks a quoted provider message is a trusted
// anchor of, from Host facts only: messages the employee itself sent for a
// Task (acknowledgements of a scene job's committed task effects, run result
// notices, watchdog notices, task-wake notices and executor sends) and the
// requester's own request message that a committed task effect came from.
// Quoted text, message bodies and UUIDs a user typed are never consulted.
type employeeQuoteAnchors struct {
	// Sent are Tasks of a message the employee sent in this conversation.
	Sent []string
	// Requested are Tasks a committed effect created or changed from the
	// quoted message itself (the requester's own request).
	Requested []string
}

type employeeQueryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func employeeResolveQuoteAnchors(ctx context.Context, q employeeQueryer, scope employeeentry.Scope, conversationID, quotedID string) (employeeQuoteAnchors, error) {
	var out employeeQuoteAnchors
	if strings.TrimSpace(quotedID) == "" || strings.TrimSpace(conversationID) == "" {
		return out, nil
	}
	args := []any{scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, scope.SceneID, quotedID, conversationID}
	requestArgs := args[:5]
	// Direct Task anchors of an employee send, each fenced to this exact scene.
	rows, err := q.Query(ctx, `WITH sent AS (
  SELECT id FROM response_action WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND provider_message_id=$5 AND provider_conversation_id=$6 AND state<>'failed'
)
SELECT n.task_id::text FROM employee_run_notice n JOIN sent ON sent.id=n.action_id
 WHERE n.workspace_id=$1::uuid AND n.agent_id=$2::uuid AND n.tenant_org_id=$3 AND n.scene_id=$4::uuid
UNION
SELECT w.task_id::text FROM employee_watchdog_notice w JOIN sent ON sent.id=w.action_id
 WHERE w.workspace_id=$1::uuid AND w.agent_id=$2::uuid AND w.tenant_org_id=$3 AND w.scene_id=$4::uuid
UNION
SELECT r.task_id::text FROM sandbox_send_receipt s JOIN employee_task_run r ON r.queue_task_id=s.task_id AND r.workspace_id=s.workspace_id AND r.agent_id=s.agent_id AND r.tenant_org_id=$3
 WHERE s.workspace_id=$1::uuid AND s.agent_id=$2::uuid AND s.provider_message_id=$5 AND s.provider_conversation_id=$6 AND s.state<>'failed'`, args...)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return out, err
		}
		out.Sent = append(out.Sent, id)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return out, err
	}
	// A scene notice names the job whose reply it is: a message window's
	// acknowledgement or a task wake's notice.
	rows, err = q.Query(ctx, `SELECT j.kind,j.items,COALESCE(j.tool_journal,'{}'::jsonb) FROM employee_scene_job j
 JOIN response_action a ON a.input->>'scene_notice_id'=j.id::text
 WHERE a.workspace_id=$1::uuid AND a.agent_id=$2::uuid AND a.provider_message_id=$5 AND a.provider_conversation_id=$6 AND a.state<>'failed'
   AND j.workspace_id=$1::uuid AND j.agent_id=$2::uuid AND j.tenant_org_id=$3 AND j.scene_id=$4::uuid`, args...)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var kind string
		var items, journal []byte
		if err = rows.Scan(&kind, &items, &journal); err != nil {
			rows.Close()
			return out, err
		}
		if kind == "task_wake" {
			out.Sent = append(out.Sent, employeeTaskWakeTaskID(items))
			continue
		}
		out.Sent = append(out.Sent, employeeCommittedTaskEffects(journal, "")...)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return out, err
	}
	// A reply sent through the source's completion callback (every native or
	// Router message): the action answers the consumption whose frozen
	// response callback it carries, so it anchors that receipt's effects.
	rows, err = q.Query(ctx, `SELECT c.receipt_id::text,COALESCE(j.tool_journal,'{}'::jsonb) FROM response_action a
 JOIN employee_event_consumption c ON c.payload#>>'{command,completionCallback,responseUrl}'=a.input->>'callback_url'
 JOIN employee_scene_job j ON j.id=c.job_id AND j.workspace_id=c.workspace_id AND j.agent_id=c.agent_id AND j.tenant_org_id=c.tenant_org_id AND j.scene_id=c.scene_id
 WHERE a.workspace_id=$1::uuid AND a.agent_id=$2::uuid AND a.provider_message_id=$5 AND a.provider_conversation_id=$6 AND a.state<>'failed'
   AND a.input->>'request_id'=a.request_id AND a.request_id LIKE 'multica-terminal:sync-completed:%' AND COALESCE(a.input->>'callback_url','')<>''
   AND c.workspace_id=$1::uuid AND c.agent_id=$2::uuid AND c.tenant_org_id=$3 AND c.scene_id=$4::uuid AND c.owner_loop='employee'`, args...)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var receipt string
		var journal []byte
		if err = rows.Scan(&receipt, &journal); err != nil {
			rows.Close()
			return out, err
		}
		out.Sent = append(out.Sent, employeeCommittedTaskEffects(journal, receipt+"/")...)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return out, err
	}
	// The requester's own request message: the committed task effects whose
	// source is exactly that message.
	rows, err = q.Query(ctx, `SELECT c.receipt_id::text,COALESCE(j.tool_journal,'{}'::jsonb) FROM employee_event_consumption c
 JOIN employee_scene_job j ON j.id=c.job_id AND j.workspace_id=c.workspace_id AND j.agent_id=c.agent_id AND j.tenant_org_id=c.tenant_org_id AND j.scene_id=c.scene_id
 WHERE c.workspace_id=$1::uuid AND c.agent_id=$2::uuid AND c.tenant_org_id=$3 AND c.scene_id=$4::uuid AND c.owner_loop='employee'
   AND jsonb_typeof(c.payload#>'{command,event,data,messages}')='array'
   AND EXISTS(SELECT 1 FROM jsonb_array_elements(c.payload#>'{command,event,data,messages}') m WHERE m->>'openMsgId'=$5)`, requestArgs...)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var receipt string
		var journal []byte
		if err = rows.Scan(&receipt, &journal); err != nil {
			rows.Close()
			return out, err
		}
		out.Requested = append(out.Requested, employeeCommittedTaskEffects(journal, receipt+"/"+quotedID)...)
	}
	rows.Close()
	return out, rows.Err()
}

func employeeTaskWakeTaskID(items []byte) string {
	var decoded []employeeentry.Item
	if json.Unmarshal(items, &decoded) != nil || len(decoded) != 1 {
		return ""
	}
	var wake employeeentry.TaskWake
	if json.Unmarshal(decoded[0].Payload, &wake) != nil {
		return ""
	}
	return wake.TaskID
}

// employeeCommittedTaskEffects returns the Tasks a job's journal shows a
// committed task effect for; sourceRef, when set, keeps only effects of that
// exact source message, or of any message of a receipt when it ends in "/".
func employeeCommittedTaskEffects(journal []byte, sourceRef string) []string {
	var entries map[string]struct {
		Input struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		} `json:"input"`
		Result employeeToolRecord `json:"result"`
	}
	if json.Unmarshal(journal, &entries) != nil {
		return nil
	}
	var out []string
	for _, entry := range entries {
		switch entry.Input.Name {
		case "dispatch_task", "continue_task", "steer_task", "stop_task":
		default:
			continue
		}
		if entry.Result.Failure != "" || entry.Result.Result.Receipt == "" {
			continue
		}
		ref, _ := entry.Input.Arguments["source_ref"].(string)
		if sourceRef != "" && ref != sourceRef && !(strings.HasSuffix(sourceRef, "/") && strings.HasPrefix(ref, sourceRef)) {
			continue
		}
		var content struct {
			TaskID string `json:"task_id"`
		}
		if json.Unmarshal([]byte(entry.Result.Result.Content), &content) == nil && content.TaskID != "" {
			out = append(out, content.TaskID)
		}
	}
	return out
}

// quotedTaskCandidates resolves, for each source that quotes a message, the
// Tasks that message is a trusted anchor of, after the provider confirms the
// quote: the outer message is the frozen requester's, it quotes exactly that
// message in this scene's conversation, and the quoted message was sent by
// the employee (a send anchor) or by the requester (a request anchor). Only
// the requester's own Employee Direct Tasks in this scene qualify. Any failure
// leaves the source without quoted candidates.
func (w *EmployeeSceneWorker) quotedTaskCandidates(ctx context.Context, job employeeentry.Job, envelopes []employeeDispatchEnvelope, store *employeetask.Store, scope employeetask.Scope) (map[string][]employeetask.Task, error) {
	out := map[string][]employeetask.Task{}
	quoting := false
	for i, item := range job.Items {
		for _, source := range employeeSourceMessages(item, envelopes[i]) {
			if employeeSourceQuotes(source) {
				quoting = true
			}
		}
	}
	// Producer gate: an older replica would replay these candidates with the
	// old refusal and diverge from the journaled outcome.
	if !quoting || w.ReplicaReady == nil || w.ReplicaReady(ctx) != nil {
		return out, nil
	}
	database, ok := employeeEntryDB(w.handler)
	if !ok {
		return nil, errors.New("employee task storage is unavailable")
	}
	provider := w.ResourceProvider
	if provider == nil && w.handler.DingTalkResponses != nil {
		provider = w.handler.DingTalkResponses
	}
	if provider == nil {
		return out, nil
	}
	reader := newEmployeeMessageResourceReader(w, provider)
	target, _, err := reader.bind(ctx, job, envelopes)
	if err != nil {
		slog.WarnContext(ctx, "employee quoted task resolution skipped", "event", "employee_quote_unresolved", "job_id", job.ID, "reason", "bind", "error", err)
		return out, nil
	}
	selfOpenID := ""
	if subscription, e := w.handler.Queries.GetAgentDWSNativeSubscription(ctx, db.GetAgentDWSNativeSubscriptionParams{WorkspaceID: parseUUID(job.Scope.WorkspaceID), AgentID: parseUUID(job.Scope.AgentID)}); e == nil {
		selfOpenID = strings.TrimSpace(subscription.SelfOpenDingtalkID)
	}
	for i, item := range job.Items {
		for _, source := range employeeSourceMessages(item, envelopes[i]) {
			if !employeeSourceQuotes(source) || source.RequesterRef == "" {
				continue
			}
			quotedID := strings.TrimSpace(source.Message.ReferencedMessage.OpenMsgID)
			anchors, err := employeeResolveQuoteAnchors(ctx, database, job.Scope, target.conversation, quotedID)
			if err != nil {
				return nil, err
			}
			if len(anchors.Sent) == 0 && len(anchors.Requested) == 0 {
				continue
			}
			bySent, reason := w.verifyQuote(ctx, provider, target, source, quotedID, selfOpenID)
			if reason != "" {
				slog.InfoContext(ctx, "employee quoted task not verified", "event", "employee_quote_unresolved", "job_id", job.ID, "source_ref", source.SourceRef, "reason", reason)
				continue
			}
			ids := anchors.Requested
			if bySent {
				ids = anchors.Sent
			}
			seen := map[string]bool{}
			for _, id := range ids {
				if id == "" || seen[id] || len(out[source.SourceRef]) >= employeeQuoteCandidateLimit {
					continue
				}
				seen[id] = true
				task, err := store.Get(ctx, scope, id)
				if errors.Is(err, employeetask.ErrNotFound) {
					continue
				}
				if err != nil {
					return nil, err
				}
				if task.RequesterRef != source.RequesterRef || task.OwnerLoop != employeetask.LoopEmployee || task.DispatchMode != employeetask.DispatchDirect {
					continue
				}
				out[source.SourceRef] = append(out[source.SourceRef], task)
			}
		}
	}
	return out, nil
}

func employeeSourceQuotes(source employeeSourceMessage) bool {
	return source.Message.Reaction == nil && source.Message.ReferencedMessage != nil && strings.TrimSpace(source.Message.ReferencedMessage.OpenMsgID) != ""
}

// verifyQuote re-reads both messages from the provider as the agent. It
// returns whether the quoted message was sent by the employee, or a reason it
// cannot anchor this source.
func (w *EmployeeSceneWorker) verifyQuote(ctx context.Context, provider employeeResourceProvider, target employeeResourceTarget, source employeeSourceMessage, quotedID, selfOpenID string) (bool, string) {
	requester := strings.TrimSpace(source.Message.SenderOpenDingTalkID)
	if requester == "" {
		return false, "requester_unknown"
	}
	outer, err := provider.ReadMessageResources(ctx, target.input, target.conversation, source.Message.OpenMsgID)
	if err != nil {
		return false, "outer_unverified"
	}
	if outer.SenderOpenDingTalkID != requester {
		return false, "requester_mismatch"
	}
	if outer.Quoted == nil || outer.Quoted.MessageID != quotedID || outer.Quoted.ConversationID != target.conversation {
		return false, "quote_mismatch"
	}
	quoted, err := provider.ReadMessageResources(ctx, target.input, target.conversation, quotedID)
	if err != nil {
		return false, "quoted_unverified"
	}
	switch {
	case quoted.SenderOpenDingTalkID == requester:
		return false, ""
	case selfOpenID != "" && quoted.SenderOpenDingTalkID == selfOpenID:
		return true, ""
	case selfOpenID == "" && quoted.SenderOpenDingTalkID != "":
		// The account's own openDingTalkId is not learned yet: the provider
		// still confirms the message exists here, and only an employee send
		// recorded by the Host can anchor it.
		return true, ""
	default:
		return false, "quoted_sender_unverified"
	}
}

// employeeQuotedControl enforces the quoted-control contract: a source that
// quotes a message may continue or stop only a Host-verified quoted
// candidate, and a quoted candidate serves only its quoting source. A snapshot
// built before the contract has no quoted candidates, so quote replies keep
// their old refusal there.
func employeeQuotedControl(source employeeSourceMessage, binding employeeCurrentTaskBinding) error {
	quoting := source.Message.ReferencedMessage != nil
	if quoting && binding.Origin != employeeQuoteOrigin {
		return errors.New("a quote reply can control only the task its quoted message belongs to")
	}
	if !quoting && binding.Origin == employeeQuoteOrigin {
		return errors.New("quoted task candidate requires its quoting source")
	}
	return nil
}

// employeeWindowHasReaction reports a window carrying an emoji reaction. The
// reaction guidance is frozen only into such new snapshots.
func employeeWindowHasReaction(envelopes []employeeDispatchEnvelope) bool {
	for _, env := range envelopes {
		for _, m := range env.Command.Event.Data.Messages {
			if m.Reaction != nil {
				return true
			}
		}
	}
	return false
}
