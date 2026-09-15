package handler

import (
	"net/http"
	"sort"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
)

// Region discovery exposes no credential material and is available to members
// who can view Runtime capabilities, including non-admin agent owners.
func (h *Handler) GetASBRuntimeRegions(w http.ResponseWriter, r *http.Request) {
	rt, _, ok := h.requireRuntimeCapabilityReadAccess(w, r, chi.URLParam(r, "runtimeId"))
	if !ok {
		return
	}
	if h.ASBLauncher == nil || h.ASBLauncher.Credentials == nil || !h.currentConfig().ASB.Enabled {
		writeError(w, http.StatusServiceUnavailable, "Aone Sandbox Runtime is disabled")
		return
	}
	runtime, err := h.Queries.GetAgentRuntime(r.Context(), parseUUID(rt.runtimeID))
	if err != nil || !service.IsASBRuntime(runtime) {
		writeError(w, http.StatusBadRequest, "ASB Runtime is required")
		return
	}
	regions, ok := h.loadASBRegionOptions(w, r, runtime.ID)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"regions": regions})
}

func (h *Handler) loadASBRegionOptions(w http.ResponseWriter, r *http.Request, runtimeID pgtype.UUID) ([]string, bool) {
	if h.ASBLauncher == nil || h.ASBLauncher.Credentials == nil {
		writeError(w, http.StatusServiceUnavailable, "ASB region discovery is unavailable")
		return nil, false
	}
	client, err := h.ASBLauncher.Credentials.ClientForRuntime(r.Context(), runtimeID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "failed to load ASB Runtime credential")
		return nil, false
	}
	quotas, err := client.ListQuotas(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "failed to discover ASB regions")
		return nil, false
	}
	seen := map[string]bool{}
	regions := make([]string, 0, len(quotas))
	for _, quota := range quotas {
		if quota.Region != "" && !seen[quota.Region] {
			regions = append(regions, quota.Region)
			seen[quota.Region] = true
		}
	}
	sort.Strings(regions)
	return regions, true
}

func (h *Handler) validateAgentASBRegions(w http.ResponseWriter, r *http.Request, runtimeID pgtype.UUID, config []byte) bool {
	selected, err := service.ParseASBExecutionRegions(config)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return false
	}
	if len(selected) == 0 {
		return true
	}
	runtime, err := h.Queries.GetAgentRuntime(r.Context(), runtimeID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "runtime not found")
		return false
	}
	// Keep saved ASB preferences when switching temporarily to another backend.
	if !service.IsASBRuntime(runtime) {
		return true
	}
	regions, ok := h.loadASBRegionOptions(w, r, runtimeID)
	if !ok {
		return false
	}
	available := map[string]bool{}
	for _, region := range regions {
		available[region] = true
	}
	for _, region := range selected {
		if !available[region] {
			writeError(w, http.StatusBadRequest, "selected ASB region is unavailable for this Runtime: "+region)
			return false
		}
	}
	return true
}
