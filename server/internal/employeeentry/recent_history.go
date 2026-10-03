package employeeentry

import (
	"context"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	RecentConversationWindow       = 24 * time.Hour
	RecentConversationMessageLimit = 20
	RecentConversationByteLimit    = 16 << 10
)

type RecentConversationRequest struct {
	Scope                            Scope
	PrincipalID                      string
	Before                           time.Time
	ExcludeReceipts, ExcludeMessages []string
	// MemoryPrincipal is the unique DM requester's org-qualified memory
	// principal. Its private reset also bounds the history; leave it empty
	// in groups, where a personal reset never hides shared dialogue.
	MemoryPrincipal string
}

// RecentConversationCoverage names what a snapshot can contain: admitted user
// text, provider-confirmed replies linked to this principal's sources, and
// Host sends into the scene that carry no source linkage (routine notices).
const RecentConversationCoverage = "admitted_user_text_and_verified_host_replies;scene_host_sends"

// RecentConversation is bounded dialogue evidence, never durable memory or an
// authorization source. Coverage deliberately excludes unobserved provider history.
type RecentConversation struct {
	Coverage                       string                      `json:"coverage"`
	Since                          time.Time                   `json:"since"`
	Before                         time.Time                   `json:"before"`
	MaxMessages                    int                         `json:"max_messages"`
	MaxBytes                       int                         `json:"max_bytes"`
	Truncated                      bool                        `json:"truncated"`
	WithdrawnMemoryEvidenceOmitted bool                        `json:"withdrawn_memory_evidence_omitted,omitempty"`
	Messages                       []RecentConversationMessage `json:"messages"`
}

type RecentConversationMessage struct {
	Role string `json:"role"`
	Text string `json:"text"`
	// At is admission time for users and provider confirmation time for replies.
	At            time.Time `json:"observed_at"`
	MessageID     string    `json:"message_id,omitempty"`
	ReceiptID     string    `json:"receipt_id,omitempty"`
	ActionID      string    `json:"action_id,omitempty"`
	Speaker       string    `json:"speaker,omitempty"`
	SpeakerRef    string    `json:"speaker_ref,omitempty"`
	OriginalBytes int       `json:"original_bytes"`
	Truncated     bool      `json:"truncated,omitempty"`
	sequence      int64
}

