package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/dshhost"
	"github.com/multica-ai/multica/server/internal/service"
)

type DSHHomeResponse struct {
	Provisioned bool   `json:"provisioned"`
	State       string `json:"state"`
	Step        int    `json:"step"`
	Generation  int64  `json:"generation"`
	SandboxID   string `json:"sandbox_id,omitempty"`
}

func (h *Handler) dshHomeKey(w http.ResponseWriter, r *http.Request) (dshhost.Key, bool) {
	a, ok := h.loadAgentForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return dshhost.Key{}, false
	}
	if !h.canManageAgent(w, r, a) {
		return dshhost.Key{}, false
	}
	if !a.RuntimeID.Valid {
		writeError(w, http.StatusConflict, "bind an FC DSH runtime before provisioning a Home")
		return dshhost.Key{}, false
	}
	rt, err := h.Queries.GetAgentRuntime(r.Context(), a.RuntimeID)
	if err != nil {
		writeError(w, http.StatusConflict, "agent runtime is unavailable")
		return dshhost.Key{}, false
	}
	if rt.WorkspaceID != a.WorkspaceID || rt.Provider != "dsh" || !service.IsFCE2BRuntime(rt) {
		writeError(w, http.StatusConflict, "persistent DSH Home requires an FC DSH runtime")
		return dshhost.Key{}, false
	}
	return dshhost.Key{WorkspaceID: uuid.UUID(a.WorkspaceID.Bytes), AgentID: uuid.UUID(a.ID.Bytes)}, true
}

func (h *Handler) dshHomeStatus(ctx context.Context, key dshhost.Key) (DSHHomeResponse, error) {
	store := dshhost.PostgresStore{DB: h.DB}
	host, err := store.Get(ctx, key)
	if err == nil {
		return DSHHomeResponse{Provisioned: true, State: host.State, Step: 6, Generation: host.Generation, SandboxID: host.SandboxID}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return DSHHomeResponse{}, err
	}
	p, err := store.GetProvision(ctx, key)
	if errors.Is(err, pgx.ErrNoRows) {
		return DSHHomeResponse{State: "unprovisioned"}, nil
	}
	return DSHHomeResponse{State: p.State, Step: p.Step}, err
}

func (h *Handler) GetDSHHome(w http.ResponseWriter, r *http.Request) {
	key, ok := h.dshHomeKey(w, r)
	if !ok {
		return
	}
	if h.DB == nil {
		writeError(w, http.StatusServiceUnavailable, "DSH Home storage is unavailable")
		return
	}
	status, err := h.dshHomeStatus(r.Context(), key)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "DSH Home status is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, status)
}

// The route is human-only. No body-supplied resource IDs, credentials or
// placement are accepted; the deployment owns the cloud provisioning scope.
func (h *Handler) EnsureDSHHome(w http.ResponseWriter, r *http.Request) {
	if r.Body != nil {
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		decoder.DisallowUnknownFields()
		var empty struct{}
		err := decoder.Decode(&empty)
		if (err != nil && !errors.Is(err, io.EOF)) || (err == nil && !errors.Is(decoder.Decode(&empty), io.EOF)) {
			writeError(w, http.StatusBadRequest, "DSH Home provisioning accepts no client configuration")
			return
		}
	}
	key, ok := h.dshHomeKey(w, r)
	if !ok {
		return
	}
	if h.ProvisionDSHStorage == nil || h.DB == nil {
		writeError(w, http.StatusServiceUnavailable, "DSH storage provisioning is not configured")
		return
	}
	// An already verified immutable binding never needs another provisioning
	// intent, including bindings created before the new provisioning workflow.
	status, err := h.dshHomeStatus(r.Context(), key)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "DSH Home status is unavailable")
		return
	}
	if status.Provisioned {
		writeJSON(w, http.StatusOK, status)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	_, err = h.ProvisionDSHStorage(ctx, key)
	if err != nil && !errors.Is(err, dshhost.ErrPending) && !errors.Is(err, dshhost.ErrChanged) && !errors.Is(err, context.DeadlineExceeded) {
		writeError(w, http.StatusBadGateway, "DSH Home provisioning is unconfirmed; check status before retrying")
		return
	}
	status, loadErr := h.dshHomeStatus(r.Context(), key)
	if loadErr != nil {
		writeError(w, http.StatusServiceUnavailable, "DSH Home provisioning status is unconfirmed")
		return
	}
	code := http.StatusAccepted
	if status.Provisioned {
		code = http.StatusOK
	}
	writeJSON(w, code, status)
}
