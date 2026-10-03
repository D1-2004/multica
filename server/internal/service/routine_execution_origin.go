package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service/dingtalkresponse"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// AutomationOriginContextKey is the queue-context locator of an EmployeeTask
// execution admitted from a Host-verified automation source rather than from a
// message: a scene routine occurrence today, a webhook delivery later. The
// locator grants nothing by itself. Every reader re-verifies it against the
// PostgreSQL receipt with LoadAutomationOrigin before acting on it.
const AutomationOriginContextKey = "employee_automation_origin"

// AutomationDeliveryOwnerSceneRoutine is the employee_delivery_owner of a
// routine-origin execution. The routine's own start and end notices are its
// only sender; the message Run notice consumer only takes owner "employee".
const AutomationDeliveryOwnerSceneRoutine = "scene_routine"

// AutomationOriginKind names one automation source protocol. Each kind has its
// own receipt table and loader; an unknown kind is never accepted.
type AutomationOriginKind string

const AutomationOriginSceneRoutine AutomationOriginKind = "scene_routine"

// Event sources of a scene routine occurrence; they are also the
// source_namespace of the EmployeeTask the occurrence creates.
const (
	routineSourceSchedule = "scene.routine.schedule"
	routineSourceManual   = "scene.routine.manual"
	// routineSourceWebhook is an accepted webhook delivery of a routine
	// (employee_webhook_task.go).
	routineSourceWebhook = "scene.routine.webhook"
)

// AutomationPrincipalKind distinguishes the authority an automation runs
// under. An Agent principal is never written into a member/user field.
type AutomationPrincipalKind string

const (
	AutomationPrincipalMember AutomationPrincipalKind = "member"
	AutomationPrincipalAgent  AutomationPrincipalKind = "agent"
)

// AutomationPrincipal is the frozen admission authority of an automation
// source: the member or the same-workspace Agent that configured it, or the
// member who ran it manually.
type AutomationPrincipal struct {
	Kind AutomationPrincipalKind `json:"kind"`
	ID   string                  `json:"id"`
}

// AutomationOriginRef is the locator persisted in queue context under
// AutomationOriginContextKey. It is data, not a credential.
type AutomationOriginRef struct {
	Kind           AutomationOriginKind `json:"kind"`
	ReceiptID      string               `json:"receipt_id"`
	AutopilotRunID string               `json:"autopilot_run_id"`
}

// ErrAutomationOriginInvalid means the locator does not match a committed,
// accepted receipt for this exact queue row. Readers fail closed on it.
var ErrAutomationOriginInvalid = errors.New("automation origin does not match a verified receipt")

// AutomationOrigin is a Host-verified automation source of one EmployeeTask
// execution. Values exist only after a loader in this package read and
// cross-checked the receipt, AutopilotRun, EmployeeTask, Run and queue rows in
// PostgreSQL: HTTP payloads, queue context and model output cannot construct
// one. Automation has no human originator; RequesterRef is never a member.
type AutomationOrigin interface {
	Kind() AutomationOriginKind
	// ReceiptID is the frozen source receipt row.
	ReceiptID() string
	// Source is the event identity: namespace plus a stable event id.
	Source() employeetask.Source
	// OccurredAt is DB time at the receipt's first commit; replays keep it.
	OccurredAt() time.Time
	RequesterRef() string
	Principal() AutomationPrincipal
	Scope() employeetask.Scope
	AutopilotRunID() string
	EmployeeTaskID() string
	RunID() string
	QueueTaskID() string
	// DeliveryOwner is the single sender of this execution's notices.
	DeliveryOwner() string
	// PromptSHA256 fingerprints the frozen compiled prompt.
	PromptSHA256() string
	// validatedFromPostgres seals the interface to loaders in this package.
	validatedFromPostgres()
}

