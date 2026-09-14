package service

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/chattrace"
	"github.com/multica-ai/multica/server/internal/dshhost"
	"github.com/multica-ai/multica/server/internal/events"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func nativeChatTestIdentity() (dshNativeChatAdmission, db.ChatSession, db.Agent) {
	u := func() pgtype.UUID { return pgtype.UUID{Bytes: uuid.New(), Valid: true} }
	session := db.ChatSession{ID: u(), WorkspaceID: u(), AgentID: u(), CreatorID: u(), RuntimeID: u(), Status: "active"}
	agent := db.Agent{ID: session.AgentID, WorkspaceID: session.WorkspaceID, RuntimeID: session.RuntimeID, RuntimeMode: "cloud", OwnerID: session.CreatorID}
	sid := uuid.NewString()
	a := dshNativeChatAdmission{
		runtimeID: session.RuntimeID,
		access:    dshhost.NativeAccess{ID: uuid.New(), Key: dshhost.Key{WorkspaceID: uuid.UUID(session.WorkspaceID.Bytes), AgentID: uuid.UUID(session.AgentID.Bytes)}, UserID: uuid.UUID(session.CreatorID.Bytes), Generation: 1, SandboxID: "test-native-host", Kind: "session"},
		input:     DSHNativeChatInput{SessionID: sid, RequestID: uuid.New(), Workdir: dshhost.MountPath + "/workspaces/" + sid},
		invoke:    func(context.Context, *db.Queries, db.Agent, pgtype.UUID) error { return nil },
	}
	return a, session, agent
}

func TestDSHNativeChatRejectsMismatchedHumanAndScope(t *testing.T) {
	for _, change := range []struct {
		name  string
		apply func(*dshNativeChatAdmission, *db.ChatSession, *db.Agent)
	}{
		{"no invocation policy", func(a *dshNativeChatAdmission, _ *db.ChatSession, _ *db.Agent) { a.invoke = nil }},
		{"entry token", func(a *dshNativeChatAdmission, _ *db.ChatSession, _ *db.Agent) { a.access.Kind = "entry" }},
		{"no host generation", func(a *dshNativeChatAdmission, _ *db.ChatSession, _ *db.Agent) { a.access.Generation = 0 }},
		{"no sandbox", func(a *dshNativeChatAdmission, _ *db.ChatSession, _ *db.Agent) { a.access.SandboxID = "" }},
		{"other human", func(a *dshNativeChatAdmission, _ *db.ChatSession, _ *db.Agent) { a.access.UserID = uuid.New() }},
		{"other workspace", func(a *dshNativeChatAdmission, _ *db.ChatSession, _ *db.Agent) { a.access.WorkspaceID = uuid.New() }},
		{"other employee", func(a *dshNativeChatAdmission, _ *db.ChatSession, _ *db.Agent) { a.access.AgentID = uuid.New() }},
		{"other session creator", func(_ *dshNativeChatAdmission, s *db.ChatSession, _ *db.Agent) { s.CreatorID.Bytes = uuid.New() }},
		{"rebound runtime", func(_ *dshNativeChatAdmission, _ *db.ChatSession, a *db.Agent) { a.RuntimeID.Bytes = uuid.New() }},
		{"local employee", func(_ *dshNativeChatAdmission, _ *db.ChatSession, a *db.Agent) { a.RuntimeMode = "local" }},
	} {
		t.Run(change.name, func(t *testing.T) {
			a, s, agent := nativeChatTestIdentity()
			if err := a.validateIdentity(s, agent, s.CreatorID); err != nil {
				t.Fatal(err)
			}
			change.apply(&a, &s, &agent)
			if err := a.validateIdentity(s, agent, s.CreatorID); err == nil {
				t.Fatal("accepted changed identity")
			}
		})
	}
}

