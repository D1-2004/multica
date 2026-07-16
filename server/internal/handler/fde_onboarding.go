package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/integrations/dingtalk"
	"github.com/multica-ai/multica/server/internal/logger"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type FDEOnboardingStateResponse struct {
	Configured bool                `json:"configured"`
	Workspaces []WorkspaceResponse `json:"workspaces"`
}

type FDEOnboardingProvisionRequest struct {
	WorkspaceID   string `json:"workspace_id"`
	WorkspaceName string `json:"workspace_name"`
}

type FDEOnboardingProvisionResponse struct {
	Workspace       WorkspaceResponse             `json:"workspace"`
	RuntimeID       string                        `json:"runtime_id"`
	AgentID         string                        `json:"agent_id"`
	AgentCreated    bool                          `json:"agent_created"`
	InstallComplete bool                          `json:"install_complete"`
	Install         *BeginDingTalkInstallResponse `json:"install,omitempty"`
}

func (h *Handler) GetFDEOnboarding(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	rows, err := h.Queries.ListAdminWorkspacesForUser(r.Context(), parseUUID(userID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list workspaces")
		return
	}
	workspaces := make([]WorkspaceResponse, len(rows))
	for i, row := range rows {
		workspaces[i] = workspaceToResponse(row)
	}
	writeJSON(w, http.StatusOK, FDEOnboardingStateResponse{
		Configured: h.fdeOnboardingConfigured(),
		Workspaces: workspaces,
	})
}

func (h *Handler) ProvisionFDEOnboarding(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	if h.ManagedAgent == nil || !h.ManagedAgent.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "FDE Agent source is not configured")
		return
	}
	if !h.cfg.FCE2B.Enabled || strings.TrimSpace(h.cfg.FCE2B.Template) == "" {
		writeError(w, http.StatusServiceUnavailable, "FDE FC runtime is not configured")
		return
	}
	if err := h.cfg.FCE2B.Validate(); err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	if h.DingTalkRegistration == nil || h.DingTalkInstallations == nil {
		writeError(w, http.StatusServiceUnavailable, "DingTalk bot installation is not configured")
		return
	}

	var req FDEOnboardingProvisionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	ownerID := parseUUID(userID)
	workspace, err := h.resolveOrCreateFDEWorkspace(r, ownerID, req)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, errFDEWorkspaceForbidden) || errors.Is(err, errFDEWorkspaceCreationDisabled) {
			status = http.StatusForbidden
		} else if !errors.Is(err, errFDEWorkspaceInput) {
			status = http.StatusInternalServerError
		}
		writeError(w, status, err.Error())
		return
	}

	runtime, err := h.upsertFDERuntime(r, workspace.ID, ownerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to prepare FC runtime: "+err.Error())
		return
	}
	model := ""
	if len(h.cfg.FCE2B.LLMModels) > 0 {
		model = h.cfg.FCE2B.LLMModels[0]
	}
	agent, created, err := h.ManagedAgent.Provision(r.Context(), workspace.ID, ownerID, runtime.ID, runtime.RuntimeMode, runtime.Provider, model)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "failed to prepare FDE Agent: "+err.Error())
		return
	}
	// FC task claims mint their mat_ token for runtime.owner_id. Keep that
	// identity exactly aligned with the user-owned managed Agent, including a
	// retry that encounters a runtime left behind by an earlier partial flow.
	if runtime.OwnerID != agent.OwnerID {
		runtime, err = h.upsertFDERuntime(r, workspace.ID, agent.OwnerID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to align FC runtime ownership: "+err.Error())
			return
		}
	}

	installations, err := h.DingTalkInstallations.ListByWorkspace(r.Context(), workspace.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to inspect DingTalk installation")
		return
	}
	for _, installation := range installations {
		if installation.AgentID == agent.ID && installation.Status == string(dingtalk.InstallationActive) {
			writeJSON(w, http.StatusOK, FDEOnboardingProvisionResponse{
				Workspace: workspaceToResponse(workspace), RuntimeID: uuidToString(runtime.ID),
				AgentID: uuidToString(agent.ID), AgentCreated: created, InstallComplete: true,
			})
			return
		}
	}

	begin, err := h.DingTalkRegistration.BeginInstall(
		r.Context(),
		fdeDingTalkInstallParams(workspace.ID, agent.ID, ownerID),
	)
	if err != nil {
		writeError(w, http.StatusBadGateway, "failed to start DingTalk install: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, FDEOnboardingProvisionResponse{
		Workspace: workspaceToResponse(workspace), RuntimeID: uuidToString(runtime.ID),
		AgentID: uuidToString(agent.ID), AgentCreated: created,
		Install: &BeginDingTalkInstallResponse{
			SessionID: begin.SessionID, QRCodeURL: begin.QRCodeURL,
			ExpiresInSeconds: begin.ExpiresInSeconds, PollIntervalSeconds: begin.PollIntervalSeconds,
		},
	})
}

func fdeDingTalkInstallParams(workspaceID, agentID, initiatorID pgtype.UUID) dingtalk.BeginInstallParams {
	return dingtalk.BeginInstallParams{
		WorkspaceID: workspaceID,
		AgentID:     agentID,
		InitiatorID: initiatorID,
		// The managed FDE bot is an organization entry point, so coworkers who
		// can reach it must not be forced through Multica account binding first.
		AllowUnbound: true,
	}
}