// AutomationOriginQuerier is satisfied by a pool or an open transaction.
type AutomationOriginQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type automationOriginLoader func(ctx context.Context, q AutomationOriginQuerier, ref AutomationOriginRef, queue db.AgentTaskQueue) (AutomationOrigin, error)

// automationOriginLoaders registers one PostgreSQL loader per kind. Another
// automation source adds its own kind and loader from an init function in its
// own file instead of reusing a routine receipt.
var automationOriginLoaders = map[AutomationOriginKind]automationOriginLoader{}

func init() {
	automationOriginLoaders[AutomationOriginSceneRoutine] = loadSceneRoutineOrigin
}

// validAutomationOriginRef checks the locator's shape against its queue row:
// a known kind, canonical ids, and the queue's real autopilot_run_id.
func validAutomationOriginRef(ref *AutomationOriginRef, queue db.AgentTaskQueue) bool {
	if ref == nil || !queue.AutopilotRunID.Valid {
		return false
	}
	if _, known := automationOriginLoaders[ref.Kind]; !known {
		return false
	}
	receipt, err := util.ParseUUID(ref.ReceiptID)
	if err != nil || util.UUIDToString(receipt) != ref.ReceiptID {
		return false
	}
	return ref.AutopilotRunID == util.UUIDToString(queue.AutopilotRunID)
}

// LoadAutomationOrigin verifies the automation locator of a Direct queue row
// against PostgreSQL. A row without a locator, a malformed or unknown locator,
// or one that does not match its committed receipt is ErrAutomationOriginInvalid;
// other errors are transient database failures.
func LoadAutomationOrigin(ctx context.Context, q AutomationOriginQuerier, queue db.AgentTaskQueue) (AutomationOrigin, error) {
	direct, ok := ParseDirectTaskContext(queue)
	if !ok || direct.AutomationOrigin == nil || q == nil {
		return nil, ErrAutomationOriginInvalid
	}
	ref := *direct.AutomationOrigin
	origin, err := automationOriginLoaders[ref.Kind](ctx, q, ref, queue)
	if err != nil {
		return nil, err
	}
	if origin.EmployeeTaskID() != direct.EmployeeTaskID || origin.Scope().WorkspaceID != direct.WorkspaceID ||
		origin.QueueTaskID() != util.UUIDToString(queue.ID) || origin.PromptSHA256() != promptSHA256(direct.Prompt) {
		return nil, ErrAutomationOriginInvalid
	}
	return origin, nil
}

func promptSHA256(prompt string) string {
	sum := sha256.Sum256([]byte(prompt))
	return hex.EncodeToString(sum[:])
}

// RoutineOccurrenceFacts are the routine-specific frozen facts of a scene
// routine origin.
type RoutineOccurrenceFacts struct {
	RoutineID     string
	AutopilotID   string
	TriggerID     string
	PlannedAt     *time.Time
	Timezone      string
	ManualActorID string
}

type sceneRoutineOrigin struct {
	receiptID, requesterRef, autopilotRunID string
	taskID, runID, queueTaskID, promptSHA   string
	source                                  employeetask.Source
	occurredAt                              time.Time
	principal                               AutomationPrincipal
	scope                                   employeetask.Scope
	facts                                   RoutineOccurrenceFacts
}

