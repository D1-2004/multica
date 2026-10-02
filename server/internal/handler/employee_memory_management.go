package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/assoc"
	"github.com/multica-ai/multica/server/internal/contextcap"
	"github.com/multica-ai/multica/server/internal/employeeloopconfig"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service/employeememory"
	"github.com/multica-ai/multica/server/internal/service/scenememory"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type managedMemoryResponse struct {
	sceneMemoryResponse
	Loop      string                          `json:"loop"`
	ScopeKind string                          `json:"scope_kind"`
	Learnings []employeememory.LearningRecord `json:"learnings,omitempty"`
	Truncated bool                            `json:"truncated,omitempty"`
}
type managedMemoryMutation struct {
	Loop             string `json:"loop"`
	SceneID          string `json:"scene_id"`
	OrgID            string `json:"org_id"`
	ExpectedRevision *int64 `json:"expected_revision"`
	MemoryText       string `json:"memory_text"`
}

func employeeMemoryResponse(snapshot employeememory.SceneSnapshot) managedMemoryResponse {
	lines := make([]string, 0, len(snapshot.Learnings))
	for _, record := range snapshot.Learnings {
		lines = append(lines, fmt.Sprintf("%s (%s; evidence %s):\n%s", record.Key, record.Source, record.EvidenceID, record.Insight))
	}
	return managedMemoryResponse{Loop: "employee", ScopeKind: "scene", Learnings: snapshot.Learnings, Truncated: snapshot.Truncated, sceneMemoryResponse: sceneMemoryResponse{
		ID: snapshot.Scope.Scene.SceneID, SceneID: snapshot.Scope.Scene.SceneID, SceneKey: snapshot.Scope.Scene.SceneID, WorkspaceID: uuidToString(snapshot.Scope.WorkspaceID), AgentID: uuidToString(snapshot.Scope.AgentID), OrgID: snapshot.Scope.TenantOrgID,
		ConversationID: snapshot.ConversationID, SceneKind: snapshot.Kind, SceneTitle: snapshot.Title, MemoryText: strings.Join(lines, "\n\n"), MemoryRevision: snapshot.Revision, Status: "clean", UpdatedAt: snapshot.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}}
}

