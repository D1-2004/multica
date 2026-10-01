package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/tag"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// The workspace Tag: one multi-tenant digital employee per workspace. See
// package tag for the domain model. Creating, hiding and removing the Tag is
// reserved to deployment platform operators (the same email allow-list that
// gates manual DingTalk identity writes, MULTICA_A2A_OPERATOR_EMAILS);
// tenants and applies are managed by whoever can manage the template agent.

const (
	// tagTemplateName is the template agent's fixed name: the workspace has
	// one Tag and it is simply called Tag.
	tagTemplateName          = "Tag"
	tagTemplateConcurrency   = 6
	tagTemplateVisibility    = "workspace"
	tagTemplatePermission    = "public_to"
	tagEmployeeVisibility    = "workspace"
	tagEmployeePermission    = "public_to"
	tagEmployeeNameSeparator = " · "
)

type TagTenantResponse struct {
	ID                  string     `json:"id"`
	Name                string     `json:"name"`
	EmployeeAgentID     string     `json:"employee_agent_id"`
	EmployeeName        string     `json:"employee_name"`
	EmployeeArchived    bool       `json:"employee_archived"`
	Bound               bool       `json:"bound"`
	OrgID               string     `json:"org_id"`
	OrganizationName    string     `json:"organization_name"`
	DigitalEmployeeName string     `json:"digital_employee_name"`
	AppliedRevision     *int32     `json:"applied_revision"`
	AppliedAt           *time.Time `json:"applied_at"`
	CreatedAt           time.Time  `json:"created_at"`
}

type TagSummaryResponse struct {
	AgentID               string    `json:"agent_id"`
	Name                  string    `json:"name"`
	Description           string    `json:"description"`
	AvatarURL             *string   `json:"avatar_url"`
	RuntimeMode           string    `json:"runtime_mode"`
	SidebarVisible        bool      `json:"sidebar_visible"`
	LatestRevision        *int32    `json:"latest_revision"`
	HasUnpublishedChanges bool      `json:"has_unpublished_changes"`
	CreatedAt             time.Time `json:"created_at"`
}

// TagStateResponse is everything the sidebar, the Tag page and the Tag
// settings tab need in one read.
type TagStateResponse struct {
	Tag *TagSummaryResponse `json:"tag"`
	// CanOperate: the caller is a platform operator (create, hide, remove).
	CanOperate bool `json:"can_operate"`
	// CanManage: the caller can manage the template (tenants, apply).
	CanManage bool                `json:"can_manage"`
	Tenants   []TagTenantResponse `json:"tenants"`
}

type TagApplyTenantResult struct {
	TenantID            string   `json:"tenant_id"`
	Applied             bool     `json:"applied"`
	Reason              string   `json:"reason,omitempty"`
	SkippedSkillIDs     []string `json:"skipped_skill_ids"`
	SkippedConnectorIDs []string `json:"skipped_connector_ids"`
	SkippedPluginIDs    []string `json:"skipped_plugin_ids"`
	SkippedOfferIDs     []string `json:"skipped_offer_ids"`
}

type TagApplyResponse struct {
	Revision  int32                  `json:"revision"`
	Published bool                   `json:"published"`
	Results   []TagApplyTenantResult `json:"results"`
}

func tagTenantToResponse(t tag.Tenant) TagTenantResponse {
	return TagTenantResponse{
		ID:                  t.ID,
		Name:                t.Name,
		EmployeeAgentID:     t.EmployeeAgentID,
		EmployeeName:        t.EmployeeName,
		EmployeeArchived:    t.EmployeeArchived,
		Bound:               t.Bound(),
		OrgID:               t.OrgID,
		OrganizationName:    t.OrganizationName,
		DigitalEmployeeName: t.DigitalEmployeeName,
		AppliedRevision:     t.AppliedRevision,
		AppliedAt:           t.AppliedAt,
		CreatedAt:           t.CreatedAt,
	}
}

// isTagOperator reports whether the human caller may create, hide or remove
// the workspace Tag. It shares the deployment operator allow-list with the
// A2A operator settings; an empty list admits nobody.
func (h *Handler) isTagOperator(r *http.Request, userID string) bool {
	id, err := parseUUIDValue(userID)
	if err != nil {
		return false
	}
	return h.isAgentA2AOperator(r, id)
}

