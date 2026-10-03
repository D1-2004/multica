package employeeverification

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/multica-ai/multica/server/internal/employeetask"
)

// EvidenceSource supplies structured evidence for one Run. Metadata is read
// through the caller's Querier; ReadArtifact performs object-store I/O and is
// always called without database locks held.
type EvidenceSource interface {
	RunArtifacts(ctx context.Context, q Querier, scope employeetask.Scope, taskID, runID string) ([]Artifact, error)
	ReadArtifact(ctx context.Context, a Artifact) ([]byte, error)
	RunDeliveries(ctx context.Context, q Querier, scope employeetask.Scope, taskID, runID string) ([]Delivery, error)
}

// ArtifactReader returns the stored plaintext of one Run artifact. The Host
// adapter opens the sealed Storage object; tests use an in-memory fixture.
type ArtifactReader func(ctx context.Context, a Artifact) ([]byte, error)

// PGEvidence reads artifact and delivery metadata from the Host ledgers
// (employee_task_artifact, employee_run_notice -> response_action) and
// delegates byte reads to Reader.
type PGEvidence struct {
	Reader ArtifactReader
	// Files reads files the Run delivered through the provider (G1.1).
	Files FileReader
}

var errNoReader = errors.New("employee verification: artifact reader unavailable")

func (p PGEvidence) RunArtifacts(ctx context.Context, q Querier, scope employeetask.Scope, taskID, runID string) ([]Artifact, error) {
	rows, err := q.Query(ctx, `SELECT attachment_id::text,task_id::text,run_id::text,queue_task_id::text,goal_revision,filename,content_type,sha256,size_bytes,state
FROM employee_task_artifact WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND scene_id=$4::uuid AND task_id=$5::uuid AND run_id=$6::uuid AND state IN ('pending','ready')
ORDER BY created_at,attachment_id`, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, scope.Scene.SceneID, taskID, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Artifact
	for rows.Next() {
		var a Artifact
		if err := rows.Scan(&a.AttachmentID, &a.TaskID, &a.RunID, &a.QueueTaskID, &a.GoalRevision, &a.Filename, &a.ContentType, &a.SHA256, &a.Size, &a.State); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (p PGEvidence) ReadArtifact(ctx context.Context, a Artifact) ([]byte, error) {
	if p.Reader == nil {
		return nil, errNoReader
	}
	return p.Reader(ctx, a)
}

func (p PGEvidence) RunDeliveries(ctx context.Context, q Querier, scope employeetask.Scope, taskID, runID string) ([]Delivery, error) {
	rows, err := q.Query(ctx, `SELECT a.id,a.state,a.provider_conversation_id,a.provider_message_id
FROM employee_run_notice n JOIN response_action a ON a.id=n.action_id AND a.workspace_id=n.workspace_id AND a.agent_id=n.agent_id
WHERE n.workspace_id=$1::uuid AND n.agent_id=$2::uuid AND n.tenant_org_id=$3 AND n.scene_id=$4::uuid AND n.task_id=$5::uuid AND n.run_id=$6::uuid AND n.state='enqueued'
ORDER BY a.created_at,a.id`, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, scope.Scene.SceneID, taskID, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Delivery
	for rows.Next() {
		var d Delivery
		if err := rows.Scan(&d.ActionID, &d.State, &d.ConversationID, &d.MessageID); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// DeliveredMessage is one provider-confirmed message that this Run's queue
// execution sent into the Task's origin conversation (sandbox_send_receipt).
type DeliveredMessage struct {
	ReceiptID      string
	ConversationID string
	MessageID      string
	// DWSUID is the sending identity frozen in the receipt; the reader must
	// still be that identity to read the message as the sender.
	DWSUID string
}

// DeliveredSet is the receipt set of one Run. Pending is true while any send
// into the origin conversation is still being confirmed, so verification
// waits instead of judging an incomplete delivery.
type DeliveredSet struct {
	Messages []DeliveredMessage
	Pending  bool
}

func (d DeliveredSet) digest() string {
	if len(d.Messages) == 0 {
		return ""
	}
	lines := make([]string, 0, len(d.Messages))
	for _, m := range d.Messages {
		lines = append(lines, m.ReceiptID+"|"+m.ConversationID+"|"+m.MessageID+"|"+m.DWSUID)
	}
	sort.Strings(lines)
	return sha256Hex([]byte(strings.Join(lines, "\n")))
}

// DeliveredFile is one own file resource of a delivered message, downloaded by
// the Host as the agent identity. Unreadable explains a file the Host could not
// read; such a file never passes or fails a check by itself.
type DeliveredFile struct {
	FileID     string
	Name       string
	Data       []byte
	Unreadable string
}

// DeliveredFileSource is implemented by evidence sources that can read files
// a Run delivered through the provider. RunDeliveredMessages runs under the
// caller's transaction; ReadDeliveredFiles performs provider I/O and is
// called with no lock held.
type DeliveredFileSource interface {
	RunDeliveredMessages(ctx context.Context, q Querier, scope employeetask.Scope, taskID, runID string) (DeliveredSet, error)
	ReadDeliveredFiles(ctx context.Context, scope employeetask.Scope, taskID, runID, queueTaskID string, m DeliveredMessage, maxBytes int64) ([]DeliveredFile, error)
}

// FileReader reads a delivered message's own file resources as the agent's
// bound DWS identity. The Host adapter wraps D1's dwsclient
// ReadMessageResources + DownloadMessageFile.
type FileReader interface {
	ReadDeliveredFiles(ctx context.Context, scope employeetask.Scope, taskID, runID, queueTaskID string, m DeliveredMessage, maxBytes int64) ([]DeliveredFile, error)
}

var errNoFileReader = errors.New("employee verification: delivered file reader unavailable")

// RunDeliveredMessages returns the provider-confirmed messages this Run's
// queue execution sent into the scene's origin conversation, using the same
// destination rule as the run-notice delivery decision: a receipt targeted at
// another conversation never qualifies, and an untargeted unconfirmed send
// keeps the set pending.
func (p PGEvidence) RunDeliveredMessages(ctx context.Context, q Querier, scope employeetask.Scope, taskID, runID string) (DeliveredSet, error) {
	rows, err := q.Query(ctx, `SELECT r.id,r.state,r.provider_conversation_id,r.provider_message_id,COALESCE(r.input->>'dws_uid',''),
  (r.state IN ('pending','provider_accepted') OR (r.state='unknown' AND r.next_attempt_at IS NOT NULL)) AS unsettled,
  s.external_scene_id
FROM employee_task_run run
JOIN agent_scene s ON s.id=$6::uuid AND s.workspace_id=run.workspace_id AND s.agent_id=run.agent_id
JOIN sandbox_send_receipt r ON r.workspace_id=run.workspace_id AND r.agent_id=run.agent_id AND r.task_id=run.queue_task_id
WHERE run.workspace_id=$1::uuid AND run.agent_id=$2::uuid AND run.tenant_org_id=$3 AND run.task_id=$4::uuid AND run.id=$5::uuid
  AND COALESCE(r.input->>'dws_org_id','')=$3
  AND (COALESCE(NULLIF(r.provider_conversation_id,''),r.target_conversation_id)=s.external_scene_id
    OR (r.provider_conversation_id='' AND r.target_conversation_id='' AND r.recipient_open_dingtalk_id='' AND r.state IN ('pending','provider_accepted','unknown')))
ORDER BY r.created_at,r.id`, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, taskID, runID, scope.Scene.SceneID)
	if err != nil {
		return DeliveredSet{}, err
	}
	defer rows.Close()
	var out DeliveredSet
	seen := map[string]bool{}
	for rows.Next() {
		var m DeliveredMessage
		var state, origin string
		var unsettled bool
		if err := rows.Scan(&m.ReceiptID, &state, &m.ConversationID, &m.MessageID, &m.DWSUID, &unsettled, &origin); err != nil {
			return DeliveredSet{}, err
		}
		out.Pending = out.Pending || unsettled
		if state == "delivered" && m.MessageID != "" && m.ConversationID == origin && !seen[m.MessageID] {
			seen[m.MessageID] = true
			out.Messages = append(out.Messages, m)
		}
	}
	return out, rows.Err()
}

func (p PGEvidence) ReadDeliveredFiles(ctx context.Context, scope employeetask.Scope, taskID, runID, queueTaskID string, m DeliveredMessage, maxBytes int64) ([]DeliveredFile, error) {
	if p.Files == nil {
		return nil, errNoFileReader
	}
	return p.Files.ReadDeliveredFiles(ctx, scope, taskID, runID, queueTaskID, m, maxBytes)
}
