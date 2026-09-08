package db_test

import (
	"context"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"os"
	"testing"
)

func TestDingTalkIdentityReuse(t *testing.T) {
	url := os.Getenv("IDENTITY_REUSE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("IDENTITY_REUSE_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := conn.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`CREATE TEMP TABLE agent(id uuid PRIMARY KEY,workspace_id uuid,owner_id uuid,name text,archived_at timestamptz);
 CREATE TEMP TABLE agent_dingtalk_identity(agent_id uuid PRIMARY KEY,workspace_id uuid,dws_uid text,org_id text,organization_name text,account_display_name text,account_avatar_url text,bound_by uuid,bound_at timestamptz,updated_at timestamptz);
 CREATE TEMP TABLE activity_log(id uuid DEFAULT gen_random_uuid(),workspace_id uuid,actor_type text,actor_id uuid,action text,details jsonb);`)
	uid := func(n byte) pgtype.UUID { return pgtype.UUID{Bytes: [16]byte{15: n}, Valid: true} }
	ws, user, other, source, target, duplicate, foreign, archived := uid(1), uid(2), uid(3), uid(4), uid(5), uid(6), uid(7), uid(8)
	for _, id := range []pgtype.UUID{source, target, duplicate, foreign, archived} {
		exec(`INSERT INTO agent VALUES($1,$2,$3,'same name',NULL)`, id, ws, user)
	}
	exec(`UPDATE agent SET owner_id=$1 WHERE id=$2`, other, foreign)
	exec(`UPDATE agent SET archived_at=now() WHERE id=$1`, archived)
	for _, id := range []pgtype.UUID{source, duplicate, foreign, archived} {
		exec(`INSERT INTO agent_dingtalk_identity VALUES($1,$2,'123','456','org','name','',$3,now(),now())`, id, ws, user)
	}
	q := db.New(conn)
	list := db.ListReusableDingTalkIdentitiesParams{WorkspaceID: ws, UserID: user, TargetAgentID: target}
	rows, err := q.ListReusableDingTalkIdentities(ctx, list)
	if err != nil || len(rows) != 1 {
		t.Fatalf("deduplicated own sources: %d %v", len(rows), err)
	}
	p := db.ReuseDingTalkIdentityParams{WorkspaceID: ws, UserID: user, SourceAgentID: source, TargetAgentID: target}
	for _, mutate := range []func(*db.ReuseDingTalkIdentityParams){
		func(p *db.ReuseDingTalkIdentityParams) { p.UserID = other },
		func(p *db.ReuseDingTalkIdentityParams) { p.WorkspaceID = uid(9) },
		func(p *db.ReuseDingTalkIdentityParams) { p.SourceAgentID = foreign },
		func(p *db.ReuseDingTalkIdentityParams) { p.SourceAgentID = archived },
		func(p *db.ReuseDingTalkIdentityParams) { p.TargetAgentID = foreign },
		func(p *db.ReuseDingTalkIdentityParams) { p.TargetAgentID = archived },
	} {
		bad := p
		mutate(&bad)
		if _, err := q.ReuseDingTalkIdentity(ctx, bad); err != pgx.ErrNoRows {
			t.Fatalf("scope bypass: %v", err)
		}
	}
	exec(`UPDATE agent_dingtalk_identity SET bound_by=$1 WHERE agent_id=$2`, other, source)
	if _, err := q.ReuseDingTalkIdentity(ctx, p); err != pgx.ErrNoRows {
		t.Fatalf("foreign authorization reused: %v", err)
	}
	exec(`UPDATE agent_dingtalk_identity SET bound_by=$1 WHERE agent_id=$2`, user, source)
	// An audit failure must leave no new binding.
	exec(`ALTER TABLE activity_log ADD CONSTRAINT deny_audit CHECK (false)`)
	if _, err := q.ReuseDingTalkIdentity(ctx, p); err == nil {
		t.Fatal("audit failure accepted")
	}
	var n int
	conn.QueryRow(ctx, `SELECT count(*) FROM agent_dingtalk_identity WHERE agent_id=$1`, target).Scan(&n)
	if n != 0 {
		t.Fatal("identity survived failed audit")
	}
	exec(`ALTER TABLE activity_log DROP CONSTRAINT deny_audit`)
	got, err := q.ReuseDingTalkIdentity(ctx, p)
	if err != nil || got != target {
		t.Fatalf("reuse: %v %v", got, err)
	}
	var boundAt string
	conn.QueryRow(ctx, `SELECT bound_at::text FROM agent_dingtalk_identity WHERE agent_id=$1`, target).Scan(&boundAt)
	if _, err := q.ReuseDingTalkIdentity(ctx, p); err != nil {
		t.Fatalf("idempotent repeat: %v", err)
	}
	var again string
	conn.QueryRow(ctx, `SELECT bound_at::text FROM agent_dingtalk_identity WHERE agent_id=$1`, target).Scan(&again)
	if again != boundAt {
		t.Fatal("idempotent repeat changed binding timestamp")
	}
	conn.QueryRow(ctx, `SELECT count(*) FROM activity_log WHERE action='agent_dingtalk_identity_reused' AND (details->>'agent_id')::uuid=$1`, target).Scan(&n)
	if n != 2 {
		t.Fatalf("missing audits: %d", n)
	}
	exec(`UPDATE agent_dingtalk_identity SET org_id='different' WHERE agent_id=$1`, target)
	if _, err := q.ReuseDingTalkIdentity(ctx, p); err != pgx.ErrNoRows {
		t.Fatalf("different identity overwritten: %v", err)
	}
	exec(`DELETE FROM agent_dingtalk_identity WHERE agent_id=$1`, source)
	if _, err := q.ReuseDingTalkIdentity(ctx, p); err != pgx.ErrNoRows {
		t.Fatalf("revoked source reused: %v", err)
	}
}