// canManageAgentQuiet mirrors canManageAgent without writing a response.
func canManageAgentQuiet(member db.Member, agent db.Agent, userID string) bool {
	return roleAllowed(member.Role, "owner", "admin") || uuidToString(agent.OwnerID) == userID
}

// loadTagTemplate returns the workspace Tag and its template agent; it writes
// 404 when the workspace has no Tag.
func (h *Handler) loadTagTemplate(w http.ResponseWriter, r *http.Request, workspaceID string) (tag.Tag, db.Agent, bool) {
	t, err := tag.Get(r.Context(), h.DB, workspaceID)
	if errors.Is(err, tag.ErrNotFound) {
		writeError(w, http.StatusNotFound, "this workspace has no tag")
		return tag.Tag{}, db.Agent{}, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load the tag")
		return tag.Tag{}, db.Agent{}, false
	}
	agent, err := h.Queries.GetAgent(r.Context(), parseUUID(t.AgentID))
	if err != nil || uuidToString(agent.WorkspaceID) != workspaceID {
		writeError(w, http.StatusInternalServerError, "the tag template agent is missing")
		return tag.Tag{}, db.Agent{}, false
	}
	return t, agent, true
}

// requireTagManager loads the Tag and checks the caller can manage its
// template agent.
func (h *Handler) requireTagManager(w http.ResponseWriter, r *http.Request) (string, tag.Tag, db.Agent, bool) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return "", tag.Tag{}, db.Agent{}, false
	}
	workspaceID := ctxWorkspaceID(r.Context())
	t, template, ok := h.loadTagTemplate(w, r, workspaceID)
	if !ok {
		return "", tag.Tag{}, db.Agent{}, false
	}
	if !h.canManageAgent(w, r, template) {
		return "", tag.Tag{}, db.Agent{}, false
	}
	return userID, t, template, true
}

func (h *Handler) buildTagState(ctx context.Context, r *http.Request, workspaceID, userID string) (TagStateResponse, error) {
	state := TagStateResponse{CanOperate: h.isTagOperator(r, userID), Tenants: []TagTenantResponse{}}
	t, err := tag.Get(ctx, h.DB, workspaceID)
	if errors.Is(err, tag.ErrNotFound) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	template, err := h.Queries.GetAgent(ctx, parseUUID(t.AgentID))
	if err != nil {
		return state, fmt.Errorf("load tag template: %w", err)
	}
	summary := &TagSummaryResponse{
		AgentID:        t.AgentID,
		Name:           template.Name,
		Description:    template.Description,
		RuntimeMode:    template.RuntimeMode,
		SidebarVisible: t.SidebarVisible,
		CreatedAt:      t.CreatedAt,
	}
	if template.AvatarUrl.Valid {
		avatar := template.AvatarUrl.String
		summary.AvatarURL = &avatar
	}
	if latest, err := tag.LatestRevision(ctx, h.DB, workspaceID, t.AgentID); err == nil {
		rev := latest.Revision
		summary.LatestRevision = &rev
	} else if !errors.Is(err, tag.ErrNotFound) {
		return state, err
	}
	if changed, err := tag.HasUnpublishedChanges(ctx, h.DB, workspaceID, t.AgentID); err == nil {
		summary.HasUnpublishedChanges = changed
	} else if !errors.Is(err, tag.ErrAgentUnavailable) {
		return state, err
	}
	state.Tag = summary
	if member, ok := ctxMember(ctx); ok {
		state.CanManage = canManageAgentQuiet(member, template, userID)
	} else if member, err := h.getWorkspaceMember(ctx, userID, workspaceID); err == nil {
		state.CanManage = canManageAgentQuiet(member, template, userID)
	}
	tenants, err := tag.ListTenants(ctx, h.DB, workspaceID, t.AgentID)
	if err != nil {
		return state, err
	}
	for _, tenant := range tenants {
		state.Tenants = append(state.Tenants, tagTenantToResponse(tenant))
	}
	return state, nil
}

// GetTag returns the workspace Tag state. Every workspace member may read it:
// the sidebar needs to know whether to show the Tag entry.
func (h *Handler) GetTag(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	state, err := h.buildTagState(r.Context(), r, workspaceID, userID)
	if err != nil {
		slog.Warn("load tag state failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to load the tag")
		return
	}
	writeJSON(w, http.StatusOK, state)
}

