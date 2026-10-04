package digest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/pkg/llm"
	openai "github.com/openai/openai-go/v3"
)

// digestSchema opens an isolated schema with the digest migrations and a
// test-only scene fact table that mirrors M5's one-fact-per-message and
// retract-own-only contract inside real transactions.
func digestSchema(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()
	dsn := os.Getenv("EMPLOYEE_MEMORY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set EMPLOYEE_MEMORY_TEST_DATABASE_URL to an isolated PostgreSQL database")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "employee_digest_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(ctx, "DROP SCHEMA "+quoted+" CASCADE"); admin.Close() })
	pool := schemaPool(t, dsn, schema)
	for _, name := range digestMigrations {
		raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "migrations", name+".up.sql"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, string(raw)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, err = pool.Exec(ctx, `CREATE TABLE test_scene_fact (
 id uuid NOT NULL, scene_id uuid NOT NULL, type text NOT NULL, key text NOT NULL, subject text NOT NULL, insight text NOT NULL,
 origin text NOT NULL, created_by text NOT NULL, evidence_id text NOT NULL, speaker_ref text NOT NULL, speaker_name text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(), forgotten_at timestamptz, UNIQUE (scene_id, evidence_id))`); err != nil {
		t.Fatal(err)
	}
	return pool, dsn + "|" + schema
}

var digestMigrations = []string{
	"9986_employee_scene_digest_state", "9987_employee_scene_digest_state_scope_idx", "9988_employee_scene_digest_state_due_idx",
	"9989_employee_scene_digest_run", "9990_employee_scene_digest_run_id_idx", "9991_employee_scene_digest_run_scene_idx",
	"9992_employee_scene_ledger", "9993_employee_scene_ledger_identity_idx", "9994_employee_scene_ledger_time_idx",
}

func schemaPool(t *testing.T, dsn, schema string) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// secondPool opens another pool on the same schema (a second replica).
func secondPool(t *testing.T, handle string) *pgxpool.Pool {
	parts := strings.SplitN(handle, "|", 2)
	return schemaPool(t, parts[0], parts[1])
}

func newKey() SceneKey {
	return SceneKey{WorkspaceID: uuid.NewString(), AgentID: uuid.NewString(), TenantOrgID: "org-1", SceneID: uuid.NewString()}
}

// memTranscript is the M8 reader stand-in.
type memTranscript struct {
	mu    sync.Mutex
	lines map[string][]TranscriptMessage
	err   error
}

func (m *memTranscript) add(key SceneKey, lines ...TranscriptMessage) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lines == nil {
		m.lines = map[string][]TranscriptMessage{}
	}
	m.lines[key.SceneID] = append(m.lines[key.SceneID], lines...)
	sort.SliceStable(m.lines[key.SceneID], func(i, j int) bool { return less(m.lines[key.SceneID][i], m.lines[key.SceneID][j]) })
}

func less(a, b TranscriptMessage) bool {
	if !a.SentAt.Equal(b.SentAt) {
		return a.SentAt.Before(b.SentAt)
	}
	return a.ProviderMessageID < b.ProviderMessageID
}

