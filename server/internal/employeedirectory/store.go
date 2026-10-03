package employeedirectory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DB is a pool (or anything that can begin transactions).
type DB interface {
	Begin(context.Context) (pgx.Tx, error)
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// Store persists profile facts and rosters (employee_agent_profile_fact,
// employee_scene_member_roster). Leases are row values compared and set in
// single statements, so every replica sees the same lease; directory I/O
// happens between the claim and the completion, never under a row lock.
type Store struct{ db DB }

func NewStore(db DB) *Store { return &Store{db: db} }

// ErrLeaseLost: another replica took over the refresh (the lease expired);
// its result stands and this one is dropped.
var ErrLeaseLost = errors.New("employee directory refresh lease lost")

// ProfileTarget is one agent identity whose own facts are refreshed.
type ProfileTarget struct {
	WorkspaceID string
	AgentID     string
	TenantOrgID string
	DWSUID      string
}

func (t ProfileTarget) valid() bool {
	return validUUID(t.WorkspaceID) && validUUID(t.AgentID) && validOrg(t.TenantOrgID) && cleanID(t.DWSUID) != ""
}

// RosterTarget is one group scene whose roster is refreshed.
type RosterTarget struct {
	WorkspaceID    string
	AgentID        string
	SceneID        string
	TenantOrgID    string
	DWSUID         string
	ConversationID string
}

func (t RosterTarget) valid() bool {
	return validUUID(t.WorkspaceID) && validUUID(t.AgentID) && validUUID(t.SceneID) && validOrg(t.TenantOrgID) &&
		cleanID(t.DWSUID) != "" && strings.TrimSpace(t.ConversationID) != ""
}

func validUUID(s string) bool { _, err := uuid.Parse(strings.TrimSpace(s)); return err == nil }
func validOrg(s string) bool {
	s = strings.TrimSpace(s)
	return s != "" && len(s) <= 128 && !strings.ContainsAny(s, " \t\r\n:")
}

// ErrInvalidTarget: a target misses a trusted identifier.
var ErrInvalidTarget = errors.New("employee directory target is invalid")

// Schedule bounds how often a refresh may run.
type Schedule struct {
	// MinAge is the minimum time between successful refreshes.
	MinAge time.Duration
	// ErrorBackoff is the minimum time after a failed attempt.
	ErrorBackoff time.Duration
	// LeaseTTL bounds one refresh; a crashed holder's lease expires.
	LeaseTTL time.Duration
}

// DefaultSchedule refreshes at most daily, retries a failure after six
// hours, and lets a crashed refresh be taken over after two minutes.
var DefaultSchedule = Schedule{MinAge: 24 * time.Hour, ErrorBackoff: 6 * time.Hour, LeaseTTL: 2 * time.Minute}

func (s Schedule) orDefault() Schedule {
	if s.MinAge <= 0 {
		s.MinAge = DefaultSchedule.MinAge
	}
	if s.ErrorBackoff <= 0 {
		s.ErrorBackoff = DefaultSchedule.ErrorBackoff
	}
	if s.LeaseTTL <= 0 {
		s.LeaseTTL = DefaultSchedule.LeaseTTL
	}
	return s
}

func seconds(d time.Duration) float64 { return d.Seconds() }

// dueCondition is shared by claims and due scans. A refresh is due when the
// last failure (if any) is older than ErrorBackoff, and the identity changed,
// or the facts never succeeded or are older than MinAge. Parameters are SQL
// expressions: the current identity and the two bounds in seconds.
func dueCondition(alias, uidParam, minAgeParam, backoffParam string) string {
	p := alias + "."
	return `((` + p + `error_code = '' OR ` + p + `attempted_at IS NULL OR ` + p + `attempted_at <= now() - make_interval(secs => ` + backoffParam + `))
		AND (` + p + `dws_uid <> ` + uidParam + ` OR ` + p + `refreshed_at IS NULL OR ` + p + `refreshed_at <= now() - make_interval(secs => ` + minAgeParam + `)))`
}

// ClaimProfile takes the agent's refresh lease when a refresh is due. ok is
// false when the facts are fresh or another replica holds the lease.
func (s *Store) ClaimProfile(ctx context.Context, t ProfileTarget, sched Schedule) (lease string, ok bool, err error) {
	if !t.valid() {
		return "", false, ErrInvalidTarget
	}
	sched = sched.orDefault()
	if _, err = s.db.Exec(ctx, `INSERT INTO employee_agent_profile_fact (workspace_id, agent_id, tenant_org_id, fact_key, dws_uid)
		VALUES ($1::uuid, $2::uuid, $3, 'supervisor', $4)
		ON CONFLICT (workspace_id, agent_id, tenant_org_id, fact_key) DO NOTHING`, t.WorkspaceID, t.AgentID, t.TenantOrgID, t.DWSUID); err != nil {
		return "", false, err
	}
	token := uuid.NewString()
	err = s.db.QueryRow(ctx, `UPDATE employee_agent_profile_fact f
		SET lease_token = $5::uuid, lease_until = now() + make_interval(secs => $6), attempted_at = now(), updated_at = now()
		WHERE f.workspace_id = $1::uuid AND f.agent_id = $2::uuid AND f.tenant_org_id = $3 AND f.fact_key = 'supervisor'
		  AND (f.lease_until IS NULL OR f.lease_until <= now())
		  AND `+dueCondition("f", "$4", "$7", "$8")+`
		RETURNING f.lease_token::text`,
		t.WorkspaceID, t.AgentID, t.TenantOrgID, t.DWSUID, token, seconds(sched.LeaseTTL), seconds(sched.MinAge), seconds(sched.ErrorBackoff)).Scan(&lease)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return lease, true, nil
}

// holdsLease locks the lease row and reports whether lease still holds it.
func holdsLease(ctx context.Context, tx pgx.Tx, query string, lease string, args ...any) error {
	var current *string
	err := tx.QueryRow(ctx, query, args...).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrLeaseLost
	}
	if err != nil {
		return err
	}
	if current == nil || *current != lease {
		return ErrLeaseLost
	}
	return nil
}

