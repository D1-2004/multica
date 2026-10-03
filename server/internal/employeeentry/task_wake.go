package employeeentry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/eventrouter"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/util"
	dbgen "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	TaskWakeSchemaVersion = 1

	TaskWakeCollectionReady   = "collection.ready"
	TaskWakeExecutionFollowUp = "execution.follow_up"
	TaskWakeRoutineDecision   = "routine.decision"
	TaskWakeWebhookDecision   = "webhook.decision"

	// TaskWakeSourcePrefix reserves the receipt source namespace of derived
	// wakes, so no provider event can share a wake's source identity.
	TaskWakeSourcePrefix  = "employee.task_wake/"
	TaskWakePayloadSchema = "employee.task_wake.v1"
)

var (
	// ErrTaskWakeNotReady: some live server replica cannot execute task wakes.
	// Producers keep their own durable intent and retry later.
	ErrTaskWakeNotReady = errors.New("employee task wake: not every live server replica executes task wakes")
	ErrTaskWakeStopped  = errors.New("employee task wake: the task was stopped")
	ErrTaskWakeStale    = errors.New("employee task wake: goal revision or input boundary is not current")
	// ErrTaskWakeOrigin: the Task has no Host-verified origin that a wake can
	// address. A new origin kind needs its own reader here, never a guess.
	ErrTaskWakeOrigin = errors.New("employee task wake: the task has no wake-capable origin")
	// ErrTaskWakeRevoked: the origin authority was revoked or its records no
	// longer agree; *TaskOriginHold carries the reason.
	ErrTaskWakeRevoked = errors.New("employee task wake: the task origin authority is not current")
)

// TaskWakeKinds lists the wake kinds this binary executes.
func TaskWakeKinds() []string {
	return []string{TaskWakeCollectionReady, TaskWakeExecutionFollowUp, TaskWakeRoutineDecision, TaskWakeWebhookDecision}
}

// KnownTaskWakeKind reports whether this binary executes kind.
func KnownTaskWakeKind(kind string) bool {
	for _, known := range TaskWakeKinds() {
		if kind == known {
			return true
		}
	}
	return false
}

// TaskWake is the single item of a task_wake job. Every field is a reference:
// the Host rebuilds scope, owner, principal and delivery target from
// PostgreSQL and never trusts a target or permission named here.
type TaskWake struct {
	SchemaVersion int    `json:"schema_version"`
	Kind          string `json:"kind"`
	TaskID        string `json:"task_id"`
	GoalRevision  int64  `json:"goal_revision"`
	InputSeq      int64  `json:"input_seq"`
	AuthorityRef  string `json:"authority_ref"`
	EvidenceRef   string `json:"evidence_ref"`
}

func wakeRef(s string) bool {
	return s != "" && len(s) <= 512 && strings.TrimSpace(s) == s && !strings.ContainsAny(s, "\x00\r\n")
}

// Validate checks the reference shape only; currency is checked against PG.
func (w TaskWake) Validate() error {
	if w.SchemaVersion != TaskWakeSchemaVersion || !KnownTaskWakeKind(w.Kind) || !validID(w.TaskID) || w.GoalRevision < 1 || w.InputSeq < 1 || !wakeRef(w.AuthorityRef) || !wakeRef(w.EvidenceRef) {
		return ErrInvalid
	}
	return nil
}

// DecodeTaskWake strictly decodes a persisted wake item. It does not check the
// kind against this binary, so callers can hold an unknown kind explicitly.
func DecodeTaskWake(item Item) (TaskWake, error) {
	decoder := json.NewDecoder(bytes.NewReader(item.Payload))
	decoder.DisallowUnknownFields()
	var wake TaskWake
	if err := decoder.Decode(&wake); err != nil || decoder.More() {
		return TaskWake{}, ErrInvalid
	}
	if wake.SchemaVersion != TaskWakeSchemaVersion || !validID(wake.TaskID) || wake.GoalRevision < 1 || wake.InputSeq < 1 || !wakeRef(wake.AuthorityRef) || !wakeRef(wake.EvidenceRef) || item.MessageCount != 0 {
		return TaskWake{}, ErrInvalid
	}
	return wake, nil
}

var taskWakeSourcePattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)

