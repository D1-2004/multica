package employeeentry

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/scene"
)

// HistoryPolicy says whose recent scene dialogue a wake of the Task may read.
type HistoryPolicy string

const (
	// HistoryScenePrincipal reads the origin scene for the Task's accepted
	// admission principal, as the original message turn did.
	HistoryScenePrincipal HistoryPolicy = "scene_principal"
	// HistorySceneEndpointPrincipal reads the origin scene for the scene's
	// current dispatch endpoint principal, which the reader has validated.
	HistorySceneEndpointPrincipal HistoryPolicy = "scene_endpoint_principal"
	// HistoryNotApplicable: the Task has no conversation (an enterprise scene
	// or a webhook without one). The wake states this; it is not "unavailable".
	HistoryNotApplicable HistoryPolicy = "not_applicable"
)

func (p HistoryPolicy) valid() bool {
	switch p {
	case HistoryScenePrincipal, HistorySceneEndpointPrincipal, HistoryNotApplicable:
		return true
	default:
		return false
	}
}

// PrincipalMember is a workspace member user admitted by an authenticated
// endpoint. Other principal kinds (an Agent creator, an endpoint installation)
// are added by their readers; an Agent UUID is never a member principal.
const PrincipalMember = "member"

// DeliveryAnchor is where a wake's reply may go, derived from PostgreSQL. It
// names the employee identity and requester the origin used; the Host still
// re-reads the scene directory and current identity before any send.
type DeliveryAnchor struct {
	// Conversation is false for a scene without a conversation: nothing can
	// be sent there and the wake gets no reply tool.
	Conversation         bool   `json:"conversation"`
	SceneID              string `json:"scene_id"`
	RequesterRef         string `json:"requester_ref,omitempty"`
	SenderOpenDingTalkID string `json:"sender_open_dingtalk_id,omitempty"`
	DWSUID               string `json:"dws_uid,omitempty"`
	DWSEnvironment       string `json:"dws_environment,omitempty"`
	ShowAITag            bool   `json:"show_ai_tag,omitempty"`
	// PersonKey selects the person capability layer; empty means none.
	PersonKey string `json:"person_key,omitempty"`
}

// TaskOrigin is the Host-verified origin of an Employee Task. A reader builds
// it from PostgreSQL only; a wake payload or a producer cannot supply it.
type TaskOrigin struct {
	Task    employeetask.Task
	Request employeetask.Entry
	// PrincipalID is the accepted authority of the Task's admission. It is the
	// wake job's principal and must still hold its current permission.
	PrincipalID   string
	PrincipalKind string
	// ReceiptID and JobID identify the origin admission, when there is one.
	ReceiptID string
	JobID     string
	// SourceRef, RequestSpeaker and RequestText describe the original request
	// as data for the model.
	SourceRef          string
	RequestSpeaker     string
	RequestText        string
	History            HistoryPolicy
	HistoryPrincipalID string
	Anchor             DeliveryAnchor
}

// TaskOriginHold is a durable refusal from a reader: the origin authority was
// revoked or its records no longer agree. Storage failures are plain errors.
type TaskOriginHold struct{ Reason string }

func (h *TaskOriginHold) Error() string { return "employee task origin held: " + h.Reason }
func (h *TaskOriginHold) Unwrap() error { return ErrTaskWakeRevoked }

// TaskOriginReader resolves Tasks created under one source namespace. It runs
// inside the caller's transaction (db), performs no network I/O, verifies the
// origin principal's current permission and returns *TaskOriginHold for a
// durable refusal.
type TaskOriginReader interface {
	ReadTaskOrigin(ctx context.Context, db DB, scope Scope, task employeetask.Task, request employeetask.Entry) (TaskOrigin, error)
}

// TaskOriginRegistry maps Task creation namespaces to their readers. A Task
// whose namespace has no reader cannot be woken; nothing is guessed.
type TaskOriginRegistry struct {
	mu      sync.RWMutex
	readers map[string]TaskOriginReader
}

func NewTaskOriginRegistry() *TaskOriginRegistry {
	return &TaskOriginRegistry{readers: map[string]TaskOriginReader{}}
}