type createTagRequest struct {
	Description string  `json:"description"`
	AvatarURL   *string `json:"avatar_url"`
	RuntimeID   string  `json:"runtime_id"`
	Model       string  `json:"model"`
	// CopyFromAgentID seeds the template's shared configuration from an
	// existing agent (for example the agent that served a single enterprise
	// before the Tag existed). The runtime stays the requested cloud runtime.
	CopyFromAgentID string `json:"copy_from_agent_id"`
}

// CreateTag provisions the workspace Tag: a template agent on a cloud
// runtime, registered as the workspace's single Tag, with revision 1
// published. Platform operators only.
func (h *Handler) CreateTag(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	if !h.isTagOperator(r, userID) {
		writeError(w, http.StatusForbidden, "only platform operators can create the tag")
		return
	}
	var req createTagRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	name := tagTemplateName
	runtimeID, ok := parseUUIDOrBadRequest(w, req.RuntimeID, "runtime_id")
	if !ok {
		return
	}
	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}
	runtime, err := h.Queries.GetAgentRuntime(r.Context(), runtimeID)
	if err != nil || uuidToString(runtime.WorkspaceID) != workspaceID {
		writeError(w, http.StatusBadRequest, "runtime not found in this workspace")
		return
	}
	if runtime.RuntimeMode != "cloud" {
		writeError(w, http.StatusBadRequest, "the tag must run on a cloud runtime")
		return
	}
	if !canUseRuntimeForAgent(member, runtime) {
		writeError(w, http.StatusForbidden, "you cannot bind an agent to this runtime")
		return
	}
	var copyFrom string
	if strings.TrimSpace(req.CopyFromAgentID) != "" {
		source, ok := h.loadAgentForUser(w, r, req.CopyFromAgentID)
		if !ok {
			return
		}
		copyFrom = uuidToString(source.ID)
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start tag create transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	if err := tag.LockWorkspace(r.Context(), tx, workspaceID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to lock the workspace tag")
		return
	}
	if _, err := tag.Get(r.Context(), tx, workspaceID); err == nil {
		writeError(w, http.StatusConflict, "this workspace already has a tag")
		return
	} else if !errors.Is(err, tag.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, "failed to load the tag")
		return
	}
	var avatar pgtype.Text
	if req.AvatarURL != nil && strings.TrimSpace(*req.AvatarURL) != "" {
		avatar = pgtype.Text{String: strings.TrimSpace(*req.AvatarURL), Valid: true}
	}
	template, err := qtx.CreateAgent(r.Context(), db.CreateAgentParams{
		WorkspaceID:        parseUUID(workspaceID),
		Name:               name,
		Description:        strings.TrimSpace(req.Description),
		AvatarUrl:          avatar,
		RuntimeMode:        runtime.RuntimeMode,
		RuntimeConfig:      []byte("{}"),
		RuntimeID:          runtime.ID,
		Visibility:         tagTemplateVisibility,
		PermissionMode:     pgtype.Text{String: tagTemplatePermission, Valid: true},
		MaxConcurrentTasks: tagTemplateConcurrency,
		OwnerID:            parseUUID(userID),
		CustomEnv:          []byte("{}"),
		CustomArgs:         []byte("[]"),
		Model:              pgtype.Text{String: strings.TrimSpace(req.Model), Valid: strings.TrimSpace(req.Model) != ""},
	})
	if err != nil {
		if agentNameTaken(err) {
			writeError(w, http.StatusConflict, fmt.Sprintf("an agent named %q already exists in this workspace", name))
			return
		}
		slog.Warn("create tag template failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to create the tag")
		return
	}
	// Workspace-visible like any shared agent, so every member can open the
	// Tag page. The template can never hold a DingTalk binding, so it only runs
	// when a member invokes it directly (e.g. to try the shared config).
	if err := replaceInvocationTargetsWithQueries(r.Context(), qtx, template.ID, parseUUID(userID), []targetSpec{
		{targetType: invocationTargetWorkspace, targetID: parseUUID(workspaceID)},
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save agent access")
		return
	}
	templateID := uuidToString(template.ID)
	if copyFrom != "" {
		if _, err := tag.CopyConfig(r.Context(), tx, workspaceID, copyFrom, templateID, userID); err != nil {
			slog.Warn("seed tag template failed", append(logger.RequestAttrs(r), "error", err)...)
			writeError(w, http.StatusInternalServerError, "failed to copy the source agent configuration")
			return
		}
		// The template always runs on the requested cloud runtime, whatever
		// the source agent used, and fields the operator filled in on the
		// create form win over the copied ones.
		if _, err := tx.Exec(r.Context(), `UPDATE agent SET
				runtime_id = $2::uuid,
				runtime_mode = $3,
				description = CASE WHEN $4::text <> '' THEN $4::text ELSE description END,
				model = CASE WHEN $5::text <> '' THEN $5::text ELSE model END,
				avatar_url = COALESCE($6::text, avatar_url),
				updated_at = now()
			WHERE id = $1::uuid`,
			templateID, uuidToString(runtime.ID), runtime.RuntimeMode,
			strings.TrimSpace(req.Description), strings.TrimSpace(req.Model), avatar); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to set the tag runtime")
			return
		}
	}
	if _, err := tag.Create(r.Context(), tx, workspaceID, templateID, userID); err != nil {
		if errors.Is(err, tag.ErrAlreadyExists) {
			writeError(w, http.StatusConflict, "this workspace already has a tag")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to register the tag")
		return
	}
	if _, _, err := tag.PublishIfChanged(r.Context(), tx, workspaceID, templateID, userID, "initial"); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to publish the tag configuration")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit tag create")
		return
	}
	slog.Info("tag created", append(logger.RequestAttrs(r), "agent_id", templateID, "workspace_id", workspaceID)...)
	h.announceAgentCreated(r, workspaceID, userID, template.ID, runtime.Status == "online")

	state, err := h.buildTagState(r.Context(), r, workspaceID, userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load the tag")
		return
	}
	writeJSON(w, http.StatusCreated, state)
}