// TaskWakeAdmission is constructed by a trusted producer from PostgreSQL facts.
// Source plus EventID is the derived source identity: a retry reuses both and
// never derives them from now(). OccurredAt is frozen by the first commit.
type TaskWakeAdmission struct {
	Scope      Scope
	Source     string
	EventID    string
	OccurredAt time.Time
	Wake       TaskWake
}

func (a TaskWakeAdmission) validate() error {
	if !validScope(a.Scope) || !taskWakeSourcePattern.MatchString(a.Source) || !wakeRef(a.EventID) || len(a.EventID) > 256 || a.OccurredAt.IsZero() {
		return ErrInvalid
	}
	return a.Wake.Validate()
}

// TaskWakeHost is the Host wiring a producer receives; producers never build it.
type TaskWakeHost interface {
	// TaskWakeProducerReady is true only while every live server replica
	// advertises the protocol marker that claims and executes task wakes.
	TaskWakeProducerReady(context.Context) (bool, error)
	// FenceTaskWakeScene applies the Host's current tenant fence inside the
	// admission transaction. It returns ErrNotFound for a scene not served now.
	FenceTaskWakeScene(context.Context, pgx.Tx, Scope) error
	// TaskOrigin resolves the Task through the Host's TaskOriginRegistry in
	// the admission transaction.
	TaskOrigin(ctx context.Context, db DB, scope Scope, taskID string) (TaskOrigin, error)
}

