package employeeverification

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/employeetask"
)

const (
	maxChecks        = 16
	maxContains      = 8
	maxColumns       = 32
	maxLiteralBytes  = 200
	maxColumnRunes   = 64
	maxDataRows      = 10_000_000
	maxCommandBytes  = 200
	deliveryOrigin   = "origin_reply"
	humanTaskSource  = "employee_scene"
	checkIDPrefix    = "chk_"
	checkIDHexLength = 24
)

var (
	sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	fieldPattern  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
	checkIDFormat = regexp.MustCompile(`^chk_[0-9a-f]{24}$`)
)

// humanOriginTask reports whether a Task was created from a human scene
// message. Every other source (routine, webhook, unknown future sources) is
// automation and can never select a human requester-private namespace.
func humanOriginTask(sourceNamespace string) bool { return sourceNamespace == humanTaskSource }

// NormalizeChecks validates Host-built checks, assigns stable IDs and returns
// them sorted with the spec digest. Unknown kinds are rejected here; a kind
// written by a newer binary is still loaded and then fails closed when checked.
func NormalizeChecks(in []Check, scope LearningScope) ([]Check, string, error) {
	if len(in) == 0 || len(in) > maxChecks {
		return nil, "", fmt.Errorf("%w: a spec needs 1-%d checks", ErrInvalid, maxChecks)
	}
	byID := map[string]Check{}
	for _, raw := range in {
		c, err := normalizeCheck(raw)
		if err != nil {
			return nil, "", err
		}
		if prior, ok := byID[c.ID]; ok && !prior.Optional {
			c.Optional = false
		}
		byID[c.ID] = c
	}
	out := make([]Check, 0, len(byID))
	for _, c := range byID {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	switch scope {
	case LearningScopeDefault, LearningScopeScene, LearningScopeNone:
	default:
		return nil, "", fmt.Errorf("%w: learning scope", ErrInvalid)
	}
	raw, err := json.Marshal(struct {
		Checks        []Check       `json:"checks"`
		LearningScope LearningScope `json:"learning_scope"`
	}{out, scope})
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(raw)
	return out, hex.EncodeToString(sum[:]), nil
}

func normalizeCheck(c Check) (Check, error) {
	bad := func(msg string) (Check, error) {
		return Check{}, fmt.Errorf("%w: %s check: %s", ErrInvalid, c.Kind, msg)
	}
	c.ID = ""
	c.Contains = append([]string(nil), c.Contains...)
	c.Columns = append([]string(nil), c.Columns...)
	if c.DataRows != nil {
		rows := *c.DataRows
		c.DataRows = &rows
	}
	c.File = strings.TrimSpace(c.File)
	c.Expect = strings.TrimSpace(c.Expect)
	c.Field = strings.TrimSpace(c.Field)
	c.Command = strings.TrimSpace(c.Command)
	c.Delivery = strings.TrimSpace(c.Delivery)
	c.SHA256 = strings.ToLower(strings.TrimSpace(c.SHA256))
	if c.File != "" {
		name, ok := safeFileName(c.File)
		if !ok {
			return bad("unsafe artifact file name")
		}
		c.File = name
	}
	if len(c.Contains) > maxContains {
		return bad("too many literals")
	}
	for i, literal := range c.Contains {
		if literal == "" || len(literal) > maxLiteralBytes || !utf8.ValidString(literal) || strings.ContainsAny(literal, "\x00\r\n") {
			return bad("invalid literal")
		}
		c.Contains[i] = literal
	}
	if len(c.Columns) > maxColumns {
		return bad("too many columns")
	}
	for i, column := range c.Columns {
		column = strings.TrimSpace(column)
		if column == "" || utf8.RuneCountInString(column) > maxColumnRunes || !utf8.ValidString(column) || strings.ContainsAny(column, "\x00\r\n") {
			return bad("invalid column")
		}
		c.Columns[i] = column
	}
	tabular := tabularFile(c.File)
	switch c.Kind {
	case KindArtifactContents:
		if c.File == "" || c.Expect != "" || c.Field != "" || c.Command != "" || c.Delivery != "" {
			return bad("needs exactly an artifact file and content conditions")
		}
		if (c.DataRows != nil || len(c.Columns) > 0) && !tabular {
			return bad("rows and columns need a .csv or .tsv artifact")
		}
		if c.DataRows != nil && (*c.DataRows < 0 || *c.DataRows > maxDataRows) {
			return bad("row count out of range")
		}
		if c.SHA256 != "" && !sha256Pattern.MatchString(c.SHA256) {
			return bad("sha256")
		}
	case KindExecutionOutput:
		if len(c.Contains) > 0 || c.DataRows != nil || len(c.Columns) > 0 || c.SHA256 != "" || c.Delivery != "" {
			return bad("unsupported condition")
		}
		switch {
		case c.Command != "":
			if c.File != "" || c.Expect != "" || c.Field != "" || len(c.Command) > maxCommandBytes || len(c.Command) < 2 || !utf8.ValidString(c.Command) || strings.ContainsAny(c.Command, "\x00\r\n") {
				return bad("invalid command")
			}
		case c.File != "" && c.Expect != "":
			// The expected value must be compared with produced output, never
			// with the assistant's final text.
			if len(c.Expect) > maxLiteralBytes || !utf8.ValidString(c.Expect) || strings.ContainsAny(c.Expect, "\x00\r\n") {
				return bad("invalid expected value")
			}
			if c.Field != "" && (!fieldPattern.MatchString(c.Field) || !strings.EqualFold(path.Ext(c.File), ".json")) {
				return bad("a field needs a .json artifact")
			}
		default:
			return bad("needs an artifact file with an expected value, or a command")
		}
	case KindDeliveryReceipt:
		if c.Delivery != deliveryOrigin || c.File != "" || len(c.Contains) > 0 || c.DataRows != nil || len(c.Columns) > 0 || c.SHA256 != "" || c.Expect != "" || c.Field != "" || c.Command != "" {
			return bad("only origin_reply delivery is supported")
		}
	default:
		return bad("unknown kind")
	}
	c.ID = checkID(c)
	return c, nil
}

func checkID(c Check) string {
	c.ID = ""
	c.Optional = false
	raw, _ := json.Marshal(c)
	sum := sha256.Sum256(raw)
	return checkIDPrefix + hex.EncodeToString(sum[:])[:checkIDHexLength]
}

// safeFileName keeps only the base name the Host stores for Run artifacts and
// rejects traversal, absolute and home-relative paths outright.
func safeFileName(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 512 || !utf8.ValidString(raw) || strings.ContainsAny(raw, "\x00\r\n\\") || strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "~") {
		return "", false
	}
	for _, part := range strings.Split(raw, "/") {
		if part == ".." || part == "." || part == "" {
			return "", false
		}
	}
	name := path.Base(raw)
	ext := path.Ext(name)
	if len(name) > 255 || ext == "" || ext == name || len(ext) < 2 {
		return "", false
	}
	return name, true
}