type updateTagRequest struct {
	SidebarVisible *bool `json:"sidebar_visible"`
}

// UpdateTag toggles the Tag's sidebar visibility. Platform operators only.
func (h *Handler) UpdateTag(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	if !h.isTagOperator(r, userID) {
		writeError(w, http.StatusForbidden, "only platform operators can change the tag")
		return
	}
	var req updateTagRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.SidebarVisible == nil {
		writeError(w, http.StatusBadRequest, "sidebar_visible is required")
		return
	}
	if _, err := tag.SetSidebarVisible(r.Context(), h.DB, workspaceID, *req.SidebarVisible); err != nil {
		if errors.Is(err, tag.ErrNotFound) {
			writeError(w, http.StatusNotFound, "this workspace has no tag")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to update the tag")
		return
	}
	state, err := h.buildTagState(r.Context(), r, workspaceID, userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load the tag")
		return
	}
	writeJSON(w, http.StatusOK, state)
}

// DeleteTag unregisters the Tag once it has no tenants. The template agent
// stays as an ordinary agent. Platform operators only.
func (h *Handler) DeleteTag(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	if !h.isTagOperator(r, userID) {
		writeError(w, http.StatusForbidden, "only platform operators can remove the tag")
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start tag delete transaction")
		return
	}
	defer tx.Rollback(r.Context())
	if err := tag.LockWorkspace(r.Context(), tx, workspaceID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to lock the workspace tag")
		return
	}
	if err := tag.Delete(r.Context(), tx, workspaceID); err != nil {
		switch {
		case errors.Is(err, tag.ErrNotFound):
			writeError(w, http.StatusNotFound, "this workspace has no tag")
		case errors.Is(err, tag.ErrHasTenants):
			writeError(w, http.StatusConflict, "remove every tenant before removing the tag")
		default:
			writeError(w, http.StatusInternalServerError, "failed to remove the tag")
		}
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit tag delete")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type createTagTenantRequest struct {
	Name string `json:"name"`
}

type TagTenantMutationResponse struct {
	Tenant TagTenantResponse     `json:"tenant"`
	Apply  *TagApplyTenantResult `json:"apply,omitempty"`
}

// CreateTagTenant adds a tenant with a new employee agent and applies the
// Tag's latest configuration to it. The employee then binds the tenant's
// DingTalk digital employee through the usual binding flow.
func (h *Handler) CreateTagTenant(w http.ResponseWriter, r *http.Request) {
	userID, t, template, ok := h.requireTagManager(w, r)
	if !ok {
		return
	}
	workspaceID := t.WorkspaceID
	var req createTagTenantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	name, err := tag.NormalizeTenantName(req.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, "name must be 1-64 characters")
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start tenant create transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	if err := tag.LockWorkspace(r.Context(), tx, workspaceID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to lock the workspace tag")
		return
	}
	rev, _, err := tag.PublishIfChanged(r.Context(), tx, workspaceID, t.AgentID, userID, "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to publish the tag configuration")
		return
	}
	employeeName := template.Name + tagEmployeeNameSeparator + name
	employee, err := qtx.CreateAgent(r.Context(), db.CreateAgentParams{
		WorkspaceID:        template.WorkspaceID,
		Name:               employeeName,
		Description:        template.Description,
		AvatarUrl:          template.AvatarUrl,
		RuntimeMode:        template.RuntimeMode,
		RuntimeConfig:      []byte("{}"),
		RuntimeID:          template.RuntimeID,
		Visibility:         tagEmployeeVisibility,
		PermissionMode:     pgtype.Text{String: tagEmployeePermission, Valid: true},
		MaxConcurrentTasks: template.MaxConcurrentTasks,
		OwnerID:            parseUUID(userID),
		CustomEnv:          []byte("{}"),
		CustomArgs:         []byte("[]"),
	})
	if err != nil {
		if agentNameTaken(err) {
			writeError(w, http.StatusConflict, fmt.Sprintf("an agent named %q already exists in this workspace", employeeName))
			return
		}
		slog.Warn("create tag employee failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to create the tenant employee")
		return
	}
	// Workspace members may chat with the employee and assign it work, like
	// any shared agent.
	if err := replaceInvocationTargetsWithQueries(r.Context(), qtx, employee.ID, parseUUID(userID), []targetSpec{
		{targetType: invocationTargetWorkspace, targetID: template.WorkspaceID},
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save agent access")
		return
	}
	tenant, err := tag.InsertTenant(r.Context(), tx, workspaceID, t.AgentID, uuidToString(employee.ID), name, userID)
	if err != nil {
		writeTagTenantError(w, err)
		return
	}
	// The employee starts from the template's tenant-owned defaults (profile,
	// reply behaviour, scene memory); from here on they are the tenant's.
	if err := tag.SeedAgent(r.Context(), tx, workspaceID, t.AgentID, uuidToString(employee.ID)); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to seed the tenant employee")
		return
	}
	result, err := tag.Apply(r.Context(), tx, workspaceID, tenant, rev, userID)
	if err != nil {
		slog.Warn("apply tag to new tenant failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to apply the tag configuration")
		return
	}
	tenant, err = tag.GetTenant(r.Context(), tx, workspaceID, tenant.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load the tenant")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit tenant create")
		return
	}
	slog.Info("tag tenant created", append(logger.RequestAttrs(r), "tenant_id", tenant.ID, "employee_agent_id", tenant.EmployeeAgentID, "revision", rev.Revision)...)
	h.announceAgentCreated(r, workspaceID, userID, employee.ID, true)
	applied := applyResultToResponse(tenant.ID, result)
	writeJSON(w, http.StatusCreated, TagTenantMutationResponse{Tenant: tagTenantToResponse(tenant), Apply: &applied})
}