func (o *sceneRoutineOrigin) Kind() AutomationOriginKind           { return AutomationOriginSceneRoutine }
func (o *sceneRoutineOrigin) ReceiptID() string                    { return o.receiptID }
func (o *sceneRoutineOrigin) Source() employeetask.Source          { return o.source }
func (o *sceneRoutineOrigin) OccurredAt() time.Time                { return o.occurredAt }
func (o *sceneRoutineOrigin) RequesterRef() string                 { return o.requesterRef }
func (o *sceneRoutineOrigin) Principal() AutomationPrincipal       { return o.principal }
func (o *sceneRoutineOrigin) Scope() employeetask.Scope            { return o.scope }
func (o *sceneRoutineOrigin) AutopilotRunID() string               { return o.autopilotRunID }
func (o *sceneRoutineOrigin) EmployeeTaskID() string               { return o.taskID }
func (o *sceneRoutineOrigin) RunID() string                        { return o.runID }
func (o *sceneRoutineOrigin) QueueTaskID() string                  { return o.queueTaskID }
func (o *sceneRoutineOrigin) DeliveryOwner() string                { return AutomationDeliveryOwnerSceneRoutine }
func (o *sceneRoutineOrigin) PromptSHA256() string                 { return o.promptSHA }
func (o *sceneRoutineOrigin) validatedFromPostgres()               {}
func (o *sceneRoutineOrigin) routineFacts() RoutineOccurrenceFacts { return o.facts }
func (o *sceneRoutineOrigin) routineIdentity() string              { return o.facts.RoutineID }

// routineBoundOrigin is an automation origin that belongs to a scene routine.
type routineBoundOrigin interface{ routineIdentity() string }

// SceneRoutineOccurrenceOf returns the routine facts of a scene routine origin.
func SceneRoutineOccurrenceOf(origin AutomationOrigin) (RoutineOccurrenceFacts, bool) {
	o, ok := origin.(*sceneRoutineOrigin)
	if !ok || o == nil {
		return RoutineOccurrenceFacts{}, false
	}
	return o.routineFacts(), true
}

// routineReceiptColumns reads a routine receipt with the EmployeeTask it
// created from the same source. A decision receipt takes its Run and queue
// from its dispatched decision, if any.
const routineReceiptColumns = `o.id::text,o.workspace_id::text,o.agent_id::text,o.tenant_org_id,o.scene_id::text,
 o.routine_id::text,o.autopilot_id::text,COALESCE(o.trigger_id::text,''),o.source,o.source_event_id,o.planned_at,o.timezone,o.occurred_at,
 o.creator_kind,o.creator_id::text,COALESCE(o.manual_actor_id::text,''),o.requester_ref,
 o.employee_task_id::text,COALESCE(o.employee_run_id,d.employee_run_id)::text,COALESCE(o.queue_task_id,d.queue_task_id)::text,o.autopilot_run_id::text,o.prompt_sha256,o.state
 FROM employee_routine_occurrence o
 LEFT JOIN employee_routine_decision d ON d.occurrence_id=o.id AND d.state='dispatched'
 JOIN employee_task t ON t.id=o.employee_task_id AND t.workspace_id=o.workspace_id AND t.agent_id=o.agent_id
  AND t.tenant_org_id=o.tenant_org_id AND t.scope_kind='scene' AND t.scene_id=o.scene_id AND t.owner_loop='employee'
  AND t.dispatch_mode='direct' AND t.requester_ref=o.requester_ref AND t.source_namespace=o.source
  AND t.source_key=o.source_event_id||'/definition'`

func scanRoutineReceipt(row pgx.Row) (*sceneRoutineOrigin, string, error) {
	o := &sceneRoutineOrigin{}
	var plannedAt pgtype.Timestamptz
	var creatorKind, state string
	var runID, queueID pgtype.Text
	o.scope.Kind = employeetask.ScopeScene
	err := row.Scan(&o.receiptID, &o.scope.WorkspaceID, &o.scope.AgentID, &o.scope.TenantOrgID, &o.scope.Scene.SceneID,
		&o.facts.RoutineID, &o.facts.AutopilotID, &o.facts.TriggerID, &o.source.Namespace, &o.source.Key, &plannedAt, &o.facts.Timezone, &o.occurredAt,
		&creatorKind, &o.principal.ID, &o.facts.ManualActorID, &o.requesterRef,
		&o.taskID, &runID, &queueID, &o.autopilotRunID, &o.promptSHA, &state)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", ErrAutomationOriginInvalid
	}
	if err != nil {
		return nil, "", fmt.Errorf("load scene routine origin: %w", err)
	}
	o.runID, o.queueTaskID = runID.String, queueID.String
	if plannedAt.Valid {
		at := plannedAt.Time.UTC()
		o.facts.PlannedAt = &at
	}
	o.principal.Kind = AutomationPrincipalKind(creatorKind)
	if o.facts.ManualActorID != "" {
		// A manual run is admitted under the member who ran it.
		o.principal = AutomationPrincipal{Kind: AutomationPrincipalMember, ID: o.facts.ManualActorID}
	}
	if o.requesterRef != routineRequesterRef(o.facts.RoutineID) || o.principal.ID == "" ||
		(o.principal.Kind != AutomationPrincipalMember && o.principal.Kind != AutomationPrincipalAgent) {
		return nil, "", ErrAutomationOriginInvalid
	}
	return o, state, nil
}

