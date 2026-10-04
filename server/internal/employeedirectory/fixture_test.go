package employeedirectory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/multica-ai/multica/server/internal/dwsclient"
)

// testSchema installs this package's migrations (and the tables its due
// scans join) into an isolated schema of the worktree's own database and
// returns two independent pools on it, standing in for two replicas.
func testSchema(t *testing.T) (*pgxpool.Pool, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("set DATABASE_URL to an isolated PostgreSQL database")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "employee_directory_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); admin.Close() })
	open := func() *pgxpool.Pool {
		cfg, err := pgxpool.ParseConfig(dsn)
		if err != nil {
			t.Fatal(err)
		}
		cfg.ConnConfig.RuntimeParams["search_path"] = schema
		cfg.MaxConns = 4
		pool, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(pool.Close)
		return pool
	}
	a, b := open(), open()
	for _, stmt := range []string{
		`CREATE TABLE agent (id uuid NOT NULL, workspace_id uuid NOT NULL, coordination_mode text NOT NULL DEFAULT 'coordinator',
			inbound_coordinator boolean NOT NULL DEFAULT false, archived_at timestamptz)`,
		`CREATE TABLE agent_dingtalk_identity (agent_id uuid NOT NULL, workspace_id uuid NOT NULL, dws_uid text NOT NULL, org_id text NOT NULL)`,
	} {
		if _, err := a.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"9500_agent_scene", "9787_dws_open_identity_staff", "9873_employee_agent_profile_fact",
		"9874_employee_agent_profile_fact_key_idx", "9890_employee_scene_member_roster", "9891_employee_scene_member_roster_scene_idx"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "migrations", name+".up.sql"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.Exec(ctx, string(raw)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	return a, b
}

// fakeDirectory is a scripted Directory. Calls are counted per method.
type fakeDirectory struct {
	mu       sync.Mutex
	self     func() (SelfEntry, error)
	members  []GroupMember
	listErr  error
	staff    map[string]string // openDingTalkId → staffId; missing = not in address book
	proveErr map[string]error
	people   map[string]Person
	usersErr error
	calls    map[string]int
	// block, when set, is closed by the test to let Self return.
	block chan struct{}
	// entered is signalled when Self starts.
	entered chan struct{}
}

func (f *fakeDirectory) count(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[name]++
}

func (f *fakeDirectory) n(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[name]
}

func (f *fakeDirectory) Self(ctx context.Context, id dwsclient.Identity) (SelfEntry, error) {
	f.count("self")
	if f.entered != nil {
		f.entered <- struct{}{}
	}
	if f.block != nil {
		<-f.block
	}
	if f.self == nil {
		return SelfEntry{}, errors.New("no self")
	}
	return f.self()
}

func (f *fakeDirectory) GroupMembers(ctx context.Context, id dwsclient.Identity, conversationID string) ([]GroupMember, error) {
	f.count("members")
	return f.members, f.listErr
}

func (f *fakeDirectory) ProveStaff(ctx context.Context, id dwsclient.Identity, openDingTalkID string, names []string) (string, error) {
	f.count("prove")
	if err := f.proveErr[openDingTalkID]; err != nil {
		return "", err
	}
	return f.staff[openDingTalkID], nil
}

func (f *fakeDirectory) Users(ctx context.Context, id dwsclient.Identity, staffIDs []string) ([]Person, error) {
	f.count("users")
	if f.usersErr != nil {
		return nil, f.usersErr
	}
	var out []Person
	for _, s := range staffIDs {
		if p, ok := f.people[s]; ok {
			out = append(out, p)
		}
	}
	return out, nil
}

func newProfileTarget() ProfileTarget {
	return ProfileTarget{WorkspaceID: uuid.NewString(), AgentID: uuid.NewString(), TenantOrgID: "44675729", DWSUID: "507523443"}
}

func newRosterTarget() RosterTarget {
	return RosterTarget{WorkspaceID: uuid.NewString(), AgentID: uuid.NewString(), SceneID: uuid.NewString(), TenantOrgID: "44675729",
		DWSUID: "507523443", ConversationID: "cid-group-1"}
}

// expire makes a target due again, as if a day had passed.
func expireProfile(t *testing.T, pool *pgxpool.Pool, target ProfileTarget) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `UPDATE employee_agent_profile_fact SET refreshed_at = refreshed_at - interval '25 hours',
		attempted_at = attempted_at - interval '25 hours' WHERE workspace_id = $1::uuid AND agent_id = $2::uuid`, target.WorkspaceID, target.AgentID); err != nil {
		t.Fatal(err)
	}
}

func expireRoster(t *testing.T, pool *pgxpool.Pool, target RosterTarget) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `UPDATE employee_scene_member_roster SET refreshed_at = refreshed_at - interval '25 hours',
		attempted_at = attempted_at - interval '25 hours' WHERE workspace_id = $1::uuid AND scene_id = $2::uuid`, target.WorkspaceID, target.SceneID); err != nil {
		t.Fatal(err)
	}
}