type adoptTagTenantRequest struct {
	AgentID string `json:"agent_id"`
	Name    string `json:"name"`
}

// AdoptTagTenant links an existing agent (typically one that already has a
// DingTalk digital employee and scenes) as a tenant. Nothing is copied until
// the next apply, so adopting is reversible without side effects.
func (h *Handler) AdoptTagTenant(w http.ResponseWriter, r *http.Request) {
	userID, t, _, ok := h.requireTagManager(w, r)
	if !ok {
		return
	}
	var req adoptTagTenantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	agent, ok := h.loadAgentForUser(w, r, req.AgentID)
	if !ok {
		return
	}
	// The adopted agent's shared configuration will be replaced by the next
	// apply, so the caller must be able to manage it too.
	if !h.canManageAgent(w, r, agent) {
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start tenant adopt transaction")
		return
	}
	defer tx.Rollback(r.Context())
	if err := tag.LockWorkspace(r.Context(), tx, t.WorkspaceID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to lock the workspace tag")
		return
	}
	tenant, err := tag.InsertTenant(r.Context(), tx, t.WorkspaceID, t.AgentID, uuidToString(agent.ID), req.Name, userID)
	if err != nil {
		writeTagTenantError(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit tenant adopt")
		return
	}
	slog.Info("tag tenant adopted", append(logger.RequestAttrs(r), "tenant_id", tenant.ID, "employee_agent_id", tenant.EmployeeAgentID)...)
	writeJSON(w, http.StatusCreated, TagTenantMutationResponse{Tenant: tagTenantToResponse(tenant)})
}

type renameTagTenantRequest struct {
	Name string `json:"name"`
}

func (h *Handler) RenameTagTenant(w http.ResponseWriter, r *http.Request) {
	userID, t, template, ok := h.requireTagManager(w, r)
	if !ok {
		return
	}
	tenantID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "tenantId"), "tenantId")
	if !ok {
		return
	}
	var req renameTagTenantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start tenant rename transaction")
		return
	}
	defer tx.Rollback(r.Context())
	before, err := tag.GetTenant(r.Context(), tx, t.WorkspaceID, uuidToString(tenantID))
	if err != nil {
		writeTagTenantError(w, err)
		return
	}
	tenant, err := tag.RenameTenant(r.Context(), tx, t.WorkspaceID, before.ID, req.Name)
	if err != nil {
		writeTagTenantError(w, err)
		return
	}
	// The employee follows the tenant's name while it still carries the name
	// it was created with ("Tag · <tenant>"); a name changed by hand stays.
	renamedEmployee := false
	if before.EmployeeName == template.Name+tagEmployeeNameSeparator+before.Name && tenant.Name != before.Name {
		next := template.Name + tagEmployeeNameSeparator + tenant.Name
		if _, err := tx.Exec(r.Context(), `UPDATE agent SET name = $3, updated_at = now() WHERE workspace_id = $1::uuid AND id = $2::uuid`,
			t.WorkspaceID, tenant.EmployeeAgentID, next); err != nil {
			if agentNameTaken(err) {
				writeError(w, http.StatusConflict, fmt.Sprintf("an agent named %q already exists in this workspace", next))
				return
			}
			writeError(w, http.StatusInternalServerError, "failed to rename the tenant employee")
			return
		}
		renamedEmployee = true
		if tenant, err = tag.GetTenant(r.Context(), tx, t.WorkspaceID, tenant.ID); err != nil {
			writeTagTenantError(w, err)
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit tenant rename")
		return
	}
	if renamedEmployee {
		h.announceAgentUpdated(r, t.WorkspaceID, userID, parseUUID(tenant.EmployeeAgentID))
	}
	writeJSON(w, http.StatusOK, TagTenantMutationResponse{Tenant: tagTenantToResponse(tenant)})
}