func TestDSHNativeChatInputPreservesCanonicalWorkdir(t *testing.T) {
	a, _, _ := nativeChatTestIdentity()
	for _, sid := range []string{a.input.SessionID, "session-" + a.input.SessionID} {
		input := DSHNativeChatInput{SessionID: sid, RequestID: uuid.New(), Workdir: dshhost.MountPath + "/workspaces/" + sid}
		if err := input.Validate("你好"); err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{"/tmp", input.Workdir + "/..", strings.ToUpper(input.Workdir), input.Workdir + "/"} {
			bad := input
			bad.Workdir = path
			if bad.Validate("prompt") == nil {
				t.Fatalf("accepted workdir %q", path)
			}
		}
	}
	for _, content := range []string{"", " \n", "nul\x00text", string([]byte{0xff}), strings.Repeat("x", 256*1024+1)} {
		if a.input.Validate(content) == nil {
			t.Fatal("accepted invalid content")
		}
	}
	for _, sid := range []string{"../session", uuid.Nil.String(), ""} {
		bad := a.input
		bad.SessionID = sid
		bad.Workdir = dshhost.MountPath + "/workspaces/" + sid
		if bad.Validate("prompt") == nil {
			t.Fatal("accepted invalid session")
		}
	}
	bad := a.input
	bad.RequestID = uuid.Nil
	if bad.Validate("prompt") == nil {
		t.Fatal("accepted missing request ID")
	}
}

type nativeChatNeverBegin struct{ calls int }

func (s *nativeChatNeverBegin) Begin(context.Context) (pgx.Tx, error) {
	s.calls++
	return nil, errors.New("unexpected transaction")
}

func TestDSHNativeChatRequiresTransactionAndInvokeBeforeOverlay(t *testing.T) {
	a, session, agent := nativeChatTestIdentity()
	builder := &stubOverlayBuilder{}
	s := &TaskService{Queries: db.New(nil), Composio: builder, FeatureFlags: composioMCPAppsTestFlags(true)}
	if _, err := s.SendDSHNativeChatMessage(context.Background(), session, agent, a.access, a.input, "prompt", chattrace.New("native_test"), a.invoke); err == nil {
		t.Fatal("accepted missing transaction")
	}
	starter := &nativeChatNeverBegin{}
	s.TxStarter = starter
	deny := func(context.Context, *db.Queries, db.Agent, pgtype.UUID) error { return dshhost.ErrNativeAccessDenied }
	if _, err := s.SendDSHNativeChatMessage(context.Background(), session, agent, a.access, a.input, "prompt", chattrace.New("native_test"), deny); !errors.Is(err, dshhost.ErrNativeAccessDenied) {
		t.Fatalf("deny = %v", err)
	}
	if starter.calls != 0 || builder.calls != 0 {
		t.Fatal("denied caller reached transaction or connected apps")
	}
}

