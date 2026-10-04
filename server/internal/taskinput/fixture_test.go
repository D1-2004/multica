package taskinput

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	testOrg       = "org-a"
	requesterRef  = "dingtalk:org-a:requester"
	participantC  = "dingtalk:org-a:staff-c"
	participantD  = "dingtalk:org-a:staff-d"
	participantDE = "dingtalk:org-a:deap-lin"
)

type fixture struct {
	pool  *pgxpool.Pool
	store *Store
	tasks *employeetask.Store
	scope Scope
	// Directory scenes of the same agent and tenant.
	origin, dmC, dmD, group, dmDE string
}

// database applies the real migrations into an isolated schema. Concurrent
// cases use separate PostgreSQL connections from the pool.
func database(t *testing.T) fixture {
	t.Helper()
	ctx := context.Background()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL required for taskinput PostgreSQL integration tests")
	}
	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	schema := "taskinput_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = admin.Exec(ctx, `CREATE SCHEMA `+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), `DROP SCHEMA `+pgx.Identifier{schema}.Sanitize()+` CASCADE`); err != nil {
			t.Error(err)
		}
		_ = admin.Close(context.Background())
	})
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	config.MaxConns = 12
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	_, self, _, _ := runtime.Caller(0)
	dir := filepath.Join(filepath.Dir(self), "..", "..", "migrations")
	for _, name := range []string{"9500_agent_scene.up.sql", "9501_agent_scene_id_idx.up.sql", "9502_agent_scene_locator_idx.up.sql", "9511_agent_scene_kind_source.up.sql"} {
		applyMigration(t, pool, filepath.Join(dir, name))
	}
	for _, pattern := range []string{"960*.up.sql", "9650_*.up.sql", "9760_*.up.sql", "990*.up.sql", "9800_*.up.sql", "992*.up.sql"} {
		paths, err := filepath.Glob(filepath.Join(dir, pattern))
		if err != nil || len(paths) == 0 {
			t.Fatalf("migrations %s: %v %v", pattern, paths, err)
		}
		sort.Strings(paths)
		for _, path := range paths {
			applyMigration(t, pool, path)
		}
	}
	// The production workspace row is the parent lock shared with teardown.
	if _, err := pool.Exec(ctx, `CREATE TABLE workspace (id uuid NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	ws, agent := uuid.NewString(), uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO workspace(id) VALUES($1::uuid)`, ws); err != nil {
		t.Fatal(err)
	}
	f := fixture{pool: pool, store: NewStore(pool), tasks: employeetask.NewStore(pool), scope: Scope{WorkspaceID: ws, AgentID: agent, TenantOrgID: testOrg}}
	f.origin = f.resolveScene(t, scene.KindGroup, "cid-origin")
	f.dmC = f.resolveScene(t, scene.KindDM, "cid-dm-c")
	f.dmD = f.resolveScene(t, scene.KindDM, "cid-dm-d")
	f.group = f.resolveScene(t, scene.KindGroup, "cid-group-b")
	f.dmDE = f.resolveScene(t, scene.KindDM, "cid-dm-de")
	return f
}

func applyMigration(t *testing.T, pool *pgxpool.Pool, path string) {
	t.Helper()
	sql, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(context.Background(), string(sql)); err != nil {
		t.Fatalf("apply %s: %v", filepath.Base(path), err)
	}
}