// loadSceneRoutineOrigin cross-checks one routine receipt with the
// AutopilotRun it mapped, the EmployeeTask it created from the same source,
// that Task's Run and the exact queue row.
func loadSceneRoutineOrigin(ctx context.Context, q AutomationOriginQuerier, ref AutomationOriginRef, queue db.AgentTaskQueue) (AutomationOrigin, error) {
	if !validAutomationOriginRef(&ref, queue) {
		return nil, ErrAutomationOriginInvalid
	}
	o, _, err := scanRoutineReceipt(q.QueryRow(ctx, `SELECT `+routineReceiptColumns+`
 WHERE o.id=$1::uuid AND o.state IN ('accepted','decision') AND COALESCE(o.queue_task_id,d.queue_task_id)=$2 AND o.autopilot_run_id=$3::uuid AND o.agent_id=$4
 AND EXISTS(SELECT 1 FROM autopilot_run ar WHERE ar.id=o.autopilot_run_id AND ar.autopilot_id=o.autopilot_id AND ar.task_id=$2)
 AND EXISTS(SELECT 1 FROM employee_task_run r WHERE r.id=COALESCE(o.employee_run_id,d.employee_run_id) AND r.task_id=t.id AND r.workspace_id=t.workspace_id AND r.agent_id=t.agent_id AND r.queue_task_id=$2)`,
		ref.ReceiptID, queue.ID, ref.AutopilotRunID, queue.AgentID))
	if err != nil {
		return nil, err
	}
	return o, nil
}

// routineRequesterRef identifies an automation requester. It is never a
// member id, so requester-private memory and steer/stop never match a human.
func routineRequesterRef(routineID string) string { return "routine:" + routineID }

// IsAutomationRequesterRef reports whether a Task requester is an automation
// source rather than a human requester.
func IsAutomationRequesterRef(ref string) bool {
	return len(ref) > len("routine:") && ref[:len("routine:")] == "routine:"
}

// automationOriginRefJSON renders the queue-context locator.
func automationOriginRefJSON(ref AutomationOriginRef) json.RawMessage {
	raw, _ := json.Marshal(ref)
	return raw
}

// AutomationTaskSourceNamespaces are the employee_task.source_namespace
// values of Tasks created by an automation origin. A Task-origin registry
// keyed by Task.Source.Namespace dispatches these to LoadAutomationTaskOrigin.
var AutomationTaskSourceNamespaces = []string{routineSourceSchedule, routineSourceManual, routineSourceWebhook}

// automationTaskReceiptTables are the receipt tables of the automation kinds
// whose Tasks a later wake may load. They share the identity columns used by
// LoadAutomationTaskOrigin; the table names are constants, never input. The
// routine occurrence table comes first.
var automationTaskReceiptTables = []string{"employee_routine_occurrence", "employee_webhook_occurrence"}

// AutomationHistoryPolicyKind selects how a later wake of an automation Task
// may read conversation history.
type AutomationHistoryPolicyKind string