// DeleteTagTenant detaches a tenant. The employee agent, its DingTalk binding
// and its scenes stay as an ordinary agent.
func (h *Handler) DeleteTagTenant(w http.ResponseWriter, r *http.Request) {
	_, t, _, ok := h.requireTagManager(w, r)
	if !ok {
		return
	}
	tenantID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "tenantId"), "tenantId")
	if !ok {
		return
	}
	if err := tag.DeleteTenant(r.Context(), h.DB, t.WorkspaceID, uuidToString(tenantID)); err != nil {
		writeTagTenantError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type applyTagRequest struct {
	TenantIDs []string `json:"tenant_ids"`
	Note      string   `json:"note"`
}

// ApplyTag publishes the template's current configuration when it changed
// and applies the latest revision to the chosen tenants in one transaction.
func (h *Handler) ApplyTag(w http.ResponseWriter, r *http.Request) {
	userID, t, _, ok := h.requireTagManager(w, r)
	if !ok {
		return
	}
	var req applyTagRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.TenantIDs) == 0 {
		writeError(w, http.StatusBadRequest, "choose at least one tenant")
		return
	}
	tenantIDs := make([]string, 0, len(req.TenantIDs))
	seen := map[string]bool{}
	for _, raw := range req.TenantIDs {
		id, ok := parseUUIDOrBadRequest(w, raw, "tenant_ids")
		if !ok {
			return
		}
		key := uuidToString(id)
		if !seen[key] {
			seen[key] = true
			tenantIDs = append(tenantIDs, key)
		}
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start apply transaction")
		return
	}
	defer tx.Rollback(r.Context())
	rev, published, err := tag.PublishIfChanged(r.Context(), tx, t.WorkspaceID, t.AgentID, userID, req.Note)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to publish the tag configuration")
		return
	}
	response := TagApplyResponse{Revision: rev.Revision, Published: published, Results: []TagApplyTenantResult{}}
	updatedEmployees := []string{}
	for _, id := range tenantIDs {
		tenant, err := tag.GetTenant(r.Context(), tx, t.WorkspaceID, id)
		if errors.Is(err, tag.ErrNotFound) {
			writeError(w, http.StatusBadRequest, "tenant not found: "+id)
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to load the tenant")
			return
		}
		if tenant.EmployeeArchived {
			response.Results = append(response.Results, TagApplyTenantResult{
				TenantID: id, Reason: "employee_archived",
				SkippedSkillIDs: []string{}, SkippedConnectorIDs: []string{}, SkippedPluginIDs: []string{}, SkippedOfferIDs: []string{},
			})
			continue
		}
		result, err := tag.Apply(r.Context(), tx, t.WorkspaceID, tenant, rev, userID)
		if err != nil {
			slog.Warn("apply tag failed", append(logger.RequestAttrs(r), "error", err, "tenant_id", id)...)
			writeError(w, http.StatusInternalServerError, "failed to apply the tag configuration")
			return
		}
		response.Results = append(response.Results, applyResultToResponse(id, result))
		updatedEmployees = append(updatedEmployees, tenant.EmployeeAgentID)
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit apply")
		return
	}
	slog.Info("tag applied", append(logger.RequestAttrs(r), "revision", rev.Revision, "published", published, "tenants", len(updatedEmployees))...)
	for _, id := range updatedEmployees {
		h.announceAgentUpdated(r, t.WorkspaceID, userID, parseUUID(id))
	}
	writeJSON(w, http.StatusOK, response)
}

