package dshprofile

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/dshhost"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func profilePools(t *testing.T) (*pgxpool.Pool, *pgxpool.Pool) {
	t.Helper()
	url := os.Getenv("DSH_PROFILE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("requires an explicitly configured real preproduction test database")
	}
	ctx := context.Background()
	connectCtx, cancelConnect := context.WithTimeout(ctx, 15*time.Second)
	defer cancelConnect()
	admin, err := pgx.Connect(connectCtx, url)
	if err != nil {
		t.Fatal("cannot connect to configured test database")
	}
	schema := "dsh_profile_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, err := admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		if err != nil {
			t.Error(err)
		}
		if err = admin.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	pool := func() *pgxpool.Pool {
		config, err := pgxpool.ParseConfig(url)
		if err != nil {
			t.Fatal("invalid test database configuration")
		}
		config.ConnConfig.RuntimeParams["search_path"] = schema
		config.MaxConns = 2
		result, err := pgxpool.NewWithConfig(ctx, config)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(result.Close)
		return result
	}
	a, b := pool(), pool()
	for _, stem := range []string{"9223_dsh_employee_host", "9224_dsh_employee_host_identity", "9241_dsh_employee_profile", "9242_dsh_employee_profile_identity", "9243_dsh_profile_revision_identity", "9244_dsh_plugin_build_identity", "9245_dsh_plugin_build_worker", "9246_dsh_plugin_build_due", "9247_dsh_plugin_build_attempts"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "migrations", stem+".up.sql"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = a.Exec(ctx, string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = a.Exec(ctx, `CREATE TABLE workspace(id uuid); CREATE TABLE agent(id uuid,workspace_id uuid,kind text DEFAULT 'user',archived_at timestamptz,runtime_mode text DEFAULT 'cloud')`); err != nil {
		t.Fatal(err)
	}
	return a, b
}

func TestProfilePostgresRevisionsAndFencing(t *testing.T) {
	a, b := profilePools(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	source := fixtureSource()
	key := dshhost.Key{WorkspaceID: uuid.New(), AgentID: uuid.New()}
	if _, err := a.Exec(ctx, `INSERT INTO workspace(id) VALUES($1)`, key.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Exec(ctx, `INSERT INTO agent(id,workspace_id) VALUES($1,$2)`, key.AgentID, key.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	read := func(context.Context, *db.Queries, dshhost.Key, string) (Source, error) { return source, nil }
	stores := []Store{{DB: a}, {DB: b}}
	type outcome struct {
		revision Revision
		err      error
	}
	results := make(chan outcome, 8)
	var group sync.WaitGroup
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			revision, err := stores[i%2].Prepare(ctx, key, source.TemplateID, read)
			results <- outcome{revision, err}
		}(i)
	}
	group.Wait()
	close(results)
	var revision Revision
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if revision.ID != 0 && result.revision.ID != revision.ID {
			t.Fatal("replicas published duplicate revisions")
		}
		revision = result.revision
	}
	if revision.Descriptor != "" || len(revision.Builds) != 1 {
		t.Fatal("queued build was treated as executable")
	}
	var count int
	if err := a.QueryRow(ctx, `SELECT count(*) FROM dsh_plugin_build WHERE workspace_id=$1`, key.WorkspaceID).Scan(&count); err != nil || count != 1 {
		t.Fatal("build intent duplicated", err, count)
	}

	if _, err := a.Exec(ctx, `UPDATE dsh_plugin_build SET state='failed' WHERE workspace_id=$1`, key.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	failed, err := stores[1].Status(ctx, key)
	if err != nil || failed.State != "build_failed" || failed.Current || len(failed.Builds) != 1 {
		t.Fatal("failed build not visible across replicas", err)
	}
	if _, err := a.Exec(ctx, `UPDATE dsh_plugin_build SET state='ready',build_digest=$2,artifact_key='fixture/object' WHERE workspace_id=$1`, key.WorkspaceID, strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
	resolved, err := stores[1].Prepare(ctx, key, source.TemplateID, read)
	if err != nil || resolved.ID != revision.ID || resolved.Descriptor == "" {
		t.Fatal("build did not resolve original revision", err)
	}
	host := dshhost.Host{Key: key, State: "running", Generation: 1, SandboxID: "sandbox-one", TemplateID: source.TemplateID}
	if _, err = a.Exec(ctx, `INSERT INTO dsh_employee_host(workspace_id,agent_id,file_system_id,space_id,volume_name,access_point_arn,role_arn,vpc_id,security_group_id,vswitch_ids,state,generation,create_intent,sandbox_id,template_id)
 VALUES($1,$2,'fixture-fs','fixture-space','fixture-volume','fixture-ap','fixture-role','fixture-vpc','fixture-sg',ARRAY['fixture-vsw'],'running',1,$3,$4,$5)`, key.WorkspaceID, key.AgentID, uuid.New(), host.SandboxID, host.TemplateID); err != nil {
		t.Fatal(err)
	}
	if err = stores[0].Acknowledge(ctx, host, resolved, read); err != nil {
		t.Fatal(err)
	}
	var first time.Time
	if err = a.QueryRow(ctx, `SELECT applied_at FROM dsh_employee_profile WHERE workspace_id=$1 AND agent_id=$2`, key.WorkspaceID, key.AgentID).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if err = stores[1].Acknowledge(ctx, host, resolved, read); err != nil {
		t.Fatal(err)
	}
	var repeated time.Time
	if err = a.QueryRow(ctx, `SELECT applied_at FROM dsh_employee_profile WHERE workspace_id=$1 AND agent_id=$2`, key.WorkspaceID, key.AgentID).Scan(&repeated); err != nil || !repeated.Equal(first) {
		t.Fatal("replay changed original application time", err)
	}
	status, err := stores[1].Status(ctx, key)
	if err != nil || !status.Current || status.State != "applied" {
		t.Fatal("valid receipt not visible across replicas", err)
	}
	source.Plugins[0].Config = map[string]any{"credential": "rotated"}
	source.Plugins[0].ConfigRevision++
	if err = stores[0].Acknowledge(ctx, host, resolved, read); !errors.Is(err, ErrChanged) {
		t.Fatal("stale settings acknowledged", err)
	}
	next, err := stores[0].Prepare(ctx, key, source.TemplateID, read)
	if err != nil || next.ID == resolved.ID || next.Descriptor == "" {
		t.Fatal("new configuration was not versioned", err)
	}
	if err = stores[1].Acknowledge(ctx, host, resolved, read); !errors.Is(err, ErrChanged) {
		t.Fatal("old revision acknowledged", err)
	}
	if _, err = a.Exec(ctx, `UPDATE dsh_employee_host SET generation=2,sandbox_id='sandbox-two' WHERE workspace_id=$1 AND agent_id=$2`, key.WorkspaceID, key.AgentID); err != nil {
		t.Fatal(err)
	}
	if err = stores[0].Acknowledge(ctx, host, next, read); !errors.Is(err, ErrChanged) {
		t.Fatal("old generation acknowledged", err)
	}
	host.Generation = 2
	host.SandboxID = "sandbox-two"
	if err = stores[1].Acknowledge(ctx, host, next, read); err != nil {
		t.Fatal(err)
	}
	if _, err = a.Exec(ctx, `UPDATE dsh_employee_host SET state='retiring' WHERE workspace_id=$1 AND agent_id=$2`, key.WorkspaceID, key.AgentID); err != nil {
		t.Fatal(err)
	}
	if err = stores[0].Acknowledge(ctx, host, next, read); !errors.Is(err, ErrChanged) {
		t.Fatal("retiring writer acknowledged", err)
	}
	status, err = stores[1].Status(ctx, key)
	if err != nil || status.Current {
		t.Fatal("retiring writer displayed as current", err)
	}
	alien := key
	alien.AgentID = uuid.New()
	if _, err = stores[0].Prepare(ctx, alien, source.TemplateID, read); err == nil {
		t.Fatal("missing employee acquired persistent Profile")
	}
	if _, err = stores[1].Load(ctx, alien, next.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("revision escaped employee scope", err)
	}
}
