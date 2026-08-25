package handler

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// AgentDispatchPromptPreviewResponse describes the inbound prompt an agent
// receives, so an owner can see the real structure before overriding any of it.
//
// It has two halves on purpose. `segments` is the task instruction, which the
// server composes and therefore knows exactly — it is produced by the same
// function the claim path uses. `runtime_sections` is everything the daemon
// assembles on the runtime (the brief and the per-turn body); the server does
// not own that text and will not reproduce it, because a second copy would
// drift and start lying. Those are listed structurally so the owner can see
// what else reaches the agent and what is not customizable from here.
type AgentDispatchPromptPreviewResponse struct {
	// Surface is the dispatch mode the preview was rendered for.
	Surface  string                  `json:"surface"`
	Segments []DispatchPromptSegment `json:"segments"`
	// Instruction is the assembled text, exactly as the claim path would emit
	// it for this scenario.
	Instruction     string                         `json:"instruction"`
	RuntimeSections []DispatchPromptRuntimeSection `json:"runtime_sections"`
}

// DispatchPromptRuntimeSection is a daemon-composed section, listed by name and
// origin only.
type DispatchPromptRuntimeSection struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	// Customizable is false for every entry today: these are assembled on the
	// runtime, so overriding them would require shipping the override through
	// the claim protocol.
	Customizable bool `json:"customizable"`
	// Origin names where the text lives, so the field is actionable rather than
	// just informative.
	Origin string `json:"origin"`
}

// runtimeComposedSections mirrors internal/daemon/execenv/runtime_config_sections.go
// and internal/daemon/prompt.go. It is a structural index, not a copy of their
// text — deliberately, so this list going stale is visible as a missing row
// rather than as wrong content shown to a user.
var runtimeComposedSections = []DispatchPromptRuntimeSection{
	{ID: "agent_identity", Source: "agent", Origin: "agent.instructions"},
	{ID: "workspace_context", Source: "workspace", Origin: "workspace.context"},
	{ID: "requesting_user", Source: "workspace", Origin: "user.profile_description"},
	{ID: "project_context", Source: "workspace", Origin: "project.title / project.description"},
	{ID: "repositories", Source: "workspace", Origin: "workspace.repos / project resources"},
	{ID: "skills_index", Source: "agent", Origin: "agent skills + built-in skills"},
	{ID: "background_task_safety", Source: "builtin", Origin: "runtime_config_sections.go"},
	{ID: "available_commands", Source: "builtin", Origin: "runtime_config_sections.go"},
	{ID: "issue_body_formatting", Source: "builtin", Origin: "runtime_config_sections.go"},
	{ID: "comment_formatting", Source: "builtin", Origin: "runtime_config_sections.go"},
	{ID: "instruction_precedence", Source: "builtin", Origin: "runtime_config_sections.go"},
	{ID: "workflow", Source: "builtin", Origin: "runtime_config_sections.go"},
	{ID: "mentions", Source: "builtin", Origin: "runtime_config_sections.go"},
	{ID: "attachments", Source: "builtin", Origin: "runtime_config_sections.go"},
	{ID: "output", Source: "builtin", Origin: "runtime_config_sections.go"},
	{ID: "turn_body", Source: "builtin", Origin: "daemon/prompt.go"},
}

// GetAgentDispatchPromptPreview renders the inbound prompt structure for one
// agent. It is agent-scoped and manage-gated: the managed policy is deployment
// configuration, not public product copy.
func (h *Handler) GetAgentDispatchPromptPreview(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.loadAgentForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	if !h.canManageAgent(w, r, agent) {
		return
	}

	surface := strings.TrimSpace(r.URL.Query().Get("surface"))
	if surface == "" {
		surface = protocol.DispatchSurfaceTypeAuto
	}
	if surface != protocol.DispatchSurfaceTypeIssue &&
		surface != protocol.DispatchSurfaceTypeChat &&
		surface != protocol.DispatchSurfaceTypeAuto {
		writeError(w, http.StatusBadRequest, "surface must be issue, chat, or auto")
		return
	}

	// A representative dispatch rather than a real one: the preview answers
	// "what would this agent be told", so it renders the scenario where every
	// segment is in play. contextPrompt is left as a placeholder because its
	// real content is resolved per dispatch by the Router and does not exist yet.
	stored := persistedDispatchContext{
		Source:        DispatchSource{Platform: "dingtalk", Type: "digital_employee"},
		Domain:        "channel",
		Type:          "message.created",
		Surface:       DispatchSurface{Type: surface},
		Outbound:      DispatchOutbound{Mode: protocol.DispatchOutboundModeDWS, ReplyTo: protocol.DispatchReplyToLatestMessage},
		ContextPrompt: "",
	}

	enterpriseAuthorizationURL := ""
	if h.EnterpriseIdentity != nil {
		enterpriseAuthorizationURL = buildEnterpriseIdentityAuthorizationURL(
			h.currentConfig().AppURL,
			h.workspaceSlugForPreview(r, agent.WorkspaceID),
			uuidToString(agent.ID),
		)
	}

	segments := composeDispatchInstructionSegments(dispatchInstructionInputs{
		Stored:                     stored,
		Present:                    true,
		DingTalkContext:            true,
		Flags:                      h.FeatureFlags,
		Overrides:                  parseDispatchPromptOverrides(agent.DispatchPromptOverrides),
		EnterpriseAuthorizationURL: enterpriseAuthorizationURL,
	})

	writeJSON(w, http.StatusOK, AgentDispatchPromptPreviewResponse{
		Surface:         surface,
		Segments:        segments,
		Instruction:     instructionFromSegments(segments),
		RuntimeSections: runtimeComposedSections,
	})
}

func (h *Handler) workspaceSlugForPreview(r *http.Request, workspaceID pgtype.UUID) string {
	workspace, err := h.Queries.GetWorkspace(r.Context(), workspaceID)
	if err != nil {
		return ""
	}
	return workspace.Slug
}
