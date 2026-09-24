package service

import (
	"context"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/dshhost"
	"github.com/multica-ai/multica/server/internal/events"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"os"
	"sync/atomic"
	"testing"
)

type dshScheduleTestInput struct {
	SessionID string
	RequestID uuid.UUID
	Workdir   string
}
type dshScheduleTestAdmission struct {
	runtimeID pgtype.UUID
	access    dshhost.Host
	input     dshScheduleTestInput
	invoke    DSHInvokeCheck
}

func dshScheduleTestIdentity() (dshScheduleTestAdmission, db.ChatSession, db.Agent) {
	u := func() pgtype.UUID { return pgtype.UUID{Bytes: uuid.New(), Valid: true} }
	session := db.ChatSession{ID: u(), WorkspaceID: u(), AgentID: u(), CreatorID: u(), RuntimeID: u(), Status: "active"}
	agent := db.Agent{ID: session.AgentID, WorkspaceID: session.WorkspaceID, RuntimeID: session.RuntimeID, RuntimeMode: "cloud", OwnerID: session.CreatorID}
	sid := uuid.NewString()
	a := dshScheduleTestAdmission{
		runtimeID: session.RuntimeID,
		access:    dshhost.Host{Key: dshhost.Key{WorkspaceID: uuid.UUID(session.WorkspaceID.Bytes), AgentID: uuid.UUID(session.AgentID.Bytes)}, Generation: 1, SandboxID: "test-native-host"},
		input:     dshScheduleTestInput{SessionID: sid, RequestID: uuid.New(), Workdir: dshhost.MountPath + "/workspaces/" + sid},
		invoke:    func(context.Context, *db.Queries, db.Agent, pgtype.UUID) error { return nil },
	}
	return a, session, agent
}

func dshScheduleDatabaseFixture(t *testing.T) (*TaskService, dshScheduleTestAdmission, db.ChatSession, db.Agent, *pgxpool.Pool, *atomic.Int32) {
	t.Helper()
	url := os.Getenv("DSH_SCHEDULE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set DSH_SCHEDULE_TEST_DATABASE_URL to a migrated preproduction test database")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	a, session, agent := dshScheduleTestIdentity()
	ws, uid, aid, rid := session.WorkspaceID, session.CreatorID, session.AgentID, session.RuntimeID
	t.Cleanup(func() {
		for _, stmt := range []struct {
			sql string
			id  pgtype.UUID
		}{
			{`DELETE FROM chat_message WHERE chat_session_id IN (SELECT id FROM chat_session WHERE agent_id=$1)`, aid},
			{`DELETE FROM dsh_task_binding WHERE agent_id=$1`, aid},
			{`DELETE FROM dsh_employee_session WHERE agent_id=$1`, aid},
			{`DELETE FROM dsh_native_access WHERE agent_id=$1`, aid},
			{`DELETE FROM dsh_employee_host WHERE agent_id=$1`, aid},
			{`DELETE FROM agent_task_queue WHERE agent_id=$1`, aid},
			{`DELETE FROM chat_session WHERE agent_id=$1`, aid},
			{`DELETE FROM agent WHERE id=$1`, aid},
			{`DELETE FROM agent_runtime WHERE id=$1`, rid},
			{`DELETE FROM member WHERE workspace_id=$1`, ws},
			{`DELETE FROM workspace WHERE id=$1`, ws},
			{`DELETE FROM "user" WHERE id=$1`, uid},
		} {
			if _, err := pool.Exec(ctx, stmt.sql, stmt.id); err != nil {
				t.Error(err)
			}
		}
	})
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO "user" (id,name,email) VALUES ($1,'DSH Native Test',$2)`, uid, uuid.NewString()+"@multica.test")
	exec(`INSERT INTO workspace (id,name,slug) VALUES ($1,'DSH Native Test',$2)`, ws, "dsh-native-"+uuid.NewString())
	exec(`INSERT INTO member (workspace_id,user_id,role) VALUES ($1,$2,'owner')`, ws, uid)
	exec(`INSERT INTO agent_runtime (id,workspace_id,daemon_id,name,runtime_mode,provider,status,owner_id,metadata)
 VALUES ($1,$2,$3,'DSH Native Test','cloud','dsh','online',$4,'{"kind":"cloud-sandbox","sandbox_backend":"aliyun_fc","provider":"dsh","template_id":"native-test-template"}')`, rid, ws, uuid.NewString(), uid)
	exec(`INSERT INTO agent (id,workspace_id,name,runtime_mode,runtime_id,owner_id,permission_mode)
 VALUES ($1,$2,'DSH Native Test','cloud',$3,$4,'private')`, aid, ws, rid, uid)
	exec(`INSERT INTO chat_session (id,workspace_id,agent_id,creator_id,runtime_id) VALUES ($1,$2,$3,$4,$5)`, session.ID, ws, aid, uid, rid)
	exec(`INSERT INTO dsh_employee_host (workspace_id,agent_id,file_system_id,space_id,volume_name,access_point_arn,role_arn,vpc_id,security_group_id,vswitch_ids,state,generation,create_intent,sandbox_id,template_id)
 VALUES ($1,$2,$3,$3,$3,$3,$3,'fixture-vpc','fixture-sg',ARRAY['fixture-vsw'],'running',1,$4,$5,'native-test-template')`, ws, aid, uuid.NewString(), uuid.New(), a.access.SandboxID)
	queries := db.New(pool)
	session, err = queries.GetChatSession(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	agent, err = queries.GetAgent(ctx, aid)
	if err != nil {
		t.Fatal(err)
	}
	bus := events.New()
	count := &atomic.Int32{}
	bus.Subscribe(protocol.EventTaskQueued, func(events.Event) { count.Add(1) })
	return &TaskService{Queries: queries, TxStarter: pool, Bus: bus}, a, session, agent, pool, count
}