const (
	// AutomationHistorySceneEndpoint: read the bound group/DM scene's history
	// with that scene's current dispatch endpoint principal, which the Host
	// resolves and validates at use time. Never use the routine creator: it is
	// not a party of the conversation and its read silently returns nothing.
	AutomationHistorySceneEndpoint AutomationHistoryPolicyKind = "scene_endpoint_principal"
	// AutomationHistoryNotApplicable: the scene has no conversation history
	// (enterprise scene, or a resource event without a conversation).
	AutomationHistoryNotApplicable AutomationHistoryPolicyKind = "not_applicable"
)

// AutomationHistoryPolicy is data for the Host; it grants no read by itself.
type AutomationHistoryPolicy struct {
	Kind      AutomationHistoryPolicyKind `json:"kind"`
	SceneID   string                      `json:"scene_id,omitempty"`
	SceneKind string                      `json:"scene_kind,omitempty"`
}

// AutomationDeliveryAnchor is where results of the automation Task are
// delivered and who sends them. For a scene routine the routine's own notice
// is the single sender into its scene; the conversation is read back from the
// scene directory at send time, never stored here.
type AutomationDeliveryAnchor struct {
	Owner          string `json:"owner"`
	SceneID        string `json:"scene_id"`
	SceneKind      string `json:"scene_kind"`
	RoutineID      string `json:"routine_id,omitempty"`
	AutopilotRunID string `json:"autopilot_run_id"`
}

// AutomationTaskOrigin is the PostgreSQL-verified origin of an automation
// Task: its scope, delivery anchor, creator principal (member or Agent, kept
// apart from member fields) and history policy.
type AutomationTaskOrigin struct {
	Origin         AutomationOrigin
	Scope          employeetask.Scope
	DeliveryAnchor AutomationDeliveryAnchor
	Creator        AutomationPrincipal
	HistoryPolicy  AutomationHistoryPolicy
}