// Explicitly opt in to a migrated preproduction test database. Default local
// tests do not open a database, launch a runtime, or resolve any real credential.
func nativeChatDatabaseFixture(t *testing.T) (*TaskService, dshNativeChatAdmission, db.ChatSession, db.Agent, *pgxpool.Pool, *atomic.Int32) {
	t.Helper()
	url := os.Getenv("DSH_NATIVE_CHAT_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set DSH_NATIVE_CHAT_TEST_DATABASE_URL to a migrated preproduction test database")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	a, session, agent := nativeChatTestIdentity()
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
	exec(`INSERT INTO dsh_native_access (id,workspace_id,agent_id,user_id,generation,sandbox_id,kind,token_hash,expires_at)
 VALUES ($1,$2,$3,$4,1,$5,'session',$6,clock_timestamp()+interval '5 minutes')`, a.access.ID, ws, aid, uid, a.access.SandboxID, strings.ReplaceAll(uuid.NewString()+uuid.NewString(), "-", ""))
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

func TestDSHNativeChatConcurrentReplayAndNextTurn(t *testing.T) {
	s, a, session, agent, pool, count := nativeChatDatabaseFixture(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	results := make([]*DirectChatSendResult, 2)
	errs := make([]error, 2)
	for i := range 2 {
		wg.Go(func() {
			replica := TaskService{Queries: s.Queries, TxStarter: s.TxStarter, Bus: s.Bus}
			results[i], errs[i] = replica.SendDSHNativeChatMessage(ctx, session, agent, a.access, a.input, "first turn", chattrace.New("native_test"), a.invoke)
		})
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if results[0].Task.ID != results[1].Task.ID || results[0].Message.ID != results[1].Message.ID || results[0].Replayed == results[1].Replayed || results[0].Queued || results[1].Queued || count.Load() != 1 {
		t.Fatal("concurrent replay created or announced duplicate input")
	}
	if _, err := s.SendDSHNativeChatMessage(ctx, session, agent, a.access, a.input, "changed input", chattrace.New("native_test"), a.invoke); !errors.Is(err, dshhost.ErrChanged) {
		t.Fatalf("changed replay = %v", err)
	}
	a.input.RequestID = uuid.New()
	next, err := s.SendDSHNativeChatMessage(ctx, session, agent, a.access, a.input, "next turn", chattrace.New("native_test"), a.invoke)
	if err != nil {
		t.Fatal(err)
	}
	if next.Replayed || !next.Queued || next.Task.ID == results[0].Task.ID || count.Load() != 2 {
		t.Fatal("next request did not create a queued turn")
	}
	var bindings, messages int
	if err := pool.QueryRow(ctx, `SELECT count(*),count(DISTINCT session_id) FROM dsh_task_binding WHERE agent_id=$1`, agent.ID).Scan(&bindings, &messages); err != nil || bindings != 2 || messages != 1 {
		t.Fatalf("bindings = %d sessions = %d error = %v", bindings, messages, err)
	}
}

func TestDSHNativeChatWaitsForHostAdmissionLock(t *testing.T) {
	s, a, session, agent, pool, count := nativeChatDatabaseFixture(t)
	conn, err := pool.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	release, err := lockDSHEmployee(context.Background(), conn, agent.WorkspaceID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	if _, err := s.SendDSHNativeChatMessage(ctx, session, agent, a.access, a.input, "prompt", chattrace.New("native_test"), a.invoke); err == nil || ctx.Err() == nil {
		t.Fatalf("native admission bypassed the Host lock: %v", err)
	}
	var tasks int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM agent_task_queue WHERE agent_id=$1`, agent.ID).Scan(&tasks); err != nil || tasks != 0 || count.Load() != 0 {
		t.Fatal("blocked admission persisted or announced a task")
	}
}

type nativeChatFaultStarter struct {
	pool  *pgxpool.Pool
	stage string
}

func (s nativeChatFaultStarter) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return nativeChatFaultTx{Tx: tx, stage: s.stage}, nil
}

type nativeChatFaultTx struct {
	pgx.Tx
	stage string
}
type nativeChatErrorRow struct{}

func (nativeChatErrorRow) Scan(...any) error { return errors.New("injected input write failure") }
func (tx nativeChatFaultTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if tx.stage == "input" && strings.Contains(sql, "-- name: CreateChatMessage :one") {
		return nativeChatErrorRow{}
	}
	return tx.Tx.QueryRow(ctx, sql, args...)
}
func (tx nativeChatFaultTx) Commit(ctx context.Context) error {
	if err := tx.Tx.Commit(ctx); err != nil {
		return err
	}
	if tx.stage == "commit response" {
		return errors.New("injected lost commit response")
	}
	return nil
}

func TestDSHNativeChatRollsBackInputFailureAndRecoversUnknownCommit(t *testing.T) {
	if os.Getenv("DSH_NATIVE_CHAT_TEST_DATABASE_URL") == "" {
		t.Skip("requires a migrated preproduction test database")
	}
	for _, stage := range []string{"input", "commit response"} {
		t.Run(stage, func(t *testing.T) {
			s, a, session, agent, pool, events := nativeChatDatabaseFixture(t)
			ctx := context.Background()
			s.TxStarter = nativeChatFaultStarter{pool: pool, stage: stage}
			if _, err := s.SendDSHNativeChatMessage(ctx, session, agent, a.access, a.input, "prompt", chattrace.New("native_test"), a.invoke); err == nil {
				t.Fatal("fault unexpectedly succeeded")
			}
			if events.Load() != 0 {
				t.Fatal("announced unconfirmed transaction")
			}
			want := 0
			if stage == "commit response" {
				want = 1
			}
			for _, table := range []string{"agent_task_queue", "dsh_employee_session", "dsh_task_binding"} {
				var n int
				if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+table+" WHERE agent_id=$1", agent.ID).Scan(&n); err != nil || n != want {
					t.Fatalf("%s count = %d, want %d, err = %v", table, n, want, err)
				}
			}
			var n int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM chat_message WHERE chat_session_id=$1`, session.ID).Scan(&n); err != nil || n != want {
				t.Fatalf("input count = %d, want %d, err = %v", n, want, err)
			}
			s.TxStarter = pool
			retry, err := s.SendDSHNativeChatMessage(ctx, session, agent, a.access, a.input, "prompt", chattrace.New("native_test"), a.invoke)
			if err != nil || retry == nil || retry.Replayed != (want == 1) {
				t.Fatalf("retry did not preserve transaction outcome: %v", err)
			}
		})
	}
}

