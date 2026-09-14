package service

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Run only against an explicitly selected preproduction database after the
// normal release migrator installed 9239. Temporary tables shadow data while
// exercising the actual installed guard functions and production lookup code.
func TestStableProviderScopeDatabaseGuards(t *testing.T) {
	url := os.Getenv("DSH_STABLE_SCOPE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("requires an explicitly selected migrated preproduction database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal("connect to preproduction test database")
	}
	defer conn.Close(context.Background())
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	_, err = tx.Exec(ctx, `
 CREATE TEMP TABLE fc_e2b_stable_release (id uuid,provider_scope text,sandbox_backend text) ON COMMIT DROP;
 CREATE TEMP TABLE fc_e2b_stable_channel (sandbox_backend text,channel text,artifact_kind text,
 current_artifact_ref text,current_artifact_build_id text,current_artifact_digest text,
 current_template_id text,current_template_alias text,current_release_id uuid,active_release_id uuid) ON COMMIT DROP;
 CREATE TEMP TABLE fc_e2b_stable_release_target (release_id uuid,provider text,sandbox_backend text) ON COMMIT DROP;
 CREATE TEMP TABLE agent_runtime (runtime_mode text,provider text,metadata jsonb) ON COMMIT DROP;
 CREATE TRIGGER scope_target BEFORE INSERT OR UPDATE ON fc_e2b_stable_release_target
 FOR EACH ROW EXECUTE FUNCTION public.check_fc_stable_target_scope();
 CREATE TRIGGER scope_channel BEFORE INSERT OR UPDATE ON fc_e2b_stable_channel
 FOR EACH ROW EXECUTE FUNCTION public.check_fc_stable_channel_scope();
 CREATE TRIGGER scope_runtime BEFORE INSERT ON agent_runtime
 FOR EACH ROW EXECUTE FUNCTION public.check_fc_stable_runtime_creation_scope();
 INSERT INTO fc_e2b_stable_release VALUES
 ('10000000-0000-4000-8000-000000000001','','aliyun_fc'),
 ('10000000-0000-4000-8000-000000000002','dsh','aliyun_fc');
 INSERT INTO fc_e2b_stable_channel VALUES
 ('aliyun_fc','stable','e2b_template','old','','','old','old','10000000-0000-4000-8000-000000000001',NULL),
 ('aliyun_fc','stable:dsh','e2b_template','old','','','old','old','10000000-0000-4000-8000-000000000001','10000000-0000-4000-8000-000000000002');
 `)
	if err != nil {
		t.Fatalf("prepare temporary scope fixtures: %v", err)
	}
	for _, item := range []struct {
		name, sql string
		denied    bool
	}{
		{"scoped target", `INSERT INTO fc_e2b_stable_release_target VALUES ('10000000-0000-4000-8000-000000000002','dsh','aliyun_fc')`, false},
		{"legacy widened target", `INSERT INTO fc_e2b_stable_release_target VALUES ('10000000-0000-4000-8000-000000000002','hermes','aliyun_fc')`, true},
		{"shared target", `INSERT INTO fc_e2b_stable_release_target VALUES ('10000000-0000-4000-8000-000000000001','hermes','aliyun_fc')`, false},
		{"shared overwrite of independent provider", `INSERT INTO fc_e2b_stable_release_target VALUES ('10000000-0000-4000-8000-000000000001','dsh','aliyun_fc')`, true},
		{"wrong active channel", `UPDATE fc_e2b_stable_channel SET active_release_id='10000000-0000-4000-8000-000000000002' WHERE channel='stable'`, true},
		{"wrong published channel", `UPDATE fc_e2b_stable_channel SET current_release_id='10000000-0000-4000-8000-000000000002' WHERE channel='stable'`, true},
	} {
		t.Run(item.name, func(t *testing.T) {
			nested, err := tx.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer nested.Rollback(ctx)
			_, err = nested.Exec(ctx, item.sql)
			if item.denied {
				if err == nil {
					t.Fatal("scope violation accepted")
				}
				state, ok := err.(interface{ SQLState() string })
				if !ok || state.SQLState() != "23514" {
					t.Fatalf("unexpected error: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
	_, err = tx.Exec(ctx, `UPDATE fc_e2b_stable_channel SET current_artifact_ref='new',current_template_id='new',current_template_alias='new',current_release_id='10000000-0000-4000-8000-000000000002',active_release_id=NULL WHERE channel='stable:dsh'`)
	if err != nil {
		t.Fatal(err)
	}
	service := &FCE2BStableService{}
	for _, provider := range []string{"dsh", "hermes", "codex"} {
		binding, err := service.LockCurrentArtifactForRuntimeCreation(ctx, tx, SandboxBackendAliyunFC, provider)
		want := "old"
		if provider == "dsh" {
			want = "new"
		}
		if err != nil || binding.TemplateID != want {
			t.Fatalf("provider=%s binding=%s err=%v", provider, binding.TemplateID, err)
		}
	}
	for _, item := range []struct {
		provider, artifact, channel string
		denied                      bool
	}{
		{"dsh", "old", "stable", true}, {"dsh", "new", "stable", false}, {"hermes", "old", "stable", false}, {"dsh", "old", "candidate", false},
	} {
		nested, err := tx.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		_, err = nested.Exec(ctx, `INSERT INTO agent_runtime VALUES ('cloud',$1,jsonb_build_object('kind','fc-e2b','template_id',$2::text,'template_channel',$3::text))`, item.provider, item.artifact, item.channel)
		_ = nested.Rollback(ctx)
		if item.denied {
			if err == nil {
				t.Fatal("old shared artifact accepted for independent provider")
			}
		} else if err != nil {
			t.Fatal(err)
		}
	}
}
