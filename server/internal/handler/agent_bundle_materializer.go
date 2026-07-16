package handler

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// materializeAgentBundleInTx owns the common atomic boundary for Agent
// snapshots: create the Agent, persist invocation access, attach manually
// selected skills, then let the caller materialize the Bundle-owned skills.
// Direct Git Agents add their source ledger in the callback; template Agents
// create independent skill rows and deliberately add no source relation.
func materializeAgentBundleInTx(
	ctx context.Context,
	queries *db.Queries,
	params db.CreateAgentParams,
	permission resolvedPermission,
	manualSkills []pgtype.UUID,
	writeBundleSkills func(db.Agent) error,
) (db.Agent, error) {
	created, err := queries.CreateAgent(ctx, params)
	if err != nil {
		return db.Agent{}, err
	}
	if err := replaceInvocationTargetsWithQueries(ctx, queries, created.ID, params.OwnerID, permission.targets); err != nil {
		return db.Agent{}, err
	}
	for _, skillID := range manualSkills {
		if err := queries.AddAgentSkill(ctx, db.AddAgentSkillParams{AgentID: created.ID, SkillID: skillID}); err != nil {
			return db.Agent{}, err
		}
	}
	if writeBundleSkills != nil {
		if err := writeBundleSkills(created); err != nil {
			return db.Agent{}, err
		}
	}
	return created, nil
}
