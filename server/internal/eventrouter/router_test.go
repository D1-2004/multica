package eventrouter

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func fixtureEvent() Event {
	return Event{Version: Version, ID: "event-1", Source: "provider/verified-endpoint", Type: "im.message",
		Category: UserMessage, PayloadSchema: "provider/1", Payload: json.RawMessage(`{"author":"actor-a","text":"hello"}`)}
}

func TestEventVersionAndPayloadBoundary(t *testing.T) {
	for _, category := range []string{UserMessage, Observation, Control, RunCallback, Wake} {
		e := fixtureEvent()
		e.Category = category
		if err := e.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	for name, mutate := range map[string]func(*Event){
		"major": func(e *Event) { e.Version = 2 }, "category": func(e *Event) { e.Category = "authorized" },
		"missing_id": func(e *Event) { e.ID = "" }, "missing_source": func(e *Event) { e.Source = "" },
		"malformed": func(e *Event) { e.Payload = []byte(`{`) },
		"oversized": func(e *Event) { e.Payload = []byte(`"` + strings.Repeat("a", MaxPayloadBytes) + `"`) },
	} {
		t.Run(name, func(t *testing.T) {
			e := fixtureEvent()
			mutate(&e)
			if !errors.Is(e.Validate(), ErrInvalidEvent) {
				t.Fatal("accepted invalid event")
			}
		})
	}
}

func database(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL required for receipt integration tests")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	return pool
}

func fixtureHost(t *testing.T, pool *pgxpool.Pool) Host {
	t.Helper()
	ws, _ := util.ParseUUID(uuid.NewString())
	agent, _ := util.ParseUUID(uuid.NewString())
	principal, _ := util.ParseUUID(uuid.NewString())
	h := Host{Owner: scene.Owner{WorkspaceID: ws, AgentID: agent}, PrincipalID: principal, TenantOrgID: "org-a",
		Locator:     scene.DingTalkConversation("org-a", scene.KindGroup, "cid-"+uuid.NewString()),
		Observation: scene.Observation{KindStated: true, ActiveAt: time.Now()}, Route: Unified, ConfigVersion: "sha:v1", Fingerprint: "sha:payload1"}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM scene_event_receipt WHERE workspace_id=$1`, ws)
		_, _ = pool.Exec(context.Background(), `DELETE FROM agent_scene WHERE workspace_id=$1`, ws)
	})
	return h
}

func TestAdmissionConcurrentReplayDatabase(t *testing.T) {
	p := database(t)
	h := fixtureHost(t, p)
	e := fixtureEvent()
	var wg sync.WaitGroup
	ids := make(chan string, 16)
	errs := make(chan error, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			row, _, err := Admit(context.Background(), p, e, h)
			if err != nil {
				errs <- err
				return
			}
			ids <- util.UUIDToString(row.ID)
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	id := ""
	for got := range ids {
		if id != "" && got != id {
			t.Fatal("multiple receipts")
		}
		id = got
	}
	h.Route = Legacy
	h.ConfigVersion = "sha:v2"
	h.Locator.ExternalID = "cid-other"
	row, replay, err := Admit(context.Background(), p, e, h)
	if err != nil || !replay || row.Route != Unified || row.ConfigVersion != "sha:v1" || util.UUIDToString(row.ID) != id {
		t.Fatalf("sticky replay: %+v, %v", row, err)
	}
	h.Fingerprint = "sha:different"
	if _, _, err := Admit(context.Background(), p, e, h); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed payload: %v", err)
	}
}

func TestAdmissionUnmappedAndAtomicRollbackDatabase(t *testing.T) {
	p := database(t)
	h := fixtureHost(t, p)
	e := fixtureEvent()
	h.Locator.Kind = ""
	row, _, err := Admit(context.Background(), p, e, h)
	if err != nil || row.State != Unmapped || row.SceneID.Valid || row.Reason != "unknown_kind" {
		t.Fatalf("unknown kind: %+v, %v", row, err)
	}
	h.Locator.Kind = scene.KindGroup
	replayed, replay, err := Admit(context.Background(), p, e, h)
	if err != nil || !replay || replayed.SceneID.Valid {
		t.Fatalf("implicitly remapped held receipt: %+v, %v", replayed, err)
	}
	h2 := fixtureHost(t, p)
	h2.ConfigVersion = "invalid\x00text"
	e.ID = "insert-failure"
	if _, _, err := Admit(context.Background(), p, e, h2); err == nil {
		t.Fatal("expected SQL failure")
	}
	if _, err := scene.Lookup(context.Background(), db.New(p), h2.Owner, h2.Locator); !errors.Is(err, scene.ErrNotFound) {
		t.Fatalf("scene survived failed receipt: %v", err)
	}
}

func TestAdmissionOwnerTenantKindIsolationDatabase(t *testing.T) {
	p := database(t)
	h := fixtureHost(t, p)
	e := fixtureEvent()
	row, _, err := Admit(context.Background(), p, e, h)
	if err != nil {
		t.Fatal(err)
	}
	h.TenantOrgID = "org-b"
	if _, _, err := Admit(context.Background(), p, e, h); !errors.Is(err, scene.ErrStaleTenant) {
		t.Fatalf("tenant rebound replay: %v", err)
	}
	h.TenantOrgID = "org-a"
	h.PrincipalID, _ = util.ParseUUID(uuid.NewString())
	if _, _, err := Admit(context.Background(), p, e, h); !errors.Is(err, ErrConflict) {
		t.Fatalf("principal changed: %v", err)
	}
	other := fixtureHost(t, p)
	other.Locator = h.Locator
	row2, _, err := Admit(context.Background(), p, e, other)
	if err != nil || row2.SceneID == row.SceneID || row2.ID == row.ID {
		t.Fatalf("owner leak: %+v, %v", row2, err)
	}
	h.PrincipalID = row.PrincipalID
	h.Locator.Kind = scene.KindDM
	e.ID = "other-kind"
	conflict, _, err := Admit(context.Background(), p, e, h)
	if err != nil || conflict.State != Unmapped || conflict.Reason != "kind_conflict" {
		t.Fatalf("kind conflict: %+v, %v", conflict, err)
	}
	e.ID = "orgless"
	h.TenantOrgID = ""
	h.Locator.TenantOrgID = ""
	missing, _, err := Admit(context.Background(), p, e, h)
	if err != nil || missing.State != Unmapped || missing.Reason != "missing_locator" {
		t.Fatalf("org missing: %+v, %v", missing, err)
	}
}
