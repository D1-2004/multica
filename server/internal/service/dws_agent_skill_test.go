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

func TestLoadAgentExecutionSkillsFollowsRuntimeDWSCapability(t *testing.T) {
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
	var nonDWSRuntimeID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, name, runtime_mode, provider, status,
			device_info, metadata, visibility, owner_id
		)
		VALUES ($1, 'FC Hermes', 'cloud', 'hermes', 'online',
			'test runtime', '{"kind":"fc-e2b","capabilities":["hermes"]}'::jsonb, 'private', $2)
		RETURNING id
	`, workspaceID, userID).Scan(&nonDWSRuntimeID); err != nil {
		t.Fatalf("create non-DWS runtime: %v", err)
	}
	var firstDWSAgentID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, visibility, max_concurrent_tasks, owner_id
		)
		VALUES ($1, 'DWS Agent', '', 'cloud', '{}'::jsonb,
			$2, 'private', 1, $3)
		RETURNING id
	`, workspaceID, dwsRuntimeID, userID).Scan(&firstDWSAgentID); err != nil {
		t.Fatalf("create DWS agent: %v", err)
	}
	var secondDWSAgentID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, visibility, max_concurrent_tasks, owner_id
		)
		VALUES ($1, 'Unbound DWS Agent', '', 'cloud', '{}'::jsonb, $2, 'private', 1, $3)
		RETURNING id
	`, workspaceID, dwsRuntimeID, userID).Scan(&secondDWSAgentID); err != nil {
		t.Fatalf("create unbound DWS agent: %v", err)
	}
	var nonDWSAgentID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, visibility, max_concurrent_tasks, owner_id
		)
		VALUES ($1, 'Non-DWS Agent', '', 'cloud', '{}'::jsonb, $2, 'private', 1, $3)
		RETURNING id
	`, workspaceID, nonDWSRuntimeID, userID).Scan(&nonDWSAgentID); err != nil {
		t.Fatalf("create non-DWS agent: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		pool.Exec(cleanupCtx, `DELETE FROM agent WHERE id IN ($1, $2, $3)`, firstDWSAgentID, secondDWSAgentID, nonDWSAgentID)
		pool.Exec(cleanupCtx, `DELETE FROM agent_runtime WHERE id IN ($1, $2)`, dwsRuntimeID, nonDWSRuntimeID)
		pool.Exec(cleanupCtx, `DELETE FROM member WHERE workspace_id = $1 AND user_id = $2`, workspaceID, userID)
		pool.Exec(cleanupCtx, `DELETE FROM workspace WHERE id = $1`, workspaceID)
		pool.Exec(cleanupCtx, `DELETE FROM "user" WHERE id = $1`, userID)
	})

	firstSkills := svc.LoadAgentExecutionSkills(ctx, util.MustParseUUID(firstDWSAgentID))
	if !hasSkillName(firstSkills, "multica-dws") {
		t.Fatalf("first DWS runtime agent skills missing multica-dws: %#v", skillNames(firstSkills))
	}
	secondSkills := svc.LoadAgentExecutionSkills(ctx, util.MustParseUUID(secondDWSAgentID))
	if !hasSkillName(secondSkills, "multica-dws") {
		t.Fatalf("second DWS runtime agent skills missing multica-dws: %#v", skillNames(secondSkills))
	}
	nonDWSSkills := svc.LoadAgentExecutionSkills(ctx, util.MustParseUUID(nonDWSAgentID))
	if hasSkillName(nonDWSSkills, "multica-dws") {
		t.Fatalf("non-DWS runtime agent unexpectedly received multica-dws: %#v", skillNames(nonDWSSkills))
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
