package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/dshhost"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestDSHNativeSessionRegistrationRequiresTransactionAndInvocation(t *testing.T) {
	a, _, agent := nativeChatTestIdentity()
	s := &TaskService{Queries: db.New(nil)}
	if _, err := s.RegisterDSHNativeChatSession(context.Background(), agent, a.access, a.input.SessionID, a.input.Workdir, a.invoke); err == nil {
		t.Fatal("registered without a transaction")
	}
	starter := &nativeChatNeverBegin{}
	s.TxStarter = starter
	deny := func(context.Context, *db.Queries, db.Agent, pgtype.UUID) error { return dshhost.ErrNativeAccessDenied }
	if _, err := s.RegisterDSHNativeChatSession(context.Background(), agent, a.access, a.input.SessionID, a.input.Workdir, deny); !errors.Is(err, dshhost.ErrNativeAccessDenied) {
		t.Fatalf("invocation denial = %v", err)
	}
	if _, err := s.RegisterDSHNativeChatSession(context.Background(), agent, a.access, a.input.SessionID, "/tmp", a.invoke); !errors.Is(err, ErrDSHNativeInput) {
		t.Fatalf("invalid workdir = %v", err)
	}
	if starter.calls != 0 {
		t.Fatal("invalid registration reached database")
	}
}

func TestDSHNativeSessionRegistrationReplaysAcrossReplicas(t *testing.T) {
	s, a, _, agent, pool, count := nativeChatDatabaseFixture(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	results := make([]DSHNativeChatSession, 2)
	errs := make([]error, 2)
	for i := range 2 {
		wg.Go(func() {
			replica := &TaskService{Queries: s.Queries, TxStarter: s.TxStarter, Bus: s.Bus}
			results[i], errs[i] = replica.RegisterDSHNativeChatSession(ctx, agent, a.access, a.input.SessionID, a.input.Workdir, a.invoke)
		})
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if results[0].Session.ID != results[1].Session.ID || results[0].Created == results[1].Created || count.Load() != 0 {
		t.Fatal("registration duplicated sessions or published a fake task")
	}
	var sessions, tasks int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM dsh_employee_session WHERE agent_id=$1`, agent.ID).Scan(&sessions); err != nil || sessions != 1 {
		t.Fatalf("native mappings = %d error=%v", sessions, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE agent_id=$1`, agent.ID).Scan(&tasks); err != nil || tasks != 0 {
		t.Fatal("registration created a task")
	}
	// A later caller cannot take over an existing human's platform transcript.
	if _, err := pool.Exec(ctx, `UPDATE chat_session SET creator_id=$2 WHERE id=$1`, results[0].Session.ID, uuid.New()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegisterDSHNativeChatSession(ctx, agent, a.access, a.input.SessionID, a.input.Workdir, a.invoke); !errors.Is(err, dshhost.ErrNativeAccessDenied) {
		t.Fatalf("other creator registration = %v", err)
	}
}

func TestDSHNativeSessionRegistrationKeepsPlatformScope(t *testing.T) {
	s, a, session, agent, pool, _ := nativeChatDatabaseFixture(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	store := dshhost.PostgresStore{DB: tx}
	if err := store.AdoptNativeSession(ctx, dshhost.SessionScope{Key: a.access.Key, Kind: "chat", ID: uuid.UUID(session.ID.Bytes)}, a.input.SessionID); err != nil {
		t.Fatal(err)
	}
	issueSession := "session-" + uuid.NewString()
	if err := store.AdoptNativeSession(ctx, dshhost.SessionScope{Key: a.access.Key, Kind: "issue", ID: uuid.New()}, issueSession); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	result, err := s.RegisterDSHNativeChatSession(ctx, agent, a.access, a.input.SessionID, a.input.Workdir, a.invoke)
	if err != nil || result.Created || result.Session.ID != session.ID {
		t.Fatalf("platform chat identity was not preserved: %v", err)
	}
	if _, err := s.RegisterDSHNativeChatSession(ctx, agent, a.access, issueSession, dshhost.MountPath+"/workspaces/"+issueSession, a.invoke); !errors.Is(err, dshhost.ErrChanged) {
		t.Fatalf("issue Session was rebound as chat: %v", err)
	}
}

type nativeSessionFaultStarter struct{ pool *pgxpool.Pool }

func (s nativeSessionFaultStarter) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return nativeSessionFaultTx{Tx: tx}, nil
}

type nativeSessionFaultTx struct{ pgx.Tx }

func (tx nativeSessionFaultTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if strings.Contains(sql, "INSERT INTO dsh_employee_session") {
		return pgconn.CommandTag{}, errors.New("injected mapping write failure")
	}
	return tx.Tx.Exec(ctx, sql, args...)
}

func TestDSHNativeSessionRegistrationRollsBackMappingFailure(t *testing.T) {
	s, a, _, agent, pool, count := nativeChatDatabaseFixture(t)
	s.TxStarter = nativeSessionFaultStarter{pool: pool}
	if _, err := s.RegisterDSHNativeChatSession(context.Background(), agent, a.access, a.input.SessionID, a.input.Workdir, a.invoke); err == nil {
		t.Fatal("mapping write failure was accepted")
	}
	var sessions, mappings int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM chat_session WHERE agent_id=$1`, agent.ID).Scan(&sessions); err != nil || sessions != 1 {
		t.Fatal("failed mapping left an extra platform session")
	}
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM dsh_employee_session WHERE agent_id=$1`, agent.ID).Scan(&mappings); err != nil || mappings != 0 || count.Load() != 0 {
		t.Fatal("failed registration persisted mapping or event")
	}
}