func (f fixture) resolveScene(t *testing.T, kind, cid string) string {
	t.Helper()
	wsID, err := util.ParseUUID(f.scope.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	agentID, err := util.ParseUUID(f.scope.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	row, err := scene.Resolve(context.Background(), db.New(f.pool), scene.Owner{WorkspaceID: wsID, AgentID: agentID},
		scene.DingTalkConversation(testOrg, kind, cid+"-"+uuid.NewString()), scene.Observation{KindStated: true})
	if err != nil {
		t.Fatal(err)
	}
	return scene.RefOf(row).SceneID
}

func (f fixture) taskScope() employeetask.Scope {
	return employeetask.Scope{WorkspaceID: f.scope.WorkspaceID, AgentID: f.scope.AgentID, TenantOrgID: testOrg, Kind: employeetask.ScopeScene, Scene: scene.Ref{SceneID: f.origin}}
}

// newTask creates an EmployeeLoop Task in the origin scene.
func (f fixture) newTask(t *testing.T, goal string) employeetask.Task {
	t.Helper()
	task, err := f.tasks.Create(context.Background(), employeetask.CreateParams{Scope: f.taskScope(), OwnerLoop: employeetask.LoopEmployee,
		DispatchMode: employeetask.DispatchDirect, RequesterRef: requesterRef, Definition: employeetask.Definition{Goal: goal},
		Source: employeetask.Source{Namespace: "dispatch", Key: "task/" + uuid.NewString()}, Input: goal})
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func requesterAuthority(sceneID string) Authority {
	return Authority{ActorRef: requesterRef, SceneID: sceneID, ReceiptRef: "receipt/origin/" + uuid.NewString(), VerifiedAt: time.Now()}
}

func spec(sceneID, participant, question string) InvitationSpec {
	return InvitationSpec{TargetSceneID: sceneID, ParticipantRef: participant, Question: question}
}

func (f fixture) createParams(task employeetask.Task, specs ...InvitationSpec) CreateCollectionParams {
	return CreateCollectionParams{TaskID: task.ID, OriginSceneID: f.origin, AuthorityRef: "task-authority/" + task.ID,
		RequesterRef: requesterRef, DeliveryAnchorRef: "origin-message/1", GoalRevision: task.GoalRevision, Invitations: specs,
		Source: Source{Namespace: "employee.tool", Key: "create_collection/" + task.ID}, Authority: requesterAuthority(f.origin)}
}

func (f fixture) create(t *testing.T, task employeetask.Task, specs ...InvitationSpec) (Collection, []Invitation) {
	t.Helper()
	col, invitations, err := f.store.CreateCollectionTx(context.Background(), f.scope, f.createParams(task, specs...))
	if err != nil {
		t.Fatal(err)
	}
	return col, invitations
}

func renderedHash(text string) string {
	return CheckEgress(EgressInput{Rendered: text, Audience: AudienceDirect, TargetSceneKind: "dm"}).RenderedHash
}

// deliver records a provider send; the invitation message id is "msg-<id>".
func (f fixture) deliver(t *testing.T, inv Invitation) Invitation {
	t.Helper()
	out, err := f.store.RecordInviteDeliveryTx(context.Background(), f.scope, RecordDeliveryParams{InvitationID: inv.ID, ActionID: inv.DeliveryActionID,
		Outcome: DeliverySent, ProviderMessageID: "msg-" + inv.ID, RenderedHash: renderedHash(inv.Question)})
	if err != nil {
		t.Fatal(err)
	}
	if out.State != InvitationDelivered {
		t.Fatalf("delivered state: %+v", out)
	}
	return out
}

func (f fixture) deliverAll(t *testing.T, invitations []Invitation) []Invitation {
	out := make([]Invitation, len(invitations))
	for i, inv := range invitations {
		out[i] = f.deliver(t, inv)
	}
	return out
}

// answer builds Host-verified answer params for inv from its own participant.
func answer(col Collection, inv Invitation, sourceKey, body string) AcceptInputParams {
	binding := BindDMSinglePending
	if inv.TargetSceneKind == "group" {
		binding = BindReplyChain
	}
	return AcceptInputParams{CollectionID: col.ID, InvitationID: inv.ID, Source: Source{Namespace: "dingtalk.message", Key: sourceKey},
		Authority:  Authority{ActorRef: inv.ParticipantRef, SceneID: inv.TargetSceneID, ReceiptRef: "receipt/" + sourceKey, VerifiedAt: time.Now()},
		SenderKind: SenderPerson, MessageKind: MessageText, Binding: binding, ProviderMessageID: sourceKey,
		// Host occurred_at is frozen from the receipt, so a redelivery repeats it.
		OccurredAt: inv.CreatedAt.Add(time.Second).UTC(), Body: body, ExpectedRevision: col.Revision}
}

// accept submits p at the collection's current revision, re-reading on a
// concurrent revision change exactly as a Host re-binds.
func (f fixture) accept(t *testing.T, p AcceptInputParams) (AcceptResult, error) {
	t.Helper()
	for range 32 {
		current, err := f.store.GetCollection(context.Background(), f.scope, p.CollectionID)
		if err != nil {
			return AcceptResult{}, err
		}
		p.ExpectedRevision = current.Revision
		result, err := f.store.AcceptInputTx(context.Background(), f.scope, p)
		// Only a stale revision is retried; a source conflict or a
		// now-ambiguous binding is returned to the caller.
		if errors.Is(err, ErrStaleRevision) {
			continue
		}
		return result, err
	}
	t.Fatal("accept: revision kept moving")
	return AcceptResult{}, nil
}

func (f fixture) mustAccept(t *testing.T, p AcceptInputParams) AcceptResult {
	t.Helper()
	result, err := f.accept(t, p)
	if err != nil {
		t.Fatalf("accept %s: %v", p.Source.Key, err)
	}
	return result
}

func (f fixture) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
