package employeeverification

import (
	"context"
	"errors"

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
type PGEvidence struct{ Reader ArtifactReader }

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