func taskWakeFingerprint(a TaskWakeAdmission, source string) (string, error) {
	raw, err := json.Marshal(struct {
		Source  string   `json:"source"`
		EventID string   `json:"event_id"`
		Scope   Scope    `json:"scope"`
		Wake    TaskWake `json:"wake"`
	}{source, a.EventID, a.Scope, a.Wake})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// AdmitTaskWake commits one receipt, its work consumption and a single-item
// task_wake job together. A wake never joins a human window, never creates a
// synthetic message and is refused until every live replica executes wakes.
// The receipt identity is Source+EventID: a replay with the same fingerprint
// returns the original job and keeps the first OccurredAt; a different
// payload under the same identity is ErrConflict.
func (s *Store) AdmitTaskWake(ctx context.Context, host TaskWakeHost, a TaskWakeAdmission) (Consumption, error) {
	if err := a.validate(); err != nil {
		return Consumption{}, err
	}
	if host == nil {
		return Consumption{}, ErrTaskWakeNotReady
	}
	if ready, err := host.TaskWakeProducerReady(ctx); err != nil || !ready {
		return Consumption{}, errors.Join(ErrTaskWakeNotReady, err)
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Consumption{}, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	// Lock order matches message admission: workspace, scene, then Task.
	if err = lockScope(ctx, tx, a.Scope); err != nil {
		return Consumption{}, err
	}
	if err = host.FenceTaskWakeScene(ctx, tx, a.Scope); err != nil {
		return Consumption{}, err
	}
	var locked string
	if err = tx.QueryRow(ctx, `SELECT id::text FROM employee_task WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND scene_id=$4::uuid AND id=$5::uuid FOR SHARE`, append(scopeArgs(a.Scope), a.Wake.TaskID)...).Scan(&locked); err != nil {
		return Consumption{}, mapError(err)
	}
	origin, err := host.TaskOrigin(ctx, tx, a.Scope, a.Wake.TaskID)
	if err != nil {
		return Consumption{}, err
	}
	source := TaskWakeSourcePrefix + a.Source
	var replay bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM scene_event_receipt WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND source=$3 AND source_event_id=$4)`, a.Scope.WorkspaceID, a.Scope.AgentID, source, a.EventID).Scan(&replay); err != nil {
		return Consumption{}, err
	}
	if !replay {
		// A replay returns its original job even after the Task moved on; the
		// worker applies the same fences again before any model request.
		switch {
		case origin.Task.State == employeetask.StateCancelled:
			return Consumption{}, ErrTaskWakeStopped
		case origin.Task.GoalRevision != a.Wake.GoalRevision || a.Wake.InputSeq > origin.Task.LastEntrySeq:
			return Consumption{}, ErrTaskWakeStale
		}
	}
	payload, err := json.Marshal(a.Wake)
	if err != nil {
		return Consumption{}, err
	}
	fingerprint, err := taskWakeFingerprint(a, source)
	if err != nil {
		return Consumption{}, err
	}
	var locator scene.Locator
	if err = tx.QueryRow(ctx, `SELECT provider,tenant_org_id,source_namespace,scene_kind,external_scene_id FROM agent_scene WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND id=$4::uuid`, scopeArgs(a.Scope)...).Scan(&locator.Provider, &locator.TenantOrgID, &locator.Namespace, &locator.Kind, &locator.ExternalID); err != nil {
		return Consumption{}, mapError(err)
	}
	owner := scene.Owner{WorkspaceID: util.MustParseUUID(a.Scope.WorkspaceID), AgentID: util.MustParseUUID(a.Scope.AgentID)}
	event := eventrouter.Event{Version: eventrouter.Version, ID: a.EventID, Source: source, Type: a.Wake.Kind, Category: eventrouter.Wake, OccurredAt: a.OccurredAt.UTC(), PayloadSchema: TaskWakePayloadSchema, Payload: payload}
	routerHost := eventrouter.Host{Owner: owner, PrincipalID: util.MustParseUUID(origin.PrincipalID), TenantOrgID: a.Scope.TenantOrgID, Locator: locator, Observation: scene.Observation{ActiveAt: a.OccurredAt}, Route: eventrouter.Unified, ConfigVersion: TaskWakePayloadSchema, Fingerprint: fingerprint}
	var c Consumption
	receipt, replayed, err := eventrouter.AdmitWithHook(ctx, tx, event, routerHost, func(ctx context.Context, receiptTx pgx.Tx, r dbgen.SceneEventReceipt) error {
		if util.UUIDToString(r.SceneID) != a.Scope.SceneID || r.Reason != "" {
			return ErrConflict
		}
		item := Item{ReceiptID: util.UUIDToString(r.ID), PrincipalID: origin.PrincipalID, Payload: payload}
		items, err := json.Marshal([]Item{item})
		if err != nil {
			return err
		}
		c = Consumption{ReceiptID: item.ReceiptID, Owner: Employee, State: "queued"}
		if err = receiptTx.QueryRow(ctx, `INSERT INTO employee_scene_job(workspace_id,agent_id,tenant_org_id,scene_id,principal_id,items,message_count,kind) VALUES($1::uuid,$2::uuid,$3,$4::uuid,$5::uuid,$6::jsonb,0,'task_wake') RETURNING id::text`, append(scopeArgs(a.Scope), origin.PrincipalID, items)...).Scan(&c.JobID); err != nil {
			return err
		}
		_, err = receiptTx.Exec(ctx, `INSERT INTO employee_event_consumption(workspace_id,agent_id,tenant_org_id,scene_id,receipt_id,owner_loop,config_revision,principal_id,payload,job_id,state) VALUES($1::uuid,$2::uuid,$3,$4::uuid,$5::uuid,'employee',$6,$7::uuid,$8::jsonb,$9::uuid,'queued')`, append(scopeArgs(a.Scope), item.ReceiptID, TaskWakePayloadSchema, origin.PrincipalID, payload, c.JobID)...)
		return err
	})
	switch {
	case errors.Is(err, eventrouter.ErrConflict):
		return Consumption{}, ErrConflict
	case errors.Is(err, scene.ErrStaleTenant):
		return Consumption{}, ErrNotFound
	case errors.Is(err, eventrouter.ErrInvalidEvent):
		return Consumption{}, ErrInvalid
	case err != nil:
		return Consumption{}, err
	}
	if replayed {
		var kind string
		err = tx.QueryRow(ctx, `SELECT c.receipt_id::text,c.owner_loop,COALESCE(c.job_id::text,''),c.state,c.reason,COALESCE(j.kind,'') FROM employee_event_consumption c LEFT JOIN employee_scene_job j ON j.id=c.job_id WHERE c.workspace_id=$1::uuid AND c.agent_id=$2::uuid AND c.tenant_org_id=$3 AND c.scene_id=$4::uuid AND c.receipt_id=$5::uuid`, append(scopeArgs(a.Scope), util.UUIDToString(receipt.ID))...).Scan(&c.ReceiptID, &c.Owner, &c.JobID, &c.State, &c.Reason, &kind)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && kind != KindTaskWake) {
			return Consumption{}, ErrConflict
		}
		if err != nil {
			return Consumption{}, err
		}
	}
	return c, tx.Commit(ctx)
}