func (m *memTranscript) After(_ context.Context, _ pgx.Tx, key SceneKey, at time.Time, id string, limit int) ([]TranscriptMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return nil, m.err
	}
	cursor := TranscriptMessage{SentAt: at, ProviderMessageID: id}
	var out []TranscriptMessage
	for _, line := range m.lines[key.SceneID] {
		if (at.IsZero() && id == "") || less(cursor, line) {
			out = append(out, line)
		}
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

func (m *memTranscript) Before(_ context.Context, _ pgx.Tx, key SceneKey, at time.Time, id string, limit int) ([]TranscriptMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cursor := TranscriptMessage{SentAt: at, ProviderMessageID: id}
	var out []TranscriptMessage
	for _, line := range m.lines[key.SceneID] {
		if less(line, cursor) {
			out = append(out, line)
		}
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}

// pgFacts mirrors M5's scene fact contract on a test table, inside the
// writer's real transaction (savepoints included).
type pgFacts struct {
	failNextUpsert atomic.Bool
	upserts        atomic.Int32
}

func (*pgFacts) HostKey(kind, subject string) (string, error) {
	norm := strings.ToLower(strings.Join(strings.Fields(subject), ""))
	if norm == "" {
		return "", errors.New("empty subject")
	}
	sum := sha256.Sum256([]byte(norm))
	return kind[:1] + "-" + hex.EncodeToString(sum[:])[:20], nil
}

func (f *pgFacts) ActiveFacts(ctx context.Context, tx pgx.Tx, key SceneKey, limit int) ([]Fact, error) {
	rows, err := tx.Query(ctx, `SELECT id::text,type,key,subject,insight,origin,created_by,evidence_id,speaker_name,created_at FROM test_scene_fact WHERE scene_id=$1::uuid AND forgotten_at IS NULL ORDER BY created_at DESC,id DESC LIMIT $2`, key.SceneID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Fact
	for rows.Next() {
		var x Fact
		if err = rows.Scan(&x.ID, &x.Type, &x.Key, &x.Subject, &x.Insight, &x.CaptureOrigin, &x.CreatedBy, &x.EvidenceID, &x.SpeakerName, &x.CreatedAt); err != nil {
			return nil, err
		}
		if x.CaptureOrigin == OriginFlush {
			x.Source = "synthesis"
		} else {
			x.Source = "observed"
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func (f *pgFacts) Upsert(ctx context.Context, tx pgx.Tx, key SceneKey, actor string, in FactUpsert) (FactWrite, error) {
	if f.failNextUpsert.CompareAndSwap(true, false) {
		return FactWrite{}, errors.New("simulated crash before commit")
	}
	if in.Evidence.SenderClass != "human" || !strings.Contains(in.Evidence.Body, in.Quote) {
		return FactWrite{}, &OpRejectedError{Reason: "ungrounded_quote"}
	}
	var existing string
	err := tx.QueryRow(ctx, `SELECT id::text FROM test_scene_fact WHERE scene_id=$1::uuid AND evidence_id=$2`, key.SceneID, in.Evidence.ProviderMessageID).Scan(&existing)
	if err == nil {
		return FactWrite{ID: existing, Replayed: true}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return FactWrite{}, err
	}
	hostKey, _ := f.HostKey(in.Kind, in.Subject)
	if _, err = tx.Exec(ctx, `UPDATE test_scene_fact SET forgotten_at=now() WHERE scene_id=$1::uuid AND type=$2 AND key=$3 AND created_by=$4 AND forgotten_at IS NULL`, key.SceneID, in.Kind, hostKey, actor); err != nil {
		return FactWrite{}, err
	}
	id := uuid.NewString()
	if _, err = tx.Exec(ctx, `INSERT INTO test_scene_fact(id,scene_id,type,key,subject,insight,origin,created_by,evidence_id,speaker_ref,speaker_name) VALUES($1::uuid,$2::uuid,$3,$4,$5,$6,'flush',$7,$8,$9,$10)`,
		id, key.SceneID, in.Kind, hostKey, in.Subject, in.Quote, actor, in.Evidence.ProviderMessageID, in.Evidence.SenderRef, in.Evidence.SenderName); err != nil {
		return FactWrite{}, err
	}
	f.upserts.Add(1)
	return FactWrite{ID: id, Changed: true}, nil
}

func (f *pgFacts) Retract(ctx context.Context, tx pgx.Tx, key SceneKey, id, actor string) error {
	tag, err := tx.Exec(ctx, `UPDATE test_scene_fact SET forgotten_at=now() WHERE scene_id=$1::uuid AND id=$2::uuid AND created_by=$3 AND forgotten_at IS NULL`, key.SceneID, id, actor)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return &OpRejectedError{Reason: "retract_denied"}
	}
	return nil
}

func seedMemberFact(t *testing.T, pool *pgxpool.Pool, key SceneKey, kind, subject, insight, evidence string) string {
	t.Helper()
	hostKey, _ := (&pgFacts{}).HostKey(kind, subject)
	id := uuid.NewString()
	if _, err := pool.Exec(context.Background(), `INSERT INTO test_scene_fact(id,scene_id,type,key,subject,insight,origin,created_by,evidence_id,speaker_ref,speaker_name) VALUES($1::uuid,$2::uuid,$3,$4,$5,$6,'window','dingtalk:org-1:uid:member',$7,'dingtalk:org-1:uid:member','成员')`,
		id, key.SceneID, kind, hostKey, subject, insight, evidence); err != nil {
		t.Fatal(err)
	}
	return id
}

// fakeModel is an OpenAI-compatible httptest server; each request pops the
// next scripted tool-call arguments (the last one repeats).
type fakeModel struct {
	srv          *httptest.Server
	requests     atomic.Int32
	mu           sync.Mutex
	script       []string
	bodies       []string
	delay        time.Duration
	status       int
	finishReason string
}

func newFakeModel(t *testing.T, script ...string) *fakeModel {
	m := &fakeModel{script: script}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(m.requests.Add(1)) - 1
		body, _ := io.ReadAll(r.Body)
		m.mu.Lock()
		m.bodies = append(m.bodies, string(body))
		args := ""
		if len(m.script) > 0 {
			args = m.script[min(n, len(m.script)-1)]
		}
		delay, status, finish := m.delay, m.status, m.finishReason
		if finish == "" {
			finish = "tool_calls"
		}
		m.mu.Unlock()
		if delay > 0 {
			time.Sleep(delay)
		}
		if status != 0 {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":{"message":"scripted failure"}}`))
			return
		}
		quoted, _ := json.Marshal(args)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"c%d","object":"chat.completion","created":1,"model":"fake-digest","choices":[{"index":0,"finish_reason":%q,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_%d","type":"function","function":{"name":"propose_scene_digest","arguments":%s}}]}}],"usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120}}`, n, finish, n, quoted)
	}))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *fakeModel) Chat(ctx context.Context, params openai.ChatCompletionNewParams) (*openai.ChatCompletion, string, error) {
	client := llm.New(llm.Config{BaseURL: m.srv.URL, APIKey: "test", DefaultModel: "fake-digest", MaxRetries: -1})
	params.Model = "fake-digest"
	out, err := client.Chat(ctx, params)
	return out, "fake/fake-digest", err
}

func (m *fakeModel) lastBody() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.bodies) == 0 {
		return ""
	}
	return m.bodies[len(m.bodies)-1]
}

type testEnv struct {
	pool       *pgxpool.Pool
	handle     string
	transcript *memTranscript
	facts      *pgFacts
	model      *fakeModel
	writer     *Writer
	fence      atomic.Value
}

func newEnv(t *testing.T, script ...string) *testEnv {
	pool, handle := digestSchema(t)
	env := &testEnv{pool: pool, handle: handle, transcript: &memTranscript{}, facts: &pgFacts{}, model: newFakeModel(t, script...)}
	env.fence.Store("")
	env.writer = env.newWriter(pool)
	return env
}

func (e *testEnv) newWriter(pool *pgxpool.Pool) *Writer {
	return &Writer{DB: pool, Transcript: e.transcript, Facts: e.facts, Model: e.model,
		Fence: func(context.Context, pgx.Tx, SceneKey) (string, error) { return e.fence.Load().(string), nil },
		Ready: func(context.Context) error { return nil }}
}

func human(id string, at time.Time, name, body string) TranscriptMessage {
	return TranscriptMessage{ProviderMessageID: id, SentAt: at, SenderClass: "human", SenderRef: "dingtalk:org-1:uid:" + name, SenderName: name, Body: body}
}

// observe stores lines in the transcript and marks the scene dirty the way
// M8's insert transaction does.
func (e *testEnv) observe(t *testing.T, key SceneKey, lines ...TranscriptMessage) {
	t.Helper()
	e.transcript.add(key, lines...)
	humans, last := 0, time.Time{}
	for _, l := range lines {
		if l.SenderClass == "human" {
			humans++
			if l.SentAt.After(last) {
				last = l.SentAt
			}
		}
	}
	ctx := context.Background()
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err = MarkSceneDirtyTx(ctx, tx, key, humans, last); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

// makeDue moves the scene's schedule into the past (segment closed).
func (e *testEnv) makeDue(t *testing.T, key SceneKey) {
	t.Helper()
	if _, err := e.pool.Exec(context.Background(), `UPDATE employee_scene_digest_state SET due_at=now()-interval '1 second', hold_until=NULL WHERE scene_id=$1::uuid`, key.SceneID); err != nil {
		t.Fatal(err)
	}
}

type stateRow struct {
	Pending, NoProgress, BudgetCalls int
	CursorID, BlockedReason          string
	Blocked, Leased                  bool
	DueAt                            *time.Time
	LastRunID                        string
}

func (e *testEnv) state(t *testing.T, key SceneKey) stateRow {
	t.Helper()
	var s stateRow
	if err := e.pool.QueryRow(context.Background(), `SELECT pending_human,no_progress_count,budget_calls,cursor_message_id,blocked_reason,blocked_at IS NOT NULL,lease_token IS NOT NULL,due_at,COALESCE(last_run_id::text,'') FROM employee_scene_digest_state WHERE scene_id=$1::uuid`, key.SceneID).
		Scan(&s.Pending, &s.NoProgress, &s.BudgetCalls, &s.CursorID, &s.BlockedReason, &s.Blocked, &s.Leased, &s.DueAt, &s.LastRunID); err != nil {
		t.Fatal(err)
	}
	return s
}

type runInfo struct {
	ID, Outcome, Error string
	Calls, Accepted    int
	Rejected           string
}

func (e *testEnv) runs(t *testing.T, key SceneKey) []runInfo {
	t.Helper()
	rows, err := e.pool.Query(context.Background(), `SELECT id::text,COALESCE(outcome,''),error,calls,ops_accepted,ops_rejected::text FROM employee_scene_digest_run WHERE scene_id=$1::uuid ORDER BY started_at,id`, key.SceneID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []runInfo
	for rows.Next() {
		var r runInfo
		if err = rows.Scan(&r.ID, &r.Outcome, &r.Error, &r.Calls, &r.Accepted, &r.Rejected); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func (e *testEnv) activeFacts(t *testing.T, key SceneKey) []Fact {
	t.Helper()
	tx, err := e.pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	facts, err := e.facts.ActiveFacts(context.Background(), tx, key, 200)
	if err != nil {
		t.Fatal(err)
	}
	return facts
}

func ops(list ...map[string]string) string {
	if list == nil {
		list = []map[string]string{}
	}
	raw, _ := json.Marshal(map[string]any{"ops": list})
	return string(raw)
}

func mustProcess(t *testing.T, w *Writer) bool {
	t.Helper()
	worked, err := w.ProcessNext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return worked
}