const profileLeaseRow = `SELECT lease_token::text FROM employee_agent_profile_fact
	WHERE workspace_id = $1::uuid AND agent_id = $2::uuid AND tenant_org_id = $3 AND fact_key = 'supervisor' FOR UPDATE`

// CompleteProfile stores a successful read under the lease. A fact the read
// could not tell (ObservedUnavailable) keeps its stored status and value.
func (s *Store) CompleteProfile(ctx context.Context, t ProfileTarget, lease string, entry SelfEntry) error {
	if !t.valid() {
		return ErrInvalidTarget
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := holdsLease(ctx, tx, profileLeaseRow, lease, t.WorkspaceID, t.AgentID, t.TenantOrgID); err != nil {
		return err
	}
	observed := map[FactKey]Observed{FactSupervisor: entry.Supervisor, FactDepartment: entry.Department, FactTitle: entry.Title}
	for _, key := range FactKeys {
		o := observed[key]
		status, value, ref, code := "", "", "", ""
		switch o.State {
		case ObservedKnown:
			status, value, ref = string(StatusKnown), clipBytes(publicText(o.Value, maxDeptRunes), 256), clipBytes(o.Ref, 256)
			if value == "" && ref == "" {
				status = string(StatusUnregistered)
			}
		case ObservedUnregistered:
			status = string(StatusUnregistered)
		default:
			code = "unavailable"
		}
		if status == "" {
			// The read could not tell: keep the stored value of the same
			// identity, but never carry a value read as another identity.
			if _, err := tx.Exec(ctx, `INSERT INTO employee_agent_profile_fact (workspace_id, agent_id, tenant_org_id, fact_key, dws_uid, error_code)
				VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6)
				ON CONFLICT (workspace_id, agent_id, tenant_org_id, fact_key) DO UPDATE SET
					status = CASE WHEN employee_agent_profile_fact.dws_uid = EXCLUDED.dws_uid THEN employee_agent_profile_fact.status ELSE 'pending' END,
					value = CASE WHEN employee_agent_profile_fact.dws_uid = EXCLUDED.dws_uid THEN employee_agent_profile_fact.value ELSE '' END,
					value_ref = CASE WHEN employee_agent_profile_fact.dws_uid = EXCLUDED.dws_uid THEN employee_agent_profile_fact.value_ref ELSE '' END,
					refreshed_at = CASE WHEN employee_agent_profile_fact.dws_uid = EXCLUDED.dws_uid THEN employee_agent_profile_fact.refreshed_at ELSE NULL END,
					dws_uid = EXCLUDED.dws_uid, error_code = EXCLUDED.error_code, updated_at = now()`,
				t.WorkspaceID, t.AgentID, t.TenantOrgID, string(key), t.DWSUID, code); err != nil {
				return err
			}
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO employee_agent_profile_fact (workspace_id, agent_id, tenant_org_id, fact_key, status, value, value_ref, dws_uid, refreshed_at, attempted_at)
			VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7, $8, now(), now())
			ON CONFLICT (workspace_id, agent_id, tenant_org_id, fact_key) DO UPDATE SET
				status = EXCLUDED.status, value = EXCLUDED.value, value_ref = EXCLUDED.value_ref, dws_uid = EXCLUDED.dws_uid,
				refreshed_at = now(), attempted_at = now(), error_code = '', updated_at = now()`,
			t.WorkspaceID, t.AgentID, t.TenantOrgID, string(key), status, value, ref, t.DWSUID); err != nil {
			return err
		}
	}
	// The supervisor row carries the schedule: its refreshed_at and
	// error_code above decide when the next refresh is due.
	if _, err := tx.Exec(ctx, `UPDATE employee_agent_profile_fact SET lease_token = NULL, lease_until = NULL, updated_at = now()
		WHERE workspace_id = $1::uuid AND agent_id = $2::uuid AND tenant_org_id = $3 AND fact_key = 'supervisor'`,
		t.WorkspaceID, t.AgentID, t.TenantOrgID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// FailProfile records a failed refresh under the lease: every stored value
// stays as it was.
func (s *Store) FailProfile(ctx context.Context, t ProfileTarget, lease, code string) error {
	if !t.valid() {
		return ErrInvalidTarget
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := holdsLease(ctx, tx, profileLeaseRow, lease, t.WorkspaceID, t.AgentID, t.TenantOrgID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE employee_agent_profile_fact SET error_code = $4,
			lease_token = CASE WHEN fact_key = 'supervisor' THEN NULL ELSE lease_token END,
			lease_until = CASE WHEN fact_key = 'supervisor' THEN NULL ELSE lease_until END, updated_at = now()
		WHERE workspace_id = $1::uuid AND agent_id = $2::uuid AND tenant_org_id = $3`,
		t.WorkspaceID, t.AgentID, t.TenantOrgID, errorCode(code)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ReadProfile returns the stored facts of an agent in a tenant org. ok is
// false when nothing was ever stored.
func (s *Store) ReadProfile(ctx context.Context, workspaceID, agentID, tenantOrgID string) (Profile, bool, error) {
	p := Profile{WorkspaceID: workspaceID, AgentID: agentID, TenantOrgID: tenantOrgID}
	if !validUUID(workspaceID) || !validUUID(agentID) || !validOrg(tenantOrgID) {
		return p, false, ErrInvalidTarget
	}
	rows, err := s.db.Query(ctx, `SELECT fact_key, status, value, value_ref, dws_uid, refreshed_at, error_code
		FROM employee_agent_profile_fact WHERE workspace_id = $1::uuid AND agent_id = $2::uuid AND tenant_org_id = $3`,
		workspaceID, agentID, tenantOrgID)
	if err != nil {
		return p, false, err
	}
	defer rows.Close()
	found := false
	p.Supervisor, p.Department, p.Title = Fact{Key: FactSupervisor, Status: StatusPending}, Fact{Key: FactDepartment, Status: StatusPending}, Fact{Key: FactTitle, Status: StatusPending}
	for rows.Next() {
		var key, status, value, ref, uid, code string
		var refreshed *time.Time
		if err := rows.Scan(&key, &status, &value, &ref, &uid, &refreshed, &code); err != nil {
			return p, false, err
		}
		found = true
		f := Fact{Key: FactKey(key), Status: FactStatus(status), Value: value, Ref: ref, ErrorCode: code}
		if refreshed != nil {
			f.RefreshedAt = *refreshed
		}
		switch FactKey(key) {
		case FactSupervisor:
			p.Supervisor, p.DWSUID, p.ErrorCode = f, uid, code
			p.RefreshedAt = f.RefreshedAt
		case FactDepartment:
			p.Department = f
		case FactTitle:
			p.Title = f
		}
	}
	return p, found, rows.Err()
}

// DueProfileTargets lists employee-mode agents whose own facts are due, in
// their identity org. Archived agents and agents without a DingTalk
// identity are never listed.
func (s *Store) DueProfileTargets(ctx context.Context, sched Schedule, limit int) ([]ProfileTarget, error) {
	sched = sched.orDefault()
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.Query(ctx, `SELECT i.workspace_id::text, i.agent_id::text, i.org_id, i.dws_uid
		FROM agent_dingtalk_identity i
		JOIN agent a ON a.id = i.agent_id AND a.workspace_id = i.workspace_id
		LEFT JOIN employee_agent_profile_fact f ON f.workspace_id = i.workspace_id AND f.agent_id = i.agent_id
			AND f.tenant_org_id = i.org_id AND f.fact_key = 'supervisor'
		WHERE a.coordination_mode = 'employee' AND a.inbound_coordinator AND a.archived_at IS NULL
		  AND i.dws_uid <> '' AND i.org_id <> ''
		  AND (f.workspace_id IS NULL OR ((f.lease_until IS NULL OR f.lease_until <= now()) AND `+dueCondition("f", "i.dws_uid", "$1", "$2")+`))
		ORDER BY f.refreshed_at NULLS FIRST, i.agent_id
		LIMIT $3`, seconds(sched.MinAge), seconds(sched.ErrorBackoff), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProfileTarget
	for rows.Next() {
		var t ProfileTarget
		if err := rows.Scan(&t.WorkspaceID, &t.AgentID, &t.TenantOrgID, &t.DWSUID); err != nil {
			return nil, err
		}
		if t.valid() {
			out = append(out, t)
		}
	}
	return out, rows.Err()
}

// ClaimRoster takes a group scene's refresh lease when a refresh is due.
func (s *Store) ClaimRoster(ctx context.Context, t RosterTarget, sched Schedule) (lease string, ok bool, err error) {
	if !t.valid() {
		return "", false, ErrInvalidTarget
	}
	sched = sched.orDefault()
	if _, err = s.db.Exec(ctx, `INSERT INTO employee_scene_member_roster (workspace_id, agent_id, scene_id, tenant_org_id, dws_uid)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5)
		ON CONFLICT (workspace_id, agent_id, scene_id) DO NOTHING`, t.WorkspaceID, t.AgentID, t.SceneID, t.TenantOrgID, t.DWSUID); err != nil {
		return "", false, err
	}
	token := uuid.NewString()
	err = s.db.QueryRow(ctx, `UPDATE employee_scene_member_roster r
		SET lease_token = $5::uuid, lease_until = now() + make_interval(secs => $6), attempted_at = now(), updated_at = now()
		WHERE r.workspace_id = $1::uuid AND r.agent_id = $2::uuid AND r.scene_id = $3::uuid
		  AND (r.lease_until IS NULL OR r.lease_until <= now())
		  AND `+dueCondition("r", "$4", "$7", "$8")+`
		RETURNING r.lease_token::text`,
		t.WorkspaceID, t.AgentID, t.SceneID, t.DWSUID, token, seconds(sched.LeaseTTL), seconds(sched.MinAge), seconds(sched.ErrorBackoff)).Scan(&lease)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return lease, true, nil
}

const rosterLeaseRow = `SELECT lease_token::text FROM employee_scene_member_roster
	WHERE workspace_id = $1::uuid AND agent_id = $2::uuid AND scene_id = $3::uuid FOR UPDATE`

// CompleteRoster replaces the roster under the lease.
func (s *Store) CompleteRoster(ctx context.Context, t RosterTarget, lease string, members []Member, total int) error {
	if !t.valid() {
		return ErrInvalidTarget
	}
	if members == nil {
		members = []Member{}
	}
	if total < len(members) {
		total = len(members)
	}
	raw, err := json.Marshal(members)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := holdsLease(ctx, tx, rosterLeaseRow, lease, t.WorkspaceID, t.AgentID, t.SceneID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE employee_scene_member_roster SET tenant_org_id = $4, dws_uid = $5, members = $6::jsonb, member_total = $7,
			refreshed_at = now(), error_code = '', lease_token = NULL, lease_until = NULL, updated_at = now()
		WHERE workspace_id = $1::uuid AND agent_id = $2::uuid AND scene_id = $3::uuid`,
		t.WorkspaceID, t.AgentID, t.SceneID, t.TenantOrgID, t.DWSUID, string(raw), total); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// FailRoster records a failed refresh under the lease; the stored members
// stay as they were.
func (s *Store) FailRoster(ctx context.Context, t RosterTarget, lease, code string) error {
	if !t.valid() {
		return ErrInvalidTarget
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := holdsLease(ctx, tx, rosterLeaseRow, lease, t.WorkspaceID, t.AgentID, t.SceneID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE employee_scene_member_roster SET error_code = $4, lease_token = NULL, lease_until = NULL, updated_at = now()
		WHERE workspace_id = $1::uuid AND agent_id = $2::uuid AND scene_id = $3::uuid`,
		t.WorkspaceID, t.AgentID, t.SceneID, errorCode(code)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ReadRoster returns a scene's stored roster. ok is false when the scene
// has none (never refreshed, or no row).
func (s *Store) ReadRoster(ctx context.Context, workspaceID, agentID, sceneID string) (Roster, bool, error) {
	r := Roster{WorkspaceID: workspaceID, AgentID: agentID, SceneID: sceneID}
	if !validUUID(workspaceID) || !validUUID(agentID) || !validUUID(sceneID) {
		return r, false, ErrInvalidTarget
	}
	var raw []byte
	var refreshed *time.Time
	err := s.db.QueryRow(ctx, `SELECT tenant_org_id, dws_uid, members, member_total, refreshed_at, error_code
		FROM employee_scene_member_roster WHERE workspace_id = $1::uuid AND agent_id = $2::uuid AND scene_id = $3::uuid`,
		workspaceID, agentID, sceneID).Scan(&r.TenantOrgID, &r.DWSUID, &raw, &r.Total, &refreshed, &r.ErrorCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, false, nil
	}
	if err != nil {
		return r, false, err
	}
	if refreshed == nil {
		return r, false, nil
	}
	r.RefreshedAt = *refreshed
	if err := json.Unmarshal(raw, &r.Members); err != nil {
		return r, false, fmt.Errorf("decode roster members: %w", err)
	}
	return r, true, nil
}

// DueRosterTargets lists group scenes of employee-mode agents that were
// active within activeWithin and whose roster is due. Only scenes in the
// agent's identity org are listed: that is the identity whose address book
// classifies members. Scenes of other tenant orgs refresh on wake, as the
// wake's own identity.
func (s *Store) DueRosterTargets(ctx context.Context, sched Schedule, activeWithin time.Duration, limit int) ([]RosterTarget, error) {
	sched = sched.orDefault()
	if limit <= 0 || limit > 100 {
		limit = 10
	}
	if activeWithin <= 0 {
		activeWithin = 72 * time.Hour
	}
	rows, err := s.db.Query(ctx, `SELECT s.workspace_id::text, s.agent_id::text, s.id::text, s.tenant_org_id, i.dws_uid, s.external_scene_id
		FROM agent_scene s
		JOIN agent a ON a.id = s.agent_id AND a.workspace_id = s.workspace_id
		JOIN agent_dingtalk_identity i ON i.agent_id = s.agent_id AND i.workspace_id = s.workspace_id AND i.org_id = s.tenant_org_id
		LEFT JOIN employee_scene_member_roster r ON r.workspace_id = s.workspace_id AND r.agent_id = s.agent_id AND r.scene_id = s.id
		WHERE s.scene_kind = 'group' AND s.provider = 'dingtalk' AND s.last_active_at > now() - make_interval(secs => $1)
		  AND a.coordination_mode = 'employee' AND a.inbound_coordinator AND a.archived_at IS NULL AND i.dws_uid <> ''
		  AND (r.workspace_id IS NULL OR ((r.lease_until IS NULL OR r.lease_until <= now()) AND `+dueCondition("r", "i.dws_uid", "$2", "$3")+`))
		ORDER BY r.refreshed_at NULLS FIRST, s.last_active_at DESC
		LIMIT $4`, seconds(activeWithin), seconds(sched.MinAge), seconds(sched.ErrorBackoff), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RosterTarget
	for rows.Next() {
		var t RosterTarget
		if err := rows.Scan(&t.WorkspaceID, &t.AgentID, &t.SceneID, &t.TenantOrgID, &t.DWSUID, &t.ConversationID); err != nil {
			return nil, err
		}
		if t.valid() {
			out = append(out, t)
		}
	}
	return out, rows.Err()
}

// keptStaff returns the staffIds kept for openDingTalkIDs as viewerUID sees
// them in orgID (dws_open_identity_staff, see contextcap.LookupOpenIDStaff),
// proved within maxAge.
func (s *Store) keptStaff(ctx context.Context, orgID, viewerUID string, openIDs []string, maxAge time.Duration) (map[string]string, error) {
	out := map[string]string{}
	if len(openIDs) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, `SELECT open_dingtalk_id, staff_id FROM dws_open_identity_staff
		WHERE org_id = $1 AND viewer_uid = $2 AND open_dingtalk_id = ANY($3::text[]) AND resolved_at > now() - make_interval(secs => $4)`,
		orgID, viewerUID, openIDs, seconds(maxAge))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var openID, staff string
		if err := rows.Scan(&openID, &staff); err != nil {
			return nil, err
		}
		if staff = cleanID(staff); staff != "" {
			out[openID] = staff
		}
	}
	return out, rows.Err()
}

func clipBytes(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut]
}

var errorCodes = map[string]bool{"timeout": true, "forbidden": true, "auth": true, "identity_mismatch": true,
	"provider_error": true, "unavailable": true, "not_group": true, "canceled": true}

func errorCode(code string) string {
	if errorCodes[code] {
		return code
	}
	return "provider_error"
}