func applyResultToResponse(tenantID string, result tag.ApplyResult) TagApplyTenantResult {
	return TagApplyTenantResult{
		TenantID:            tenantID,
		Applied:             true,
		SkippedSkillIDs:     result.SkippedSkillIDs,
		SkippedConnectorIDs: result.SkippedConnectorIDs,
		SkippedPluginIDs:    result.SkippedPluginIDs,
		SkippedOfferIDs:     result.SkippedOfferIDs,
	}
}

func writeTagTenantError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, tag.ErrNotFound):
		writeError(w, http.StatusNotFound, "tenant not found")
	case errors.Is(err, tag.ErrInvalidName):
		writeError(w, http.StatusBadRequest, "name must be 1-64 characters")
	case errors.Is(err, tag.ErrAgentInUse):
		writeError(w, http.StatusConflict, "this agent already belongs to the tag")
	case errors.Is(err, tag.ErrTagChanged):
		writeError(w, http.StatusConflict, "the tag was removed or replaced; reload and try again")
	case errors.Is(err, tag.ErrAgentServesSeveralOrgs):
		writeError(w, http.StatusConflict, "this agent serves several organizations; remove its extra tenants before adding it to the tag")
	case errors.Is(err, tag.ErrAgentUnavailable):
		writeError(w, http.StatusBadRequest, "agent not found in this workspace")
	default:
		writeError(w, http.StatusInternalServerError, "failed to update the tenant")
	}
}

func agentNameTaken(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "agent_workspace_name_unique"
}

// announceAgentCreated broadcasts a created agent like CreateAgent does.
func (h *Handler) announceAgentCreated(r *http.Request, workspaceID, userID string, agentID pgtype.UUID, reconcile bool) {
	ctx := r.Context()
	if reconcile {
		h.TaskService.ReconcileAgentStatus(ctx, agentID)
	}
	agent, err := h.Queries.GetAgent(ctx, agentID)
	if err != nil {
		return
	}
	resp := h.agentToResponse(agent)
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	h.publish(protocol.EventAgentCreated, workspaceID, actorType, actorID, map[string]any{"agent": broadcastAgentResponse(resp)})
}

