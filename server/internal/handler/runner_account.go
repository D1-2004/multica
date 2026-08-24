package handler

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type accountRunnerBindingResponse struct {
	BindingID     string   `json:"binding_id"`
	WorkspaceID   string   `json:"workspace_id"`
	WorkspaceName string   `json:"workspace_name"`
	WorkspaceSlug string   `json:"workspace_slug"`
	AgentID       string   `json:"agent_id"`
	AgentName     string   `json:"agent_name"`
	Roots         []string `json:"roots"`
	Disconnected  bool     `json:"disconnected"`
	BoundAt       string   `json:"bound_at"`
}

type accountRunnerMachineResponse struct {
	MachineID     string                         `json:"machine_id"`
	Name          string                         `json:"name"`
	OS            string                         `json:"os"`
	Arch          string                         `json:"arch"`
	ClientVersion string                         `json:"client_version"`
	Online        bool                           `json:"online"`
	LastSeenAt    *string                        `json:"last_seen_at"`
	Bindings      []accountRunnerBindingResponse `json:"bindings"`
}

func (h *Handler) ListMyRunnerBindings(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	rows, err := h.Queries.ListRunnerBindingsForOwner(r.Context(), parseUUID(userID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list Runner bindings")
		return
	}

	now := time.Now()
	machines := make([]accountRunnerMachineResponse, 0)
	machineIndexes := make(map[string]int)
	for _, row := range rows {
		machineID := uuidToString(row.MachineID)
		machineIndex, exists := machineIndexes[machineID]
		if !exists {
			machine := accountRunnerMachineResponse{
				MachineID:     machineID,
				Name:          row.Name,
				OS:            row.Os,
				Arch:          row.Arch,
				ClientVersion: row.ClientVersion,
				Online:        runnerMachineOnline(row.ConnectionID, row.LastSeenAt, now),
				Bindings:      make([]accountRunnerBindingResponse, 0),
			}
			if row.LastSeenAt.Valid {
				value := row.LastSeenAt.Time.UTC().Format(time.RFC3339)
				machine.LastSeenAt = &value
			}
			machines = append(machines, machine)
			machineIndex = len(machines) - 1
			machineIndexes[machineID] = machineIndex
		}
		machines[machineIndex].Bindings = append(machines[machineIndex].Bindings, accountRunnerBindingResponse{
			BindingID:     uuidToString(row.BindingID),
			WorkspaceID:   uuidToString(row.WorkspaceID),
			WorkspaceName: row.WorkspaceName,
			WorkspaceSlug: row.WorkspaceSlug,
			AgentID:       uuidToString(row.AgentID),
			AgentName:     row.AgentName,
			Roots:         decodeRunnerRoots(row.Roots),
			Disconnected:  row.DisconnectedAt.Valid,
			BoundAt:       row.BoundAt.Time.UTC().Format(time.RFC3339),
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{"machines": machines})
}

func (h *Handler) runnerBindingForOwner(w http.ResponseWriter, r *http.Request) (runnerBindingScope, bool) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return runnerBindingScope{}, false
	}
	bindingID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "bindingId"), "Runner binding id")
	if !ok {
		return runnerBindingScope{}, false
	}
	binding, err := h.Queries.GetRunnerBindingForOwner(r.Context(), db.GetRunnerBindingForOwnerParams{
		BindingID: bindingID,
		OwnerID:   parseUUID(userID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "Runner binding not found")
		return runnerBindingScope{}, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load Runner binding")
		return runnerBindingScope{}, false
	}
	return runnerBindingScope{
		BindingID: binding.ID, WorkspaceID: binding.WorkspaceID, AgentID: binding.AgentID,
	}, true
}

func (h *Handler) DisconnectMyRunnerBinding(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.runnerBindingForOwner(w, r)
	if !ok {
		return
	}
	h.disconnectRunnerBinding(w, r, scope)
}

func (h *Handler) CreateMyRunnerReconnectCommand(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.runnerBindingForOwner(w, r)
	if !ok {
		return
	}
	h.createRunnerReconnectCommand(w, r, scope)
}

func (h *Handler) RevokeMyRunnerBinding(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.runnerBindingForOwner(w, r)
	if !ok {
		return
	}
	h.revokeRunnerBinding(w, r, scope)
}