// LoadAutomationTaskOrigin reads the validated origin of the EmployeeTask
// taskID in scope from PostgreSQL only: the accepted receipt that created the
// Task, its AutopilotRun mapping, Run and queue row, and the current scene
// directory kind. A Task that no automation receipt created, or whose rows do
// not agree, is ErrAutomationOriginInvalid.
func LoadAutomationTaskOrigin(ctx context.Context, q AutomationOriginQuerier, scope employeetask.Scope, taskID string) (AutomationTaskOrigin, error) {
	var out AutomationTaskOrigin
	if q == nil || scope.Kind != employeetask.ScopeScene {
		return out, ErrAutomationOriginInvalid
	}
	// A schedule or run-now Task has a routine receipt (accepted, or decision
	// before its Loop dispatched a Run); a webhook Task has a webhook receipt.
	var queueID, receiptID, runID, table string
	var origin AutomationOrigin
	receipt, state, err := scanRoutineReceipt(q.QueryRow(ctx, `SELECT `+routineReceiptColumns+`
 WHERE o.employee_task_id=$1::uuid AND o.workspace_id=$2::uuid AND o.agent_id=$3::uuid AND o.tenant_org_id=$4 AND o.scene_id=$5::uuid AND o.state IN ('accepted','decision')`,
		taskID, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, scope.Scene.SceneID))
	switch {
	case err == nil:
		table, origin = "employee_routine_occurrence", receipt
		queueID, receiptID, runID = receipt.queueTaskID, receipt.receiptID, receipt.autopilotRunID
		if queueID == "" && state != routineOccurrenceDecision {
			return out, ErrAutomationOriginInvalid
		}
	case errors.Is(err, ErrAutomationOriginInvalid):
		for _, candidate := range automationTaskReceiptTables[1:] {
			err := q.QueryRow(ctx, `SELECT o.queue_task_id::text,o.id::text,o.autopilot_run_id::text FROM `+candidate+` o
 JOIN employee_task t ON t.id=o.employee_task_id AND t.source_namespace=o.source AND t.source_key=o.source_event_id||'/definition'
 WHERE o.employee_task_id=$1::uuid AND o.workspace_id=$2::uuid AND o.agent_id=$3::uuid AND o.tenant_org_id=$4 AND o.scene_id=$5::uuid AND o.state='accepted'`,
				taskID, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, scope.Scene.SceneID).Scan(&queueID, &receiptID, &runID)
			if err == nil {
				table = candidate
				break
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return out, fmt.Errorf("load automation task origin: %w", err)
			}
		}
		if table == "" {
			return out, ErrAutomationOriginInvalid
		}
	default:
		return out, err
	}
	if queueID != "" {
		// A Run exists: the queue row must agree with the receipt as well.
		var queue db.AgentTaskQueue
		if err := q.QueryRow(ctx, `SELECT id,agent_id,autopilot_run_id,context FROM agent_task_queue WHERE id=$1::uuid`, queueID).Scan(&queue.ID, &queue.AgentID, &queue.AutopilotRunID, &queue.Context); errors.Is(err, pgx.ErrNoRows) {
			return out, ErrAutomationOriginInvalid
		} else if err != nil {
			return out, fmt.Errorf("load automation task queue: %w", err)
		}
		if origin, err = LoadAutomationOrigin(ctx, q, queue); err != nil {
			return out, err
		}
	}
	if origin.ReceiptID() != receiptID || origin.EmployeeTaskID() != taskID || origin.Scope() != scope {
		return out, ErrAutomationOriginInvalid
	}
	routineID := ""
	if bound, ok := origin.(routineBoundOrigin); ok {
		routineID = bound.routineIdentity()
	}
	var sceneKind, creatorKind, creatorID string
	err = q.QueryRow(ctx, `SELECT s.scene_kind,o.creator_kind,o.creator_id::text FROM `+table+` o
 JOIN agent_scene s ON s.id=o.scene_id AND s.workspace_id=o.workspace_id AND s.agent_id=o.agent_id AND s.tenant_org_id=o.tenant_org_id
 WHERE o.id=$1::uuid`, receiptID).Scan(&sceneKind, &creatorKind, &creatorID)
	if errors.Is(err, pgx.ErrNoRows) {
		// The scene left the directory: the Task keeps its identity but has
		// no deliverable scene and no readable history.
		sceneKind = ""
		err = q.QueryRow(ctx, `SELECT creator_kind,creator_id::text FROM `+table+` WHERE id=$1::uuid`, receiptID).Scan(&creatorKind, &creatorID)
	}
	if err != nil {
		return out, fmt.Errorf("load automation task scene: %w", err)
	}
	out.Origin, out.Scope = origin, origin.Scope()
	out.Creator = AutomationPrincipal{Kind: AutomationPrincipalKind(creatorKind), ID: creatorID}
	out.DeliveryAnchor = AutomationDeliveryAnchor{Owner: origin.DeliveryOwner(), SceneID: scope.Scene.SceneID, SceneKind: sceneKind, RoutineID: routineID, AutopilotRunID: runID}
	out.HistoryPolicy = AutomationHistoryPolicy{Kind: AutomationHistoryNotApplicable}
	switch sceneKind {
	case scene.KindGroup, scene.KindDM:
		out.HistoryPolicy = AutomationHistoryPolicy{Kind: AutomationHistorySceneEndpoint, SceneID: scope.Scene.SceneID, SceneKind: sceneKind}
	}
	return out, nil
}

