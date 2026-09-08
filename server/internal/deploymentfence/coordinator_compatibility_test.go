package deploymentfence

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/service/inboundcoord"
)

// The real query runs against an isolated copy of the heartbeat table. Neither
// the global fence row nor another process's acknowledgements are changed.
func TestCoordinatorReplicaCompatibilityUsesEveryLiveHeartbeat(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("requires an explicitly configured isolated PostgreSQL database")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if err := admin.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	schema := "compat_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Errorf("cleanup isolated compatibility schema: %v", err)
		}
	}()
	if _, err := admin.Exec(ctx, "CREATE TABLE "+quoted+".deployment_fence_replica_ack (LIKE public.deployment_fence_replica_ack INCLUDING ALL)"); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	svc := &Service{pool: pool, liveWindow: 15 * time.Second}
	svc.snapshot.Store(&Snapshot{State: StateNormal, Revision: 1})
	for _, tc := range []struct {
		name       string
		builds     []string
		expiredOld bool
		want       bool
	}{
		{name: "no live replicas", want: false},
		{name: "mixed old and new", builds: []string{"old-build", "new-build " + inboundcoord.ReplicaPlanMarker}, want: false},
		{name: "every live replica supports plan", builds: []string{"build-a " + inboundcoord.ReplicaPlanMarker, "build-b " + inboundcoord.ReplicaPlanMarker}, want: true},
		{name: "expired old replica is ignored", builds: []string{"new-build " + inboundcoord.ReplicaPlanMarker}, expiredOld: true, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := pool.Exec(ctx, "TRUNCATE deployment_fence_replica_ack"); err != nil {
				t.Fatal(err)
			}
			for i, build := range tc.builds {
				if _, err := pool.Exec(ctx, `INSERT INTO deployment_fence_replica_ack(instance_id,build_id,state,revision,last_seen_at) VALUES($1,$2,'normal',1,now())`, uuid.NewString()+string(rune('a'+i)), build); err != nil {
					t.Fatal(err)
				}
			}
			if tc.expiredOld {
				if _, err := pool.Exec(ctx, `INSERT INTO deployment_fence_replica_ack(instance_id,build_id,state,revision,last_seen_at) VALUES('expired','old-build','normal',1,now()-interval '1 minute')`); err != nil {
					t.Fatal(err)
				}
			}
			got, err := svc.AllLiveReplicasSupport(ctx, inboundcoord.ReplicaPlanMarker)
			if err != nil || got != tc.want {
				t.Fatalf("ready=%v want=%v err=%v", got, tc.want, err)
			}
		})
	}
}