func tabularFile(name string) bool {
	ext := strings.ToLower(path.Ext(name))
	return ext == ".csv" || ext == ".tsv"
}

// taskRow is the Host-owned Task binding read under the caller's transaction.
type taskRow struct {
	ID, RequesterRef, State, SourceNamespace, SceneID, Goal string
	GoalRevision                                            int64
}

func validScope(scope employeetask.Scope) bool {
	return validUUID(scope.WorkspaceID) && validUUID(scope.AgentID) && scope.Kind == employeetask.ScopeScene && validUUID(scope.Scene.SceneID) &&
		scope.TenantOrgID != "" && len(scope.TenantOrgID) <= 128 && strings.TrimSpace(scope.TenantOrgID) == scope.TenantOrgID
}

func validUUID(s string) bool {
	_, err := uuid.Parse(s)
	return err == nil && len(s) == 36
}

// loadTask reads only Employee-owned scene Tasks in the exact scope; other
// owners or scopes are indistinguishable from a missing Task.
func loadTask(ctx context.Context, q Querier, scope employeetask.Scope, taskID string, lock bool) (taskRow, error) {
	if !validScope(scope) || !validUUID(taskID) {
		return taskRow{}, ErrInvalid
	}
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	var t taskRow
	var ownerLoop, scopeKind string
	err := q.QueryRow(ctx, `SELECT id::text,state,goal_revision,requester_ref,source_namespace,owner_loop,scope_kind,COALESCE(scene_id::text,''),COALESCE(definition->>'goal','') FROM employee_task WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND id=$4::uuid`+suffix,
		scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, taskID).Scan(&t.ID, &t.State, &t.GoalRevision, &t.RequesterRef, &t.SourceNamespace, &ownerLoop, &scopeKind, &t.SceneID, &t.Goal)
	if errors.Is(err, pgx.ErrNoRows) {
		return taskRow{}, ErrNotFound
	}
	if err != nil {
		return taskRow{}, err
	}
	if ownerLoop != string(employeetask.LoopEmployee) || scopeKind != string(employeetask.ScopeScene) || t.SceneID != scope.Scene.SceneID {
		return taskRow{}, ErrNotFound
	}
	return t, nil
}

