package handler

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/integrations/agentmessagerouter"
	"github.com/multica-ai/multica/server/internal/integrations/dingtalk"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type dingTalkAgentMetadataStore interface {
	GetAgentInWorkspace(context.Context, db.GetAgentInWorkspaceParams) (db.Agent, error)
}

func (h *Handler) dingTalkAgentMetadataStore() dingTalkAgentMetadataStore {
	if h.dingTalkAccountBindingMetadata != nil {
		return h.dingTalkAccountBindingMetadata
	}
	return h.Queries
}

func (h *Handler) loadDingTalkAgentForRequest(
	w http.ResponseWriter,
	r *http.Request,
	workspaceID pgtype.UUID,
	agentID pgtype.UUID,
) (db.Agent, bool) {
	store := h.dingTalkAgentMetadataStore()
	if store == nil {
		writeError(w, http.StatusInternalServerError, "failed to load agent")
		return db.Agent{}, false
	}
	agent, err := store.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
		ID:          agentID,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "agent not found in this workspace")
		} else {
			writeError(w, http.StatusInternalServerError, "failed to load agent")
		}
		return db.Agent{}, false
	}
	if !requireWorkspaceAccessAgent(w, r, agent) {
		return db.Agent{}, false
	}
	return agent, true
}

func (h *Handler) requireDingTalkAgentForWorkspaceToken(
	w http.ResponseWriter,
	r *http.Request,
	workspaceID pgtype.UUID,
	agentID pgtype.UUID,
) bool {
	if _, isToken := middleware.WorkspaceAccessPrincipalFromContext(r.Context()); !isToken {
		return true
	}
	_, ok := h.loadDingTalkAgentForRequest(w, r, workspaceID, agentID)
	return ok
}

func (h *Handler) filterDingTalkAccountBindingsForRequest(
	r *http.Request,
	workspaceID pgtype.UUID,
	bindings []agentmessagerouter.PublicDingTalkAccountBinding,
) ([]agentmessagerouter.PublicDingTalkAccountBinding, error) {
	if _, isToken := middleware.WorkspaceAccessPrincipalFromContext(r.Context()); !isToken {
		return bindings, nil
	}
	store := h.dingTalkAgentMetadataStore()
	if store == nil {
		return nil, errors.New("dingtalk agent metadata store is not configured")
	}
	filtered := make([]agentmessagerouter.PublicDingTalkAccountBinding, 0, len(bindings))
	for _, binding := range bindings {
		agentID, err := util.ParseUUID(binding.AgentID)
		if err != nil {
			return nil, err
		}
		agent, err := store.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
			ID:          agentID,
			WorkspaceID: workspaceID,
		})
		if err != nil {
			return nil, err
		}
		if allowed, _ := workspaceAccessCanUseAgent(r.Context(), agent); allowed {
			filtered = append(filtered, binding)
		}
	}
	return filtered, nil
}

func (h *Handler) filterDingTalkInstallationsForRequest(
	r *http.Request,
	workspaceID pgtype.UUID,
	installations []dingtalk.Installation,
) ([]dingtalk.Installation, error) {
	if _, isToken := middleware.WorkspaceAccessPrincipalFromContext(r.Context()); !isToken {
		return installations, nil
	}
	store := h.dingTalkAgentMetadataStore()
	if store == nil {
		return nil, errors.New("dingtalk agent metadata store is not configured")
	}
	filtered := make([]dingtalk.Installation, 0, len(installations))
	for _, installation := range installations {
		agent, err := store.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
			ID:          installation.AgentID,
			WorkspaceID: workspaceID,
		})
		if err != nil {
			return nil, err
		}
		if allowed, _ := workspaceAccessCanUseAgent(r.Context(), agent); allowed {
			filtered = append(filtered, installation)
		}
	}
	return filtered, nil
}
