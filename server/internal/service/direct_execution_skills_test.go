package service

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestDirectExecutionSkillsKeepBindingsAndDWS(t *testing.T) {
	f := directDatabase(t)
	ctx := context.Background()
	aid := util.MustParseUUID(f.request.Task.Scope.AgentID)
	ws := util.MustParseUUID(f.request.Task.Scope.WorkspaceID)
	bound, scope := uuid.NewString(), uuid.NewString()
	for _, id := range []string{bound, scope} {
		if _, err := f.pool.Exec(ctx, `INSERT INTO skill(id,workspace_id,name,description,content) VALUES($1::uuid,$2,$3,'','USER_SKILL_SENTINEL')`, id, ws, "multica-user-"+id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO agent_skill(agent_id,skill_id) VALUES($1,$2::uuid)`, aid, bound); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(ctx, `DELETE FROM agent_skill WHERE agent_id=$1`, aid)
		_, _ = f.pool.Exec(ctx, `DELETE FROM skill WHERE id IN ($1::uuid,$2::uuid)`, bound, scope)
	})
	runtime := db.AgentRuntime{WorkspaceID: ws, RuntimeMode: "cloud", Provider: "hermes", Metadata: []byte(`{"kind":"fc-e2b","provider":"hermes","capabilities":["hermes","dws"]}`)}
	extra := []pgtype.UUID{util.MustParseUUID(scope)}
	policy := &protocol.DingTalkMessagePolicy{PlatformManagedLifecycle: true}
	skills := f.service.LoadTaskExecutionSkills(ctx, aid, extra, runtime, SandboxBackendAliyunFC, TaskExecutionSurfaceDirect, policy)
	if len(skills) != 3 {
		t.Errorf("Direct skills = %v; want bound, scope and DWS only", skillNames(skills))
	}
	for _, name := range []string{"multica-user-" + bound, "multica-user-" + scope, "multica-dws"} {
		if !hasSkillName(skills, name) {
			t.Errorf("missing %s", name)
		}
	}
	for _, skill := range skills {
		if skill.Name == "multica-dws" && (!strings.Contains(skill.Content, "Do not bypass the wrapper") || strings.Contains(skill.Content, "--ai-tag=false")) {
			t.Error("Direct lost DWS identity/policy contract")
		}
	}
	bundles, refs := f.service.LoadTaskSkillBundles(ctx, aid, extra, runtime, SandboxBackendAliyunFC, TaskExecutionSurfaceDirect, policy)
	wantBundles, wantRefs := BuildAgentSkillBundles(skills)
	if !reflect.DeepEqual(bundles, wantBundles) || !reflect.DeepEqual(refs, wantRefs) {
		t.Error("inline/bundle assembly differs")
	}
	ordinary := f.service.LoadAgentExecutionSkills(ctx, aid, runtime, SandboxBackendAliyunFC, policy)
	for _, builtin := range f.service.BuiltinSkills() {
		if !hasSkillName(ordinary, builtin.Name) {
			t.Errorf("ordinary lost %s", builtin.Name)
		}
	}
}