// RoutineOccurrenceOutcome is one deterministic, model-free outcome record of
// a routine occurrence, for a later decision wake (B3).
type RoutineOccurrenceOutcome struct {
	OccurrenceID string     `json:"occurrence_id"`
	Source       string     `json:"source"`
	EventID      string     `json:"event_id"`
	PlannedAt    *time.Time `json:"planned_at,omitempty"`
	OccurredAt   time.Time  `json:"occurred_at"`
	// State is accepted, skipped, skipped_overlap or failed (admission).
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
	// RunStatus is the AutopilotRun status; ExecutionState the Run state.
	RunStatus string `json:"run_status"`
	// Decision is the employee_decide outcome (pending, quiet, waited,
	// replied, dispatched, failed); empty for run_only occurrences.
	Decision       string     `json:"decision,omitempty"`
	DecisionReason string     `json:"decision_reason,omitempty"`
	ExecutionState string     `json:"execution_state,omitempty"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
	// NoticeState is the end notice's outbox state; Sent means the provider
	// accepted or delivered it. NoticeSHA256 hashes the rendered body.
	NoticeState  string `json:"notice_state,omitempty"`
	Sent         bool   `json:"sent"`
	NoticeSHA256 string `json:"notice_sha256,omitempty"`
	// ResultSHA256 hashes the Run's recorded result text.
	ResultSHA256 string `json:"result_sha256,omitempty"`
}

// ListRoutineOccurrenceOutcomes returns the newest limit (1..50) occurrences
// of a routine with their admission, execution and end-notice outcome, read
// from PostgreSQL only. It never summarizes text.
func ListRoutineOccurrenceOutcomes(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, workspaceID, routineID string, limit int) ([]RoutineOccurrenceOutcome, error) {
	if q == nil || limit < 1 || limit > 50 {
		return nil, ErrAutomationOriginInvalid
	}
	// The sent result is the routine's end notice, or the decision's single
	// scene reply (request scene-notice:<job>).
	rows, err := q.Query(ctx, `SELECT o.id::text,o.source,o.source_event_id,o.planned_at,o.occurred_at,o.state,o.reason,ar.status,
 COALESCE(d.state,''),COALESCE(d.reason,''),COALESCE(r.state,''),r.finished_at,COALESCE(r.result,''),COALESCE(n.state,rn.state,''),COALESCE(n.input->>'text',rn.input->>'text','')
 FROM employee_routine_occurrence o
 JOIN autopilot_run ar ON ar.id=o.autopilot_run_id
 LEFT JOIN employee_routine_decision d ON d.occurrence_id=o.id
 LEFT JOIN employee_task_run r ON r.id=COALESCE(o.employee_run_id,d.employee_run_id)
 LEFT JOIN response_action n ON n.workspace_id=o.workspace_id AND n.agent_id=o.agent_id AND n.request_id=$3||o.autopilot_run_id::text||$4 AND n.kind='message.send'
 LEFT JOIN response_action rn ON d.state='replied' AND rn.workspace_id=o.workspace_id AND rn.agent_id=o.agent_id AND rn.request_id='scene-notice:'||d.job_id::text AND rn.kind='message.send'
 WHERE o.workspace_id=$1::uuid AND o.routine_id=$2::uuid
 ORDER BY o.created_at DESC,o.id DESC LIMIT $5`, workspaceID, routineID, "routine:", ":"+dingtalkresponse.RoutineNoticeEnd, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RoutineOccurrenceOutcome
	for rows.Next() {
		var o RoutineOccurrenceOutcome
		var planned, finished pgtype.Timestamptz
		var result, notice string
		if err := rows.Scan(&o.OccurrenceID, &o.Source, &o.EventID, &planned, &o.OccurredAt, &o.State, &o.Reason, &o.RunStatus,
			&o.Decision, &o.DecisionReason, &o.ExecutionState, &finished, &result, &o.NoticeState, &notice); err != nil {
			return nil, err
		}
		if planned.Valid {
			at := planned.Time.UTC()
			o.PlannedAt = &at
		}
		if finished.Valid {
			at := finished.Time.UTC()
			o.FinishedAt = &at
		}
		if result != "" {
			o.ResultSHA256 = promptSHA256(result)
		}
		if notice != "" {
			o.NoticeSHA256 = promptSHA256(notice)
		}
		o.Sent = o.NoticeState == "provider_accepted" || o.NoticeState == "delivered"
		out = append(out, o)
	}
	return out, rows.Err()
}