// RecentConversation only reads admitted text and provider-confirmed Host sends.
// The caller must fence the current scene and invocation principal before use.
// Neither model output nor a callback/outbox acknowledgement proves a reply.
func (s *Store) RecentConversation(ctx context.Context, request RecentConversationRequest) (RecentConversation, error) {
	if s == nil || s.db == nil || !validScope(request.Scope) || !validID(request.PrincipalID) || request.Before.IsZero() || len(request.ExcludeReceipts) > MaxWindowItems || len(request.ExcludeMessages) > MaxWindowMessages || len(request.MemoryPrincipal) > 256 || strings.TrimSpace(request.MemoryPrincipal) != request.MemoryPrincipal {
		return RecentConversation{}, ErrInvalid
	}
	// Host timestamps reach the model as Asia/Shanghai with an explicit offset;
	// a bare UTC clock was read as local time ("ends around 11:34" for 19:34).
	before := HostTime(request.Before)
	out := RecentConversation{Coverage: RecentConversationCoverage, Since: before.Add(-RecentConversationWindow), Before: before, MaxMessages: RecentConversationMessageLimit, MaxBytes: RecentConversationByteLimit, Messages: []RecentConversationMessage{}}
	resetAt, err := s.memoryResetAt(ctx, request.Scope, request.MemoryPrincipal)
	if err != nil {
		return RecentConversation{}, err
	}
	if resetAt.After(out.Since) {
		// A memory reset starts a new conversation: earlier dialogue is not
		// replayed into later wakes, even inside the 24-hour window.
		out.Since = HostTime(resetAt)
	}
	excludedReceipts := append([]string{}, request.ExcludeReceipts...)
	excludedMessages := append([]string{}, request.ExcludeMessages...)
	const candidates = RecentConversationMessageLimit * 2
	args := append(scopeArgs(request.Scope), request.PrincipalID, out.Since, before, excludedReceipts, excludedMessages, candidates+1)
	rows, err := s.db.Query(ctx, `SELECT c.receipt_id::text,COALESCE(c.job_id::text,''),c.created_at,m.ordinal,
 COALESCE(m.value->>'openMsgId',''),left(COALESCE(m.value->>'senderDisplayName',''),128),octet_length(COALESCE(m.value->>'senderDisplayName','')),left(m.value->>'text',2048),octet_length(m.value->>'text'),
 CASE WHEN COALESCE(m.value->>'senderUid','')<>'' THEN 'uid:'||(m.value->>'senderUid') WHEN COALESCE(m.value->>'senderOpenDingTalkId','')<>'' THEN 'open_id:'||(m.value->>'senderOpenDingTalkId') WHEN COALESCE(m.value->>'senderStaffId','')<>'' THEN 'staff_id:'||(m.value->>'senderStaffId') ELSE '' END,
 COALESCE(c.payload#>>'{command,completionCallback,responseUrl}',''),COALESCE(c.payload#>>'{command,externalIdentity,dws,uid}','')
 FROM employee_event_consumption c JOIN scene_event_receipt r ON r.id=c.receipt_id AND r.workspace_id=c.workspace_id AND r.agent_id=c.agent_id AND r.tenant_org_id=c.tenant_org_id AND r.scene_id=c.scene_id AND r.principal_id=c.principal_id
 CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(c.payload#>'{command,event,data,messages}')='array' THEN c.payload#>'{command,event,data,messages}' ELSE '[]'::jsonb END) WITH ORDINALITY m(value,ordinal)
 WHERE c.workspace_id=$1::uuid AND c.agent_id=$2::uuid AND c.tenant_org_id=$3 AND c.scene_id=$4::uuid AND c.principal_id=$5::uuid
 AND c.state IN ('queued','completed','delegated') AND c.reason='' AND r.reason='' AND r.envelope->>'category'='user_message'
 AND ((r.route='unified' AND r.state='ready') OR (r.route='legacy' AND r.state='legacy'))
 AND c.payload->>'principal_id'=c.principal_id::text AND c.payload#>>'{command,event_receipt_id}'=c.receipt_id::text AND c.payload#>>'{command,agent_scene,scene_id}'=c.scene_id::text
 AND c.created_at>=$6 AND c.created_at<$7 AND NOT(c.receipt_id::text=ANY($8::text[])) AND NOT(COALESCE(m.value->>'openMsgId','')=ANY($9::text[]))
 AND jsonb_typeof(m.value->'text')='string' AND btrim(m.value->>'text')<>''
 ORDER BY c.created_at DESC,c.receipt_id DESC,m.ordinal DESC LIMIT $10`, args...)
	if err != nil {
		return RecentConversation{}, err
	}
	noticeIDs, jobs, dwsIDs, receipts := []string{}, []string{}, []string{}, []string{}
	callbacks := []recentCallback{}
	for rows.Next() {
		var message RecentConversationMessage
		var job, callback, dwsID string
		var speakerBytes int
		if err = rows.Scan(&message.ReceiptID, &job, &message.At, &message.sequence, &message.MessageID, &message.Speaker, &speakerBytes, &message.Text, &message.OriginalBytes, &message.SpeakerRef, &callback, &dwsID); err != nil {
			rows.Close()
			return RecentConversation{}, err
		}
		if len(out.Messages) == candidates {
			out.Truncated = true
			break
		}
		message.Role = "user"
		boundRecentMessage(&message)
		message.Truncated = message.Truncated || speakerBytes > len(message.Speaker)
		out.Truncated = out.Truncated || message.Truncated
		out.Messages = append(out.Messages, message)
		receipts = append(receipts, message.ReceiptID)
		if validID(job) {
			noticeIDs = append(noticeIDs, job, uuid.NewSHA1(uuid.MustParse(job), []byte("receipt:"+message.ReceiptID)).String())
			jobs = append(jobs, job)
		}
		if pair, ok := recentReplyCallback(callback); ok {
			callbacks = append(callbacks, pair)
		}
		if dwsID != "" {
			dwsIDs = append(dwsIDs, dwsID)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return RecentConversation{}, err
	}
	withdrawn, err := s.withdrawnRecentEvidence(ctx, request, receipts, jobs)
	if err != nil {
		return RecentConversation{}, err
	}
	if len(withdrawn.sources) > 0 || withdrawn.replies {
		out.WithdrawnMemoryEvidenceOmitted = true
		kept := out.Messages[:0]
		for _, message := range out.Messages {
			if !withdrawn.sources[message.ReceiptID+"/"+message.MessageID] {
				kept = append(kept, message)
			}
		}
		out.Messages = kept
	}
	withdrawnReplies, err := s.withdrawnMemoryReplyIDs(ctx, request.Scope, out.Since, before)
	if err != nil {
		return RecentConversation{}, err
	}
	kept := out.Messages[:0]
	for _, message := range out.Messages {
		if withdrawnReplies.Sources[message.MessageID] {
			out.WithdrawnMemoryEvidenceOmitted = true
			continue
		}
		kept = append(kept, message)
	}
	out.Messages = kept
	// A send must link to this principal's admitted sources: a foreground
	// scene notice, the original response callback, or a persisted Run notice.
	// A Host send that carries no source linkage at all (a routine notice) was
	// posted to the whole scene and is history for every principal of it.
	callbackJSON, _ := json.Marshal(callbacks)
	withdrawnCallbackJSON, _ := json.Marshal(withdrawn.callbacks)
	// Host-initiated notices (task wakes, invitations, watchdog) join through
	// their own fact row for this scene and principal. A withdrawal of any
	// memory evidence from their origin receipt conservatively hides them.
	args = append(scopeArgs(request.Scope), out.Since, before, noticeIDs, callbackJSON, jobs, dwsIDs, candidates+1, excludedMessages, withdrawn.notices, withdrawnCallbackJSON, withdrawn.jobs, request.PrincipalID)
	rows, err = s.db.Query(ctx, `SELECT a.id,a.provider_message_id,a.updated_at,left(a.input->>'text',2048),octet_length(a.input->>'text')
 FROM response_action a JOIN agent_scene sc ON sc.id=$4::uuid AND sc.workspace_id=$1::uuid AND sc.agent_id=$2::uuid AND sc.tenant_org_id=$3
 WHERE a.workspace_id=$1::uuid AND a.agent_id=$2::uuid AND a.kind='message.send' AND a.state='delivered' AND a.error_code='' AND a.provider_message_id<>'' AND a.provider_conversation_id=sc.external_scene_id
 AND a.input->>'workspace_id'=$1::text AND a.input->>'agent_id'=$2::text AND a.input->>'scene_id'=$4::text AND a.input->>'dws_org_id'=$3 AND a.input->>'conversation_id'=sc.external_scene_id
 AND a.updated_at>=$5 AND a.updated_at<$6 AND jsonb_typeof(a.input->'text')='string' AND btrim(a.input->>'text')<>''
 AND NOT(a.provider_message_id=ANY($12::text[]))
 AND ((a.input->>'dws_uid'=ANY($10::text[]) AND (a.input->>'scene_notice_id'=ANY($7::text[]) OR (a.input->>'request_id'=a.request_id AND $8::jsonb @> jsonb_build_array(jsonb_build_object('url',a.input->>'callback_url','request_id',a.request_id))) OR EXISTS(SELECT 1 FROM employee_run_notice n WHERE n.action_id=a.id AND n.workspace_id=a.workspace_id AND n.agent_id=a.agent_id AND n.tenant_org_id=$3 AND n.scene_id=$4::uuid AND n.job_id::text=ANY($9::text[]) AND n.state='enqueued')))
  OR EXISTS(SELECT 1 FROM employee_host_notice h WHERE h.action_id=a.id AND h.workspace_id=a.workspace_id AND h.agent_id=a.agent_id AND h.tenant_org_id=$3 AND h.scene_id=$4::uuid AND h.principal_id=$16::uuid
   AND NOT EXISTS(SELECT 1 FROM employee_learning l WHERE l.workspace_id=h.workspace_id AND l.agent_id=h.agent_id AND l.tenant_org_id=h.tenant_org_id AND l.scene_id=h.scene_id AND l.scope_kind='private'
    AND (l.superseded_by IS NOT NULL OR l.forgotten_at IS NOT NULL) AND l.record->>'source_id'='employee-message:'||h.origin_receipt_id::text))
  OR (COALESCE(a.input->>'scene_notice_id','')='' AND COALESCE(a.input->>'callback_url','')='' AND COALESCE(a.input->>'employee_run_notice_id','')='' AND COALESCE(a.input->>'invitation_action_id','')='' AND COALESCE(a.input->>'coordinator_wait_job_id','')=''
   AND NOT EXISTS(SELECT 1 FROM employee_run_notice n WHERE n.action_id=a.id) AND NOT EXISTS(SELECT 1 FROM employee_host_notice h WHERE h.action_id=a.id)))
 AND NOT(COALESCE(a.input->>'scene_notice_id'=ANY($13::text[]),false) OR $14::jsonb @> jsonb_build_array(jsonb_build_object('url',a.input->>'callback_url','request_id',a.request_id)) OR EXISTS(SELECT 1 FROM employee_run_notice n WHERE n.action_id=a.id AND n.workspace_id=a.workspace_id AND n.agent_id=a.agent_id AND n.tenant_org_id=$3 AND n.scene_id=$4::uuid AND n.job_id::text=ANY($15::text[])))
 ORDER BY a.updated_at DESC,a.id DESC LIMIT $11`, args...)
	if err != nil {
		return RecentConversation{}, err
	}
	count := 0
	for rows.Next() {
		var message RecentConversationMessage
		if err = rows.Scan(&message.ActionID, &message.MessageID, &message.At, &message.Text, &message.OriginalBytes); err != nil {
			rows.Close()
			return RecentConversation{}, err
		}
		if count == candidates {
			out.Truncated = true
			break
		}
		count++
		if withdrawnReplies.Actions[message.ActionID] || withdrawnReplies.Messages[message.MessageID] {
			out.WithdrawnMemoryEvidenceOmitted = true
			continue
		}
		message.Role = "assistant"
		boundRecentMessage(&message)
		out.Truncated = out.Truncated || message.Truncated
		out.Messages = append(out.Messages, message)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return RecentConversation{}, err
	}
	sort.SliceStable(out.Messages, func(i, j int) bool {
		a, b := out.Messages[i], out.Messages[j]
		if !a.At.Equal(b.At) {
			return a.At.Before(b.At)
		}
		if a.Role != b.Role {
			return a.Role == "user"
		}
		if a.ReceiptID != b.ReceiptID {
			return a.ReceiptID < b.ReceiptID
		}
		if a.sequence != b.sequence {
			return a.sequence < b.sequence
		}
		return a.ActionID < b.ActionID
	})
	if len(out.Messages) > RecentConversationMessageLimit {
		out.Messages = out.Messages[len(out.Messages)-RecentConversationMessageLimit:]
		out.Truncated = true
	}
	for {
		raw, err := json.Marshal(out)
		if err != nil {
			return RecentConversation{}, err
		}
		if len(raw) <= RecentConversationByteLimit {
			break
		}
		out.Truncated = true
		out.Messages = out.Messages[1:]
	}
	return out, nil
}

// memoryResetAt is the latest reset of the scene memory and, for a DM
// requester, of that requester's private memory. Zero when never reset.
func (s *Store) memoryResetAt(ctx context.Context, scope Scope, memoryPrincipal string) (time.Time, error) {
	var resetAt *time.Time
	err := s.db.QueryRow(ctx, `SELECT max(reset_at) FROM employee_memory_state WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND scene_id=$4::uuid
 AND ((scope_kind='scene' AND principal_id='') OR ($5<>'' AND scope_kind='private' AND principal_id=$5))`, append(scopeArgs(scope), memoryPrincipal)...).Scan(&resetAt)
	if err != nil || resetAt == nil {
		return time.Time{}, err
	}
	return resetAt.UTC(), nil
}

type recentWithdrawals struct {
	sources       map[string]bool
	notices, jobs []string
	callbacks     []recentCallback
	// replies is set when a capture's own confirmation was withdrawn.
	replies bool
}

// Only exact, scoped memory_capture evidence withdraws dialogue. Never inspect
// insight text or guess that an unrelated statement repeats a retired value.
// A reply may combine multiple messages, so its original job/callback is the
// smallest safe unit to omit; other user messages in that job remain visible.
func (s *Store) withdrawnRecentEvidence(ctx context.Context, request RecentConversationRequest, receipts, jobs []string) (recentWithdrawals, error) {
	out := recentWithdrawals{sources: map[string]bool{}, notices: []string{}, callbacks: []recentCallback{}, jobs: []string{}}
	if len(receipts) == 0 {
		return out, nil
	}
	args := append(scopeArgs(request.Scope), request.PrincipalID, receipts, jobs)
	rows, err := s.db.Query(ctx, `SELECT DISTINCT c.receipt_id::text,COALESCE(c.job_id::text,''),l.record->>'evidence_id',COALESCE(other.receipt_id::text,c.receipt_id::text),COALESCE(other.payload#>>'{command,completionCallback,responseUrl}',c.payload#>>'{command,completionCallback,responseUrl}','')
 FROM employee_event_consumption c
 CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(c.payload#>'{command,event,data,messages}')='array' THEN c.payload#>'{command,event,data,messages}' ELSE '[]'::jsonb END) m(value)
 JOIN employee_learning l ON l.workspace_id=c.workspace_id AND l.agent_id=c.agent_id AND l.tenant_org_id=c.tenant_org_id AND l.scene_id=c.scene_id
 AND l.scope_kind='private' AND (l.superseded_by IS NOT NULL OR l.forgotten_at IS NOT NULL)
 AND l.record->>'source_id'='employee-message:'||c.receipt_id::text AND l.record->>'evidence_id'=m.value->>'openMsgId'
 AND l.principal_id='dingtalk:'||c.tenant_org_id||':'||CASE WHEN btrim(COALESCE(m.value->>'senderUid',''))<>'' THEN 'uid:'||btrim(m.value->>'senderUid') WHEN btrim(COALESCE(m.value->>'senderOpenDingTalkId',''))<>'' THEN 'open_id:'||btrim(m.value->>'senderOpenDingTalkId') WHEN btrim(COALESCE(m.value->>'senderStaffId',''))<>'' THEN 'staff_id:'||btrim(m.value->>'senderStaffId') ELSE '' END
 LEFT JOIN employee_event_consumption other ON other.job_id=c.job_id AND other.workspace_id=c.workspace_id AND other.agent_id=c.agent_id AND other.tenant_org_id=c.tenant_org_id AND other.scene_id=c.scene_id AND other.principal_id=c.principal_id
 WHERE c.workspace_id=$1::uuid AND c.agent_id=$2::uuid AND c.tenant_org_id=$3 AND c.scene_id=$4::uuid AND c.principal_id=$5::uuid
 AND (c.receipt_id::text=ANY($6::text[]) OR c.job_id::text=ANY($7::text[]))`, args...)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var receipt, job, message, replyReceipt, callback string
		if err := rows.Scan(&receipt, &job, &message, &replyReceipt, &callback); err != nil {
			rows.Close()
			return out, err
		}
		out.sources[receipt+"/"+message] = true
		out.withdrawReplies(job, replyReceipt, callback)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	// A capture grounded in other evidence (an overheard group line) names the
	// admitted message that requested it. Its withdrawal hides that job's
	// confirmation reply, which may repeat the value, but not the request.
	rows, err = s.db.Query(ctx, `SELECT DISTINCT COALESCE(c.job_id::text,''),COALESCE(other.receipt_id::text,c.receipt_id::text),COALESCE(other.payload#>>'{command,completionCallback,responseUrl}',c.payload#>>'{command,completionCallback,responseUrl}','')
 FROM employee_event_consumption c
 JOIN employee_learning l ON l.workspace_id=c.workspace_id AND l.agent_id=c.agent_id AND l.tenant_org_id=c.tenant_org_id AND l.scene_id=c.scene_id
 AND (l.superseded_by IS NOT NULL OR l.forgotten_at IS NOT NULL) AND l.record->>'capture_source_id'='employee-message:'||c.receipt_id::text
 LEFT JOIN employee_event_consumption other ON other.job_id=c.job_id AND other.workspace_id=c.workspace_id AND other.agent_id=c.agent_id AND other.tenant_org_id=c.tenant_org_id AND other.scene_id=c.scene_id AND other.principal_id=c.principal_id
 WHERE c.workspace_id=$1::uuid AND c.agent_id=$2::uuid AND c.tenant_org_id=$3 AND c.scene_id=$4::uuid AND c.principal_id=$5::uuid
 AND (c.receipt_id::text=ANY($6::text[]) OR c.job_id::text=ANY($7::text[]))`, args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var job, replyReceipt, callback string
		if err := rows.Scan(&job, &replyReceipt, &callback); err != nil {
			return out, err
		}
		out.replies = true
		out.withdrawReplies(job, replyReceipt, callback)
	}
	return out, rows.Err()
}

func (w *recentWithdrawals) withdrawReplies(job, replyReceipt, callback string) {
	if validID(job) {
		w.jobs = append(w.jobs, job)
		w.notices = append(w.notices, job, uuid.NewSHA1(uuid.MustParse(job), []byte("receipt:"+replyReceipt)).String())
	}
	if pair, ok := recentReplyCallback(callback); ok {
		w.callbacks = append(w.callbacks, pair)
	}
}

type recentCallback struct {
	URL       string `json:"url"`
	RequestID string `json:"request_id"`
}

var recentCallbackPath = regexp.MustCompile(`^/api/v1/dispatch-tasks/([A-Za-z0-9_-]{1,128})/response-receipt$`)

// EnqueueSynchronousWorkReceipt uses this exact request identity. A response URL
// alone is shared with wrap-ups and other runs, so it is never a provenance key.
func recentReplyCallback(url string) (recentCallback, bool) {
	parts := recentCallbackPath.FindStringSubmatch(url)
	if len(parts) != 2 {
		return recentCallback{}, false
	}
	return recentCallback{URL: url, RequestID: "multica-terminal:sync-completed:" + parts[1]}, true
}

func boundRecentMessage(message *RecentConversationMessage) {
	message.At = HostTime(message.At)
	message.Text = clipRecentText(message.Text, 2048)
	message.Speaker = clipRecentText(message.Speaker, 128)
	message.Truncated = message.OriginalBytes > len(message.Text)
}

func clipRecentText(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	end := limit - len("…")
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return strings.Clone(text[:end]) + "…"
}