// announceAgentUpdated broadcasts an agent whose configuration changed, the
// same event UpdateAgent publishes.
func (h *Handler) announceAgentUpdated(r *http.Request, workspaceID, userID string, agentID pgtype.UUID) {
	agent, err := h.Queries.GetAgent(r.Context(), agentID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("announce tag employee update failed", "error", err)
		}
		return
	}
	resp := h.agentToResponse(agent)
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	h.publish(protocol.EventAgentStatus, workspaceID, actorType, actorID, map[string]any{"agent": broadcastAgentResponse(resp)})
}

// rejectTagTemplateBinding refuses a DingTalk binding on the Tag template: the
// template only holds shared configuration, and each tenant's employee agent
// binds its own digital employee. Handlers built without a database (unit
// harnesses with a fake binding service) have no Tag. Returns false after
// writing the error response.
func (h *Handler) rejectTagTemplateBinding(w http.ResponseWriter, r *http.Request, workspaceID, agentID pgtype.UUID) bool {
	if h.DB == nil {
		return true
	}
	role, err := tag.AgentRole(r.Context(), h.DB, uuidToString(workspaceID), uuidToString(agentID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check the tag")
		return false
	}
	if role == tag.RoleTemplate {
		writeDingTalkAccountBindingAPIError(w, http.StatusBadRequest, "tag_template_not_bindable", "bind the digital employee on a tenant of the tag, not on the tag template")
		return false
	}
	return true
}

// tagEmployeeInheritedCode marks a write refused because a Tag tenant's
// employee inherits that configuration from the Tag.
const tagEmployeeInheritedCode = "tag_employee_inherited"

// refuseTagEmployeeWrite writes 409 and returns true when agentID is a Tag
// tenant's employee: its instructions, skills, connectors, MCP, DSH plugins,
// runtime, model, environment and profile come from the Tag (by apply, or
// from the tenant's name) and are read-only on the employee itself.
func (h *Handler) refuseTagEmployeeWrite(w http.ResponseWriter, r *http.Request, workspaceID, agentID string) bool {
	if h.DB == nil {
		return false
	}
	role, err := tag.AgentRole(r.Context(), h.DB, workspaceID, agentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check the tag")
		return true
	}
	if role != tag.RoleEmployee {
		return false
	}
	writeErrorCode(w, http.StatusConflict, tagEmployeeInheritedCode,
		"this agent is a tag tenant's employee; its configuration is inherited from the tag")
	return true
}

// RefuseTagEmployeeConfigWrites guards the agent configuration routes keyed by
// {id} (skills, DSH plugins, environment, offers) for Tag employees. An {id}
// that is not a UUID is left to the handler's own loader.
func (h *Handler) RefuseTagEmployeeConfigWrites(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		agentID, err := util.ParseUUID(chi.URLParam(r, "id"))
		if err == nil && h.refuseTagEmployeeWrite(w, r, ctxWorkspaceID(r.Context()), uuidToString(agentID)) {
			return
		}
		next.ServeHTTP(w, r)
	})
}

// tagEmployeeConnectorGrantsChanged reports whether setting a connector's
// agents to agentIDs would add or remove a Tag employee: an employee's
// connectors come from the Tag. Other agents in the list are unaffected.
func tagEmployeeConnectorGrantsChanged(ctx context.Context, tx tag.DBTX, workspaceID, connectorID string, agentIDs []string) (bool, error) {
	var changed bool
	err := tx.QueryRow(ctx, `WITH employees AS (
			SELECT employee_agent_id AS id FROM tag_tenant WHERE workspace_id = $1::uuid),
		current AS (
			SELECT agent_id AS id FROM internal_connector_agent
			WHERE connector_id = $2::uuid AND workspace_id = $1::uuid AND agent_id IN (SELECT id FROM employees)),
		requested AS (
			SELECT DISTINCT id::uuid AS id FROM unnest($3::text[]) AS id WHERE id::uuid IN (SELECT id FROM employees))
		SELECT EXISTS (SELECT id FROM current EXCEPT SELECT id FROM requested)
		    OR EXISTS (SELECT id FROM requested EXCEPT SELECT id FROM current)`,
		workspaceID, connectorID, agentIDs).Scan(&changed)
	return changed, err
}