var (
	errFDEWorkspaceInput            = errors.New("workspace name or workspace_id is required")
	errFDEWorkspaceForbidden        = errors.New("workspace must be owned or administered by the current user")
	errFDEWorkspaceCreationDisabled = errors.New("workspace creation is disabled for this instance")
)

func (h *Handler) resolveOrCreateFDEWorkspace(r *http.Request, userID pgtype.UUID, req FDEOnboardingProvisionRequest) (db.Workspace, error) {
	if strings.TrimSpace(req.WorkspaceID) != "" {
		workspaceID, err := parseUUIDValue(req.WorkspaceID)
		if err != nil {
			return db.Workspace{}, errFDEWorkspaceInput
		}
		member, err := h.Queries.GetMemberByUserAndWorkspace(r.Context(), db.GetMemberByUserAndWorkspaceParams{UserID: userID, WorkspaceID: workspaceID})
		if err != nil || (member.Role != "owner" && member.Role != "admin") {
			return db.Workspace{}, errFDEWorkspaceForbidden
		}
		return h.Queries.GetWorkspace(r.Context(), workspaceID)
	}
	name := strings.TrimSpace(req.WorkspaceName)
	if name == "" {
		return db.Workspace{}, errFDEWorkspaceInput
	}
	if h.cfg.DisableWorkspaceCreation {
		return db.Workspace{}, errFDEWorkspaceCreationDisabled
	}
	base := runtimeSlug(name)
	if base == "fc-hermes" || isReservedSlug(base) {
		base = "fde-workspace"
	}
	for attempt := 0; attempt < 5; attempt++ {
		slug := base
		if attempt > 0 {
			slug += "-" + randomID()[:8]
		}
		tx, err := h.TxStarter.Begin(r.Context())
		if err != nil {
			return db.Workspace{}, err
		}
		qtx := h.Queries.WithTx(tx)
		if _, err := tx.Exec(r.Context(), "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", "multica:fde-workspace:"+uuidToString(userID)); err != nil {
			_ = tx.Rollback(r.Context())
			return db.Workspace{}, err
		}
		existing, err := qtx.ListAdminWorkspacesForUser(r.Context(), userID)
		if err != nil {
			_ = tx.Rollback(r.Context())
			return db.Workspace{}, err
		}
		if len(existing) > 0 {
			_ = tx.Rollback(r.Context())
			return existing[0], nil
		}
		workspace, err := qtx.CreateWorkspace(r.Context(), db.CreateWorkspaceParams{
			Name: name, Slug: slug, IssuePrefix: generateIssuePrefix(name),
		})
		if err == nil {
			_, err = qtx.CreateMember(r.Context(), db.CreateMemberParams{WorkspaceID: workspace.ID, UserID: userID, Role: "owner"})
		}
		if err == nil {
			err = tx.Commit(r.Context())
		} else {
			_ = tx.Rollback(r.Context())
		}
		if err == nil {
			workspaceID := uuidToString(workspace.ID)
			obsmetrics.RecordEvent(h.Analytics, h.Metrics, analytics.WorkspaceCreated(uuidToString(userID), workspaceID))
			h.notifyDaemonWorkspacesChanged(uuidToString(userID))
			slog.Info("FDE onboarding workspace created", append(logger.RequestAttrs(r), "workspace_id", workspaceID, "name", workspace.Name)...)
			return workspace, nil
		}
		if !isUniqueViolation(err) {
			return db.Workspace{}, err
		}
	}
	return db.Workspace{}, errors.New("failed to generate a unique workspace slug")
}

func (h *Handler) upsertFDERuntime(r *http.Request, workspaceID, ownerID pgtype.UUID) (db.AgentRuntime, error) {
	name := "FDE Runtime"
	daemonID := pgtype.Text{String: "fc-e2b:fde:" + uuidToString(workspaceID), Valid: true}
	// The FDE template is DWS-enabled by contract, so "dws" is asserted here
	// rather than sniffed from the template name.
	provider := service.FCE2BProviderForTemplate(h.cfg.FCE2B.Template)
	metadata, err := json.Marshal(map[string]any{
		"kind": service.FCE2BMetadataKind, "template": h.cfg.FCE2B.Template,
		"template_id": h.cfg.FCE2B.Template, "template_name": name,
		"capabilities": []string{provider, "dws"}, "timeout_seconds": h.cfg.FCE2B.TimeoutSeconds,
		"created_by": uuidToString(ownerID), "runner": service.FCE2BRunnerCommandForProvider(provider),
		"managed_source_key": "fde-agent",
	})
	if err != nil {
		return db.AgentRuntime{}, err
	}
	return h.Queries.UpsertCloudAgentRuntime(r.Context(), db.UpsertCloudAgentRuntimeParams{
		WorkspaceID: workspaceID,
		DaemonID:    daemonID,
		Name:        name, RuntimeMode: "cloud", Provider: provider,
		Status: "online", DeviceInfo: name, Metadata: metadata, OwnerID: ownerID, Visibility: "private",
	})
}

func (h *Handler) fdeOnboardingConfigured() bool {
	if h.ManagedAgent == nil || !h.ManagedAgent.Enabled() || !h.cfg.FCE2B.Enabled || strings.TrimSpace(h.cfg.FCE2B.Template) == "" || h.DingTalkRegistration == nil || h.DingTalkInstallations == nil {
		return false
	}
	return h.cfg.FCE2B.Validate() == nil
}