func TestDSHNativeChatRechecksGrantAndInvocationInsideTransaction(t *testing.T) {
	if os.Getenv("DSH_NATIVE_CHAT_TEST_DATABASE_URL") == "" {
		t.Skip("requires a migrated preproduction test database")
	}
	for _, stage := range []string{"revoked", "expired", "stale host", "invoke removed"} {
		t.Run(stage, func(t *testing.T) {
			s, a, session, agent, pool, count := nativeChatDatabaseFixture(t)
			ctx := context.Background()
			var err error
			switch stage {
			case "revoked":
				_, err = pool.Exec(ctx, `UPDATE dsh_native_access SET kind='revoked' WHERE id=$1`, a.access.ID)
			case "expired":
				_, err = pool.Exec(ctx, `UPDATE dsh_native_access SET expires_at=$2 WHERE id=$1`, a.access.ID, time.Now().Add(-time.Hour))
			case "stale host":
				a.access.Generation++
			case "invoke removed":
				calls := 0
				a.invoke = func(context.Context, *db.Queries, db.Agent, pgtype.UUID) error {
					calls++
					if calls > 1 {
						return dshhost.ErrNativeAccessDenied
					}
					return nil
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.SendDSHNativeChatMessage(ctx, session, agent, a.access, a.input, "prompt", chattrace.New("native_test"), a.invoke); !errors.Is(err, dshhost.ErrNativeAccessDenied) {
				t.Fatalf("admission = %v", err)
			}
			var tasks int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE agent_id=$1`, agent.ID).Scan(&tasks); err != nil || tasks != 0 || count.Load() != 0 {
				t.Fatal("denied request created a task")
			}
		})
	}
}

func TestDSHNativeChatSourceReplayComparesFullInput(t *testing.T) {
	a, _, _ := nativeChatTestIdentity()
	text := "input"
	data := "aGVsbG8="
	mime := "image/png"
	receipt := "upload"
	zone := "Asia/Shanghai"
	p := &protocol.DSHNativePrompt{SessionID: a.input.SessionID, RequestID: a.input.RequestID.String(), Mode: "queue", Content: []protocol.DSHNativePromptPart{{Type: "text", Text: &text}, {Type: "image", MediaType: &mime, Data: &data}, {Type: "file", ReceiptID: &receipt}}, ClientTimeZone: &zone}
	a.input.Prompt = p
	if a.input.Validate(p.DisplayText()) != nil {
		t.Fatal("valid typed admission rejected")
	}
	if a.input.Validate("altered summary") == nil {
		t.Fatal("display input detached from native input")
	}
	raw, err := dshNativeSource(p)
	if err != nil {
		t.Fatal(err)
	}
	if !sameDSHNativeSource(raw, p) || sameDSHNativeSource(raw, nil) || sameDSHNativeSource(nil, p) {
		t.Fatal("typed replay lost presence semantics")
	}
	for _, field := range []string{"mode", "image", "file", "zone", "request", "session"} {
		t.Run(field, func(t *testing.T) {
			changed, err := p.Clone()
			if err != nil {
				t.Fatal(err)
			}
			switch field {
			case "mode":
				changed.Mode = "steer"
			case "image":
				*changed.Content[1].Data = "d29ybGQ="
			case "file":
				*changed.Content[2].ReceiptID = "other"
			case "zone":
				*changed.ClientTimeZone = "UTC"
			case "request":
				changed.RequestID = uuid.NewString()
			case "session":
				changed.SessionID = uuid.NewString()
			}
			if sameDSHNativeSource(raw, changed) {
				t.Fatal("changed native input treated as replay")
			}
		})
	}
	if !sameDSHNativeSource([]byte(`{"other":"metadata"}`), nil) {
		t.Fatal("legacy source rejected")
	}
	if sameDSHNativeSource([]byte(`{"dsh_native_prompt":null}`), nil) {
		t.Fatal("malformed typed input treated as legacy")
	}
}

func TestDSHNativeChatTypedPersistenceAndBusySteer(t *testing.T) {
	s, a, session, agent, pool, count := nativeChatDatabaseFixture(t)
	ctx := context.Background()
	receipt := "fixture-session-upload"
	zone := "Asia/Shanghai"
	a.input.Prompt = &protocol.DSHNativePrompt{SessionID: a.input.SessionID, RequestID: a.input.RequestID.String(), Mode: "queue", Content: []protocol.DSHNativePromptPart{{Type: "file", ReceiptID: &receipt}}, ClientTimeZone: &zone}
	content := a.input.Prompt.DisplayText()
	result, err := s.SendDSHNativeChatMessage(ctx, session, agent, a.access, a.input, content, chattrace.New("native_test"), a.invoke)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := s.Queries.GetChatMessage(ctx, result.Message.ID)
	if err != nil || !sameDSHNativeSource(stored.SourcePayload, a.input.Prompt) {
		t.Fatal("complete native payload was not persisted")
	}
	replay, err := s.SendDSHNativeChatMessage(ctx, session, agent, a.access, a.input, content, chattrace.New("native_test"), a.invoke)
	if err != nil || !replay.Replayed || replay.Task.ID != result.Task.ID || count.Load() != 1 {
		t.Fatal("typed retry duplicated task")
	}
	receipt = "changed-receipt"
	if _, err := s.SendDSHNativeChatMessage(ctx, session, agent, a.access, a.input, content, chattrace.New("native_test"), a.invoke); !errors.Is(err, dshhost.ErrChanged) {
		t.Fatalf("changed receipt replay: %v", err)
	}
	receipt = "fixture-session-upload"
	a.input.RequestID = uuid.New()
	a.input.Prompt.RequestID = a.input.RequestID.String()
	a.input.Prompt.Mode = "steer"
	if _, err := s.SendDSHNativeChatMessage(ctx, session, agent, a.access, a.input, content, chattrace.New("native_test"), a.invoke); !errors.Is(err, ErrDSHNativeSteerBusy) {
		t.Fatalf("busy steer silently queued: %v", err)
	}
	var tasks int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE chat_session_id=$1`, session.ID).Scan(&tasks); err != nil || tasks != 1 || count.Load() != 1 {
		t.Fatal("rejected steer left a task or event")
	}
}
