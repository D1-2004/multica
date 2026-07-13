package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestLoadAgentExecutionSkillsAddsDWSOnlyForBoundProfile(t *testing.T) {
	ctx := context.Background()
	pool := newTaskClaimRacePool(t)
	queries := db.New(pool)
	svc := NewTaskService(queries, pool, nil, events.New())

	suffix := time.Now().UnixNano()
	var userID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO "user" (name, email)
		VALUES ($1, $2)
		RETURNING id
	`, "DWS Skill Test", fmt.Sprintf("dws-skill-%d@multica.test", suffix)).Scan(&userID); err != nil {
		t.Fatalf("create user: %v", err)
	}
	var workspaceID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description, issue_prefix)
		VALUES ($1, $2, '', 'DST')
		RETURNING id
	`, "DWS Skill Test", fmt.Sprintf("dws-skill-%d", suffix)).Scan(&workspaceID); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO member (workspace_id, user_id, role)
		VALUES ($1, $2, 'owner')
	`, workspaceID, userID); err != nil {
		t.Fatalf("create member: %v", err)
	}

	var dwsRuntimeID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, name, runtime_mode, provider, status,
			device_info, metadata, visibility, owner_id
		)
		VALUES ($1, 'FC Hermes DWS', 'cloud', 'hermes', 'online',
			'test runtime', '{"kind":"fc-e2b","capabilities":["hermes","dws"]}'::jsonb, 'private', $2)
		RETURNING id
	`, workspaceID, userID).Scan(&dwsRuntimeID); err != nil {
		t.Fatalf("create DWS runtime: %v", err)
	}
	var boundAgentID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, visibility, max_concurrent_tasks, owner_id
		)
		VALUES ($1, 'DWS Agent', '', 'cloud',
			'{"fc_e2b":{"dws_profile_id":"11111111-1111-1111-1111-111111111111"}}'::jsonb,
			$2, 'private', 1, $3)
		RETURNING id
	`, workspaceID, dwsRuntimeID, userID).Scan(&boundAgentID); err != nil {
		t.Fatalf("create DWS agent: %v", err)
	}
	var unboundAgentID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, visibility, max_concurrent_tasks, owner_id
		)
		VALUES ($1, 'Unbound DWS Agent', '', 'cloud', '{}'::jsonb, $2, 'private', 1, $3)
		RETURNING id
	`, workspaceID, dwsRuntimeID, userID).Scan(&unboundAgentID); err != nil {
		t.Fatalf("create unbound DWS agent: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		pool.Exec(cleanupCtx, `DELETE FROM agent WHERE id IN ($1, $2)`, boundAgentID, unboundAgentID)
		pool.Exec(cleanupCtx, `DELETE FROM agent_runtime WHERE id = $1`, dwsRuntimeID)
		pool.Exec(cleanupCtx, `DELETE FROM member WHERE workspace_id = $1 AND user_id = $2`, workspaceID, userID)
		pool.Exec(cleanupCtx, `DELETE FROM workspace WHERE id = $1`, workspaceID)
		pool.Exec(cleanupCtx, `DELETE FROM "user" WHERE id = $1`, userID)
	})

	dwsSkills := svc.LoadAgentExecutionSkills(ctx, util.MustParseUUID(boundAgentID))
	if !hasSkillName(dwsSkills, "multica-dws") {
		t.Fatalf("DWS-bound agent execution skills missing multica-dws: %#v", skillNames(dwsSkills))
	}
	unboundSkills := svc.LoadAgentExecutionSkills(ctx, util.MustParseUUID(unboundAgentID))
	if hasSkillName(unboundSkills, "multica-dws") {
		t.Fatalf("DWS-capable runtime without a profile unexpectedly received multica-dws: %#v", skillNames(unboundSkills))
	}
}

func hasSkillName(skills []AgentSkillData, name string) bool {
	for _, skill := range skills {
		if skill.Name == name {
			return true
		}
	}
	return false
}

func skillNames(skills []AgentSkillData) []string {
	names := make([]string, 0, len(skills))
	for _, skill := range skills {
		names = append(names, skill.Name)
	}
	return names
}