// handleSelectedSceneMemory routes explicit loop requests and fences all writes.
// Omitted-loop legacy GETs keep their Coordinator namespace; they never read
// Employee learning. All new UI requests carry loop and all writes verify mode.
func (h *Handler) handleSelectedSceneMemory(w http.ResponseWriter, r *http.Request, action string) bool {
	selected := r.URL.Query().Get("loop")
	explicit := selected != ""
	writing := action == "update" || action == "reset" || action == "clear"
	if !explicit && !writing {
		return false
	}
	if selected == "" {
		selected = "coordinator"
	}
	if employeeloopconfig.Validate(selected) != nil {
		writeError(w, http.StatusBadRequest, "loop must be coordinator or employee")
		return true
	}
	if scope := r.URL.Query().Get("scope"); scope != "" && scope != "scene" {
		writeError(w, http.StatusBadRequest, "only shared scene memory is managed here")
		return true
	}
	caller, ok := h.agentSceneAdmin(w, r)
	if !ok {
		return true
	}
	database := contextcap.DBTX(h.DB)
	var tx pgx.Tx
	if writing {
		if h.TxStarter == nil {
			writeError(w, 503, "memory transactions are not configured")
			return true
		}
		var err error
		tx, err = h.TxStarter.Begin(r.Context())
		if err != nil {
			writeError(w, 500, "failed to begin memory update")
			return true
		}
		defer tx.Rollback(r.Context())
		database = tx
		var ws pgtype.UUID
		if err := tx.QueryRow(r.Context(), "SELECT id FROM workspace WHERE id=$1 FOR KEY SHARE", caller.agent.WorkspaceID).Scan(&ws); err != nil {
			writeError(w, 404, "workspace not found")
			return true
		}
	}
	modeSQL := "SELECT coordination_mode FROM agent WHERE workspace_id=$1 AND id=$2"
	if writing {
		modeSQL += " FOR UPDATE"
	}
	var mode string
	if err := database.QueryRow(r.Context(), modeSQL, caller.agent.WorkspaceID, caller.agent.ID).Scan(&mode); err != nil {
		writeError(w, 500, "failed to load selected loop")
		return true
	}
	if mode != selected {
		writeError(w, http.StatusConflict, "selected loop changed; reload memory before continuing")
		return true
	}
	if selected == "employee" && (action == "update" || action == "clear") {
		writeError(w, http.StatusMethodNotAllowed, "Employee memory is learning-backed; only scene reset is supported")
		return true
	}
	tenants, err := contextcap.AgentTenants(r.Context(), database, caller.workspaceID, caller.agentID)
	if err != nil {
		writeError(w, 500, "failed to load current tenants")
		return true
	}
	orgs := make([]string, 0, len(tenants))
	for _, tenant := range tenants {
		orgs = append(orgs, tenant.OrgID)
	}
	if action == "list" {
		out := []managedMemoryResponse{}
		if selected == "employee" {
			if h.EmployeeMemory == nil {
				writeError(w, 503, "Employee memory is not configured")
				return true
			}
			rows, err := h.EmployeeMemory.ManagedScenes(r.Context(), caller.agent.WorkspaceID, caller.agent.ID, orgs, 200)
			if err != nil {
				writeError(w, 500, "failed to list Employee memory")
				return true
			}
			for _, row := range rows {
				out = append(out, employeeMemoryResponse(row))
			}
		} else if h.SceneMemoryStore != nil {
			rows, err := h.SceneMemoryStore.List(r.Context(), caller.agent.WorkspaceID, caller.agent.ID, 200)
			if err != nil {
				writeError(w, 500, "failed to list Coordinator memory")
				return true
			}
			names := h.sceneMemorySelfNames(r.Context(), caller.agent)
			for _, row := range rows {
				if _, ok := contextcap.FindTenant(tenants, row.OrgID()); ok {
					out = append(out, managedMemoryResponse{sceneMemoryResponse: sceneMemoryToResponse(row, names...), Loop: "coordinator", ScopeKind: "scene"})
				}
			}
		}
		writeJSON(w, 200, out)
		return true
	}
	sceneID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "sceneId"), "scene id")
	if !ok {
		return true
	}
	sc, err := scene.Get(r.Context(), db.New(database), scene.Owner{WorkspaceID: caller.agent.WorkspaceID, AgentID: caller.agent.ID}, sceneID)
	if err != nil {
		writeError(w, 404, "scene not found")
		return true
	}
	if _, ok := contextcap.FindTenant(tenants, sc.TenantOrgID); !ok {
		writeError(w, 404, "scene is outside current agent tenants")
		return true
	}
	var input managedMemoryMutation
	if writing {
		var body io.Reader = http.NoBody
		if r.Body != nil {
			body = r.Body
		}
		err := json.NewDecoder(io.LimitReader(body, 1<<20)).Decode(&input)
		if err != nil && !(errors.Is(err, io.EOF) && !explicit && action != "update") {
			writeError(w, 400, "invalid memory mutation")
			return true
		}
		if explicit && (input.SceneID == "" || input.OrgID == "" || input.ExpectedRevision == nil) {
			writeError(w, 400, "scene_id, org_id and expected_revision are required")
			return true
		}
		if input.SceneID != "" && input.SceneID != uuidToString(sc.ID) || input.OrgID != "" && input.OrgID != sc.TenantOrgID || input.Loop != "" && input.Loop != selected {
			writeError(w, 409, "memory scope changed; reload before continuing")
			return true
		}
	}
	var response any
	if selected == "employee" {
		if h.EmployeeMemory == nil {
			writeError(w, 503, "Employee memory is not configured")
			return true
		}
		scope := employeememory.Scope{WorkspaceID: sc.WorkspaceID, AgentID: sc.AgentID, TenantOrgID: sc.TenantOrgID, Scene: scene.RefOf(sc), Kind: employeememory.ScopeScene}
		var snapshot employeememory.SceneSnapshot
		if writing {
			snapshot, err = h.EmployeeMemory.ResetSceneTx(r.Context(), tx, scope, input.ExpectedRevision)
		} else {
			snapshot, err = h.EmployeeMemory.ManagedScene(r.Context(), scope)
		}
		if errors.Is(err, employeememory.ErrStaleRevision) {
			writeError(w, 409, "memory revision is stale")
			return true
		}
		if err != nil {
			writeError(w, 500, "failed to access Employee memory")
			return true
		}
		response = employeeMemoryResponse(snapshot)
	} else {
		if h.SceneMemoryStore == nil {
			writeError(w, 503, "Coordinator memory is not configured")
			return true
		}
		store := scenememory.NewStore(db.New(database))
		row, e := store.GetByScene(r.Context(), sc.WorkspaceID, sc.AgentID, sc.ID)
		if e != nil {
			writeError(w, 404, "scene memory not found")
			return true
		}
		if writing {
			var revision int64
			if err := tx.QueryRow(r.Context(), "SELECT memory_revision FROM agent_scene_memory WHERE scene_id=$1 AND workspace_id=$2 AND agent_id=$3 FOR UPDATE", sc.ID, sc.WorkspaceID, sc.AgentID).Scan(&revision); err != nil {
				writeError(w, 404, "scene memory not found")
				return true
			}
			if input.ExpectedRevision != nil && *input.ExpectedRevision != revision {
				writeError(w, 409, "memory revision is stale")
				return true
			}
			switch action {
			case "update":
				if input.ExpectedRevision == nil {
					writeError(w, 400, "expected_revision is required")
					return true
				}
				row, err = store.ReplaceText(r.Context(), row, *input.ExpectedRevision, scenememory.SanitizeMemoryTextForAgent(input.MemoryText, h.sceneMemorySelfNames(r.Context(), caller.agent)...))
			case "reset":
				row, err = store.Reset(r.Context(), sc, scenememory.DirtyTrigger{OccurredAt: time.Now().UTC(), EvidenceID: "owner-reset"})
			}
			if err != nil {
				if errors.Is(err, scenememory.ErrStaleRevision) {
					writeError(w, 409, "memory revision is stale")
				} else if errors.Is(err, scenememory.ErrMemoryText) {
					writeError(w, 400, "invalid memory text")
				} else {
					writeError(w, 500, "failed to update Coordinator memory")
				}
				return true
			}
			if (action == "reset" || action == "clear") && h.Assoc != nil {
				cleared, e := assoc.NewSQLStore(tx).CloseSceneAssociations(r.Context(), caller.workspaceID, caller.agentID, uuidToString(sc.ID))
				if e != nil {
					writeError(w, 500, "failed to clear scene associations")
					return true
				}
				if action == "clear" {
					response = map[string]int{"closed_edges": cleared.ClosedEdges, "unlinked_events": cleared.UnlinkedEvents}
				}
			} else if action == "clear" {
				writeError(w, 503, "association store is not configured")
				return true
			}
		}
		if response == nil {
			response = managedMemoryResponse{sceneMemoryResponse: sceneMemoryToResponse(row, h.sceneMemorySelfNames(r.Context(), caller.agent)...), Loop: "coordinator", ScopeKind: "scene"}
		}
	}
	if writing {
		if err := tx.Commit(r.Context()); err != nil {
			writeError(w, 500, "failed to commit memory update")
			return true
		}
	}
	if writing {
		slog.InfoContext(r.Context(), "agent scene memory mutation", "loop", selected, "action", action, "agent_id", caller.agentID, "scene_id", uuidToString(sc.ID), "org_id", sc.TenantOrgID)
	}
	writeJSON(w, 200, response)
	return true
}
