package handler

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/pkg/featureflag"
)

// AgentDispatchPromptDefaultResponse serves the deployment-managed dispatch
// policy so the Agent settings editor can open on it instead of on a blank
// field. Seeding matters: `agent.dispatch_prompt` replaces the managed policy
// wholesale, and an author starting from an empty box silently drops the
// injection defenses, credential-handling rules, and truthfulness clauses the
// managed prompt carries. Starting from the real text makes keeping them the
// default and removing one a deliberate edit.
type AgentDispatchPromptDefaultResponse struct {
	// Prompt is the managed common policy: the section an override replaces
	// for every dispatch mode. Empty when the deployment configures none.
	Prompt string `json:"prompt"`
}

// GetAgentDispatchPromptDefault returns the managed dispatch policy for the
// Agent settings editor. It is agent-scoped and gated on the same permission
// as editing the prompt, because the managed policy is deployment
// configuration rather than public product copy.
func (h *Handler) GetAgentDispatchPromptDefault(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.loadAgentForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	if !h.canManageAgent(w, r, agent) {
		return
	}
	writeJSON(w, http.StatusOK, AgentDispatchPromptDefaultResponse{
		Prompt: resolveDispatchRuntimePrompt(h.FeatureFlags, featureflag.DispatchCommonRuntimePromptFlagKey),
	})
}