// Register installs reader for namespace once; a second reader is an error.
func (r *TaskOriginRegistry) Register(namespace string, reader TaskOriginReader) error {
	if r == nil || reader == nil || namespace == "" || strings.TrimSpace(namespace) != namespace {
		return ErrInvalid
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.readers[namespace]; exists {
		return ErrConflict
	}
	r.readers[namespace] = reader
	return nil
}

func taskScope(scope Scope) employeetask.Scope {
	return employeetask.Scope{WorkspaceID: scope.WorkspaceID, AgentID: scope.AgentID, TenantOrgID: scope.TenantOrgID, Kind: employeetask.ScopeScene, Scene: scene.Ref{SceneID: scope.SceneID}}
}

// Read loads the Employee Task in scope and its request entry through the
// employeetask read APIs, then dispatches on the request's source namespace.
// A scope mismatch is ErrNotFound; an unknown namespace is ErrTaskWakeOrigin.
func (r *TaskOriginRegistry) Read(ctx context.Context, database DB, scope Scope, taskID string) (TaskOrigin, error) {
	if r == nil || !validScope(scope) || !validID(taskID) {
		return TaskOrigin{}, ErrInvalid
	}
	store := employeetask.NewStore(database)
	task, err := store.Get(ctx, taskScope(scope), taskID)
	switch {
	case errors.Is(err, employeetask.ErrNotFound):
		return TaskOrigin{}, ErrNotFound
	case errors.Is(err, employeetask.ErrInvalid):
		return TaskOrigin{}, ErrInvalid
	case err != nil:
		return TaskOrigin{}, err
	}
	if task.OwnerLoop != employeetask.LoopEmployee {
		return TaskOrigin{}, ErrNotFound
	}
	entries, err := store.ReadEntries(ctx, taskScope(scope), taskID, 0, 1)
	if err != nil {
		return TaskOrigin{}, err
	}
	if len(entries) != 1 || entries[0].Seq != 1 || entries[0].Kind != "request" {
		return TaskOrigin{}, ErrTaskWakeOrigin
	}
	r.mu.RLock()
	reader := r.readers[entries[0].Source.Namespace]
	r.mu.RUnlock()
	if reader == nil {
		return TaskOrigin{}, ErrTaskWakeOrigin
	}
	origin, err := reader.ReadTaskOrigin(ctx, database, scope, task, entries[0])
	if err != nil {
		return TaskOrigin{}, err
	}
	origin.Task, origin.Request = task, entries[0]
	if !validID(origin.PrincipalID) || origin.PrincipalKind == "" || !origin.History.valid() || origin.Anchor.SceneID != scope.SceneID ||
		(origin.History == HistoryNotApplicable) != (origin.HistoryPrincipalID == "") || (origin.HistoryPrincipalID != "" && !validID(origin.HistoryPrincipalID)) ||
		origin.Anchor.Conversation != (origin.History != HistoryNotApplicable) || (origin.Anchor.Conversation && origin.Anchor.DWSUID == "") {
		// A reader returned an inconsistent origin; never deliver with it.
		return TaskOrigin{}, &TaskOriginHold{Reason: "task_origin_inconsistent"}
	}
	return origin, nil
}

// TaskOriginNamespace is the Task creation source written by the scene Host's
// dispatch_task: "<receipt>/<native call>/definition".
const TaskOriginNamespace = "employee_scene"

// TaskOriginReceipt parses the receipt of a scene dispatch Task source key.
func TaskOriginReceipt(source employeetask.Source) (string, bool) {
	if source.Namespace != TaskOriginNamespace {
		return "", false
	}
	parts := strings.Split(source.Key, "/")
	if len(parts) != 3 || !validID(parts[0]) || strings.TrimSpace(parts[1]) == "" || parts[2] != "definition" {
		return "", false
	}
	return parts[0], true
}

// SceneMessageAdmission is the frozen Employee work consumption of a scene
// message receipt: its job and authenticated admission principal.
type SceneMessageAdmission struct {
	ReceiptID   string
	JobID       string
	PrincipalID string
}

// ReadSceneMessageAdmission resolves a scene dispatch Task's request entry to
// the consumption that created it. Readers add their own payload checks.
func ReadSceneMessageAdmission(ctx context.Context, database DB, scope Scope, request employeetask.Entry) (SceneMessageAdmission, error) {
	receipt, ok := TaskOriginReceipt(request.Source)
	if !ok {
		return SceneMessageAdmission{}, ErrTaskWakeOrigin
	}
	out := SceneMessageAdmission{ReceiptID: receipt}
	var owner string
	err := database.QueryRow(ctx, `SELECT owner_loop,COALESCE(job_id::text,''),principal_id::text FROM employee_event_consumption WHERE `+scopeWhere+` AND receipt_id=$5::uuid`, append(scopeArgs(scope), receipt)...).Scan(&owner, &out.JobID, &out.PrincipalID)
	if errors.Is(err, pgx.ErrNoRows) {
		return SceneMessageAdmission{}, ErrTaskWakeOrigin
	}
	if err != nil {
		return SceneMessageAdmission{}, err
	}
	if owner != Employee || !validID(out.JobID) || !validID(out.PrincipalID) {
		return SceneMessageAdmission{}, ErrTaskWakeOrigin
	}
	return out, nil
}