func lockWorkspace(ctx context.Context, tx pgx.Tx, workspaceID string) error {
	var id string
	err := tx.QueryRow(ctx, `SELECT id::text FROM workspace WHERE id=$1::uuid FOR KEY SHARE`, workspaceID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

const specColumns = `task_id::text,revision,origin,state,source_ref,author_ref,learning_scope,checks,spec_digest,confirmed_by,updated_at`

func scanSpec(row pgx.Row) (Spec, error) {
	var s Spec
	var raw []byte
	if err := row.Scan(&s.TaskID, &s.Revision, &s.Origin, &s.State, &s.SourceRef, &s.AuthorRef, &s.LearningScope, &raw, &s.Digest, &s.ConfirmedBy, &s.UpdatedAt); err != nil {
		return Spec{}, err
	}
	if err := json.Unmarshal(raw, &s.Checks); err != nil {
		return Spec{}, err
	}
	return s, nil
}

// currentSpec returns the highest revision. Callers that write hold the Task
// row lock, which serializes every spec writer of that Task.
func currentSpec(ctx context.Context, q Querier, scope employeetask.Scope, taskID string) (Spec, error) {
	s, err := scanSpec(q.QueryRow(ctx, `SELECT `+specColumns+` FROM employee_task_verification_spec WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND task_id=$4::uuid ORDER BY revision DESC LIMIT 1`, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, taskID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Spec{}, ErrNoSpec
	}
	return s, err
}

func specBySource(ctx context.Context, q Querier, scope employeetask.Scope, taskID, sourceRef string) (Spec, error) {
	s, err := scanSpec(q.QueryRow(ctx, `SELECT `+specColumns+` FROM employee_task_verification_spec WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND task_id=$4::uuid AND source_ref=$5 ORDER BY revision DESC LIMIT 1`, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, taskID, sourceRef))
	if errors.Is(err, pgx.ErrNoRows) {
		return Spec{}, ErrNoSpec
	}
	return s, err
}

// Spec returns the Task's current verification contract or ErrNoSpec.
func (s *Store) Spec(ctx context.Context, scope employeetask.Scope, taskID string) (Spec, error) {
	if s == nil || s.db == nil {
		return Spec{}, ErrInvalid
	}
	if _, err := loadTask(ctx, s.db, scope, taskID, false); err != nil {
		return Spec{}, err
	}
	return currentSpec(ctx, s.db, scope, taskID)
}

// SetSpec stores a Host-built verification contract in its own transaction.
func (s *Store) SetSpec(ctx context.Context, scope employeetask.Scope, taskID string, p SetSpecParams) (Spec, error) {
	if s == nil || s.db == nil {
		return Spec{}, ErrInvalid
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Spec{}, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	spec, err := SetSpecTx(ctx, tx, scope, taskID, p)
	if err != nil {
		return Spec{}, err
	}
	return spec, tx.Commit(ctx)
}

// SetSpecTx joins the caller's transaction (for example the Task creation or a
// verified human correction). Lock order: workspace -> Task -> spec rows.
// A human_cue spec must come from the Task requester's own words; a model
// proposal is stored inactive and never replaces an active contract. The same
// source replays its original revision; changed content under that source
// conflicts.
func SetSpecTx(ctx context.Context, tx pgx.Tx, scope employeetask.Scope, taskID string, p SetSpecParams) (Spec, error) {
	if tx == nil || !validText(p.SourceRef, 512) || !validText(p.AuthorRef, 256) || p.ExpectedRevision < 0 {
		return Spec{}, ErrInvalid
	}
	state := SpecActive
	switch p.Origin {
	case OriginHumanCue, OriginHostFixture, OriginAutomation:
	case OriginModelProposed:
		state = SpecProposed
	default:
		return Spec{}, fmt.Errorf("%w: origin", ErrInvalid)
	}
	checks, digest, err := NormalizeChecks(p.Checks, p.LearningScope)
	if err != nil {
		return Spec{}, err
	}
	if err = lockWorkspace(ctx, tx, scope.WorkspaceID); err != nil {
		return Spec{}, err
	}
	task, err := loadTask(ctx, tx, scope, taskID, true)
	if err != nil {
		return Spec{}, err
	}
	human := humanOriginTask(task.SourceNamespace)
	if (p.Origin == OriginHumanCue || p.Origin == OriginModelProposed) && !human {
		return Spec{}, fmt.Errorf("%w: automation tasks take checks from configuration or fixtures", ErrInvalid)
	}
	if p.Origin == OriginAutomation && human {
		return Spec{}, fmt.Errorf("%w: automation configuration cannot govern a human task", ErrInvalid)
	}
	if human && p.LearningScope != LearningScopeDefault {
		return Spec{}, fmt.Errorf("%w: human tasks always learn requester-private", ErrInvalid)
	}
	if p.Origin == OriginHumanCue && p.AuthorRef != task.RequesterRef {
		return Spec{}, ErrNotRequester
	}
	if prior, err := specBySource(ctx, tx, scope, taskID, p.SourceRef); err == nil {
		if prior.Digest == digest && prior.Origin == p.Origin && prior.AuthorRef == p.AuthorRef {
			return prior, nil
		}
		return Spec{}, ErrConflict
	} else if !errors.Is(err, ErrNoSpec) {
		return Spec{}, err
	}
	revision := int64(1)
	current, err := currentSpec(ctx, tx, scope, taskID)
	switch {
	case errors.Is(err, ErrNoSpec):
		if p.ExpectedRevision != 0 {
			return Spec{}, ErrConflict
		}
	case err != nil:
		return Spec{}, err
	default:
		if p.ExpectedRevision != current.Revision {
			return Spec{}, ErrConflict
		}
		if state == SpecProposed && current.State == SpecActive {
			return Spec{}, fmt.Errorf("%w: a model proposal cannot replace an active contract", ErrConflict)
		}
		revision = current.Revision + 1
	}
	return insertSpec(ctx, tx, scope, taskID, Spec{Revision: revision, Origin: p.Origin, State: state, SourceRef: p.SourceRef, AuthorRef: p.AuthorRef, LearningScope: p.LearningScope, Checks: checks, Digest: digest})
}

func insertSpec(ctx context.Context, tx pgx.Tx, scope employeetask.Scope, taskID string, s Spec) (Spec, error) {
	raw, err := json.Marshal(s.Checks)
	if err != nil {
		return Spec{}, err
	}
	return scanSpec(tx.QueryRow(ctx, `INSERT INTO employee_task_verification_spec(workspace_id,agent_id,tenant_org_id,task_id,revision,origin,state,source_ref,author_ref,learning_scope,checks,spec_digest,confirmed_by)
VALUES($1::uuid,$2::uuid,$3,$4::uuid,$5,$6,$7,$8,$9,$10,$11,$12,$13) RETURNING `+specColumns,
		scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, taskID, s.Revision, s.Origin, s.State, s.SourceRef, s.AuthorRef, s.LearningScope, raw, s.Digest, s.ConfirmedBy))
}

// ConfirmSpec activates a proposed spec after the Task requester's explicit,
// Host-verified confirmation. It writes a new revision; the proposal remains
// in history.
func (s *Store) ConfirmSpec(ctx context.Context, scope employeetask.Scope, taskID string, p ConfirmParams) (Spec, error) {
	if s == nil || s.db == nil || !validText(p.ConfirmerRef, 256) || !validText(p.SourceRef, 512) || p.ExpectedRevision < 1 {
		return Spec{}, ErrInvalid
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Spec{}, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if err = lockWorkspace(ctx, tx, scope.WorkspaceID); err != nil {
		return Spec{}, err
	}
	task, err := loadTask(ctx, tx, scope, taskID, true)
	if err != nil {
		return Spec{}, err
	}
	if p.ConfirmerRef != task.RequesterRef {
		return Spec{}, ErrNotRequester
	}
	if prior, err := specBySource(ctx, tx, scope, taskID, p.SourceRef); err == nil {
		if prior.State == SpecActive && prior.ConfirmedBy == p.ConfirmerRef {
			return prior, tx.Commit(ctx)
		}
		return Spec{}, ErrConflict
	} else if !errors.Is(err, ErrNoSpec) {
		return Spec{}, err
	}
	current, err := currentSpec(ctx, tx, scope, taskID)
	if err != nil {
		return Spec{}, err
	}
	if current.Revision != p.ExpectedRevision || current.State != SpecProposed {
		return Spec{}, ErrConflict
	}
	current.Revision++
	current.State = SpecActive
	current.SourceRef = p.SourceRef
	current.ConfirmedBy = p.ConfirmerRef
	confirmed, err := insertSpec(ctx, tx, scope, taskID, current)
	if err != nil {
		return Spec{}, err
	}
	return confirmed, tx.Commit(ctx)
}

func validText(value string, limit int) bool {
	return strings.TrimSpace(value) != "" && strings.TrimSpace(value) == value && len(value) <= limit && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}

func sha256Hex(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func clip(text string, limit int) string {
	text = strings.TrimSpace(text)
	if len(text) <= limit {
		return text
	}
	cut := text[:limit]
	for !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut + "..."
}
