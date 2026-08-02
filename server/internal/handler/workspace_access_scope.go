package handler

import (
	"context"
	"net/http"

	"github.com/multica-ai/multica/server/internal/middleware"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func workspaceAccessCanUseAgent(ctx context.Context, agent db.Agent) (bool, bool) {
	principal, isToken := middleware.WorkspaceAccessPrincipalFromContext(ctx)
	if !isToken {
		return false, false
	}
	if uuidToString(agent.WorkspaceID) != principal.WorkspaceID {
		return false, true
	}
	return uuidToString(agent.OwnerID) == principal.UserID, true
}

func workspaceAccessCanManageSkill(ctx context.Context, skill db.Skill) (bool, bool) {
	principal, isToken := middleware.WorkspaceAccessPrincipalFromContext(ctx)
	if !isToken {
		return false, false
	}
	if uuidToString(skill.WorkspaceID) != principal.WorkspaceID {
		return false, true
	}
	return skill.CreatedBy.Valid && uuidToString(skill.CreatedBy) == principal.UserID, true
}

func requireWorkspaceAccessAgent(w http.ResponseWriter, r *http.Request, agent db.Agent) bool {
	allowed, isToken := workspaceAccessCanUseAgent(r.Context(), agent)
	if !isToken {
		return true
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "workspace_access_resource_not_allowed")
		return false
	}
	return true
}

func canUseRuntimeForRequest(r *http.Request, member db.Member, runtime db.AgentRuntime) bool {
	if principal, isToken := middleware.WorkspaceAccessPrincipalFromContext(r.Context()); isToken {
		if uuidToString(runtime.WorkspaceID) != principal.WorkspaceID {
			return false
		}
	}
	return canUseRuntimeForAgent(member, runtime)
}
