package handler

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func (h *Handler) loadASBNetworkRuntime(w http.ResponseWriter, r *http.Request) (db.AgentRuntime, bool) {
	if h.ASBLauncher == nil || !h.currentConfig().ASB.Enabled {
		writeError(w, http.StatusServiceUnavailable, "Aone Sandbox Runtime is disabled")
		return db.AgentRuntime{}, false
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "runtimeId"), "runtime_id")
	if !ok {
		return db.AgentRuntime{}, false
	}
	runtime, err := h.Queries.GetAgentRuntime(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "runtime not found")
		return db.AgentRuntime{}, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load runtime")
		return db.AgentRuntime{}, false
	}
	if _, ok := h.requireWorkspaceRole(w, r, uuidToString(runtime.WorkspaceID), "runtime not found", "owner", "admin"); !ok {
		return db.AgentRuntime{}, false
	}
	if !service.IsASBRuntime(runtime) {
		writeError(w, http.StatusBadRequest, service.ErrCloudSandboxRuntimeRequired.Error())
		return db.AgentRuntime{}, false
	}
	return runtime, true
}

func (h *Handler) GetASBRuntimeNetworkPolicy(w http.ResponseWriter, r *http.Request) {
	runtime, ok := h.loadASBNetworkRuntime(w, r)
	if !ok {
		return
	}
	h.writeASBNetworkPolicy(w, r, runtime)
}

func (h *Handler) writeASBNetworkPolicy(w http.ResponseWriter, r *http.Request, runtime db.AgentRuntime) {
	settings, err := h.ASBLauncher.RuntimeNetworkPolicy(r.Context(), runtime)
	if err != nil {
		slog.Error("load ASB network policy", "runtime_id", uuidToString(runtime.ID), "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load network policy")
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (h *Handler) UpdateASBRuntimeNetworkPolicy(w http.ResponseWriter, r *http.Request) {
	runtime, ok := h.loadASBNetworkRuntime(w, r)
	if !ok {
		return
	}
	var req struct {
		CustomTargets *[]string `json:"custom_targets"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&req) != nil || req.CustomTargets == nil {
		writeError(w, http.StatusBadRequest, "custom_targets must be an array of exact domains or IPs")
		return
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	targets, err := service.NormalizeASBNetworkTargets(*req.CustomTargets)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	runtime, err = h.ASBLauncher.UpdateRuntimeNetworkAllowlist(r.Context(), runtime.ID, targets)
	if err != nil {
		slog.Error("update ASB network policy", "runtime_id", uuidToString(runtime.ID), "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save network allowlist")
		return
	}
	slog.Info("ASB network allowlist updated", "runtime_id", uuidToString(runtime.ID), "workspace_id", uuidToString(runtime.WorkspaceID), "target_count", len(targets))
	h.writeASBNetworkPolicy(w, r, runtime)
}
