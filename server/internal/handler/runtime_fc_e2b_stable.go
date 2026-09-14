package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
)

type stableChannelResponse struct {
	Current       *service.FCE2BStableTemplateBinding `json:"current"`
	ActiveRelease *service.FCE2BStableRelease         `json:"active_release"`
	CanPublish    bool                                `json:"can_publish"`
}

type createStableReleaseRequest struct {
	ProviderScope       string `json:"provider_scope"`
	SandboxBackend      string `json:"sandbox_backend"`
	ArtifactRef         string `json:"artifact_ref"`
	ArtifactBuildID     string `json:"artifact_build_id"`
	ArtifactBuiltAt     string `json:"artifact_built_at"`
	ArtifactDigest      string `json:"artifact_digest"`
	GitCommit           string `json:"git_commit"`
	ProviderFingerprint string `json:"provider_fingerprint"`
	TemplateID          string `json:"template_id"`
	Note                string `json:"note"`
}

func (h *Handler) canPublishFCE2BStable(r *http.Request) bool {
	userID := strings.TrimSpace(requestUserID(r))
	if userID == "" {
		return false
	}
	_, ok := h.currentConfig().StableRuntimePublisherUserIDs[userID]
	return ok
}

func (h *Handler) requireFCE2BStablePublisher(w http.ResponseWriter, r *http.Request) (pgtype.UUID, bool) {
	userID, err := util.ParseUUID(requestUserID(r))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return pgtype.UUID{}, false
	}
	if _, ok := h.currentConfig().StableRuntimePublisherUserIDs[util.UUIDToString(userID)]; !ok {
		writeError(w, http.StatusForbidden, "FC/E2B stable publishing is not permitted")
		return pgtype.UUID{}, false
	}
	return userID, true
}

func (h *Handler) GetFCE2BStableChannel(w http.ResponseWriter, r *http.Request) {
	h.getCloudSandboxStableChannel(w, r, service.SandboxBackendAliyunFC)
}

func (h *Handler) GetCloudSandboxStableChannel(w http.ResponseWriter, r *http.Request) {
	backend, ok := parseCloudSandboxBackendQuery(w, r)
	if !ok {
		return
	}
	h.getCloudSandboxStableChannel(w, r, backend)
}

func (h *Handler) getCloudSandboxStableChannel(
	w http.ResponseWriter,
	r *http.Request,
	backend service.SandboxBackendKind,
) {
	if h.FCE2BStable == nil {
		writeError(w, http.StatusServiceUnavailable, "cloud sandbox stable channel is unavailable")
		return
	}
	scope := r.URL.Query().Get("provider_scope")
	if err := service.ValidateStableProviderScope(backend, scope); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	channel, err := h.FCE2BStable.GetProviderChannel(r.Context(), backend, scope)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load cloud sandbox stable channel")
		return
	}
	writeJSON(w, http.StatusOK, stableChannelResponse{
		Current:       channel.Current,
		ActiveRelease: channel.ActiveRelease,
		CanPublish:    h.canPublishFCE2BStable(r),
	})
}

func (h *Handler) ListFCE2BStableRuntimes(w http.ResponseWriter, r *http.Request) {
	h.listCloudSandboxStableRuntimes(w, r, service.SandboxBackendAliyunFC)
}

func (h *Handler) ListCloudSandboxStableRuntimes(w http.ResponseWriter, r *http.Request) {
	backend, ok := parseCloudSandboxBackendQuery(w, r)
	if !ok {
		return
	}
	h.listCloudSandboxStableRuntimes(w, r, backend)
}

func (h *Handler) listCloudSandboxStableRuntimes(
	w http.ResponseWriter,
	r *http.Request,
	backend service.SandboxBackendKind,
) {
	if _, ok := h.requireFCE2BStablePublisher(w, r); !ok {
		return
	}
	if h.FCE2BStable == nil {
		writeError(w, http.StatusServiceUnavailable, "cloud sandbox stable channel is unavailable")
		return
	}
	scope := r.URL.Query().Get("provider_scope")
	if err := service.ValidateStableProviderScope(backend, scope); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	runtimes, err := h.FCE2BStable.ListRuntimeOverview(r.Context(), backend)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load cloud sandbox stable runtimes")
		return
	}
	if scope != "" {
		filtered := make([]service.FCE2BStableRuntimeOverview, 0)
		for _, runtime := range runtimes {
			if runtime.Provider == scope {
				filtered = append(filtered, runtime)
			}
		}
		runtimes = filtered
	}
	writeJSON(w, http.StatusOK, runtimes)
}

func (h *Handler) CreateFCE2BStableRelease(w http.ResponseWriter, r *http.Request) {
	h.createCloudSandboxStableRelease(w, r, service.SandboxBackendAliyunFC)
}

func (h *Handler) CreateCloudSandboxStableRelease(w http.ResponseWriter, r *http.Request) {
	h.createCloudSandboxStableRelease(w, r, "")
}

func (h *Handler) createCloudSandboxStableRelease(
	w http.ResponseWriter,
	r *http.Request,
	forcedBackend service.SandboxBackendKind,
) {
	actor, ok := h.requireFCE2BStablePublisher(w, r)
	if !ok {
		return
	}
	if h.FCE2BStable == nil {
		writeError(w, http.StatusServiceUnavailable, "FC/E2B stable channel is unavailable")
		return
	}
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" || len(idempotencyKey) > 200 {
		writeError(w, http.StatusBadRequest, "a valid Idempotency-Key header is required")
		return
	}
	var req createStableReleaseRequest
	if r.Body == nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.SandboxBackend = strings.ToLower(strings.TrimSpace(req.SandboxBackend))
	req.ArtifactRef = strings.TrimSpace(req.ArtifactRef)
	req.ArtifactBuildID = strings.TrimSpace(req.ArtifactBuildID)
	req.ArtifactBuiltAt = strings.TrimSpace(req.ArtifactBuiltAt)
	req.ArtifactDigest = strings.ToLower(strings.TrimSpace(req.ArtifactDigest))
	req.GitCommit = strings.ToLower(strings.TrimSpace(req.GitCommit))
	req.ProviderFingerprint = strings.ToLower(strings.TrimSpace(req.ProviderFingerprint))
	req.TemplateID = strings.TrimSpace(req.TemplateID)
	req.Note = strings.TrimSpace(req.Note)
	backend := service.SandboxBackendKind(req.SandboxBackend)
	if forcedBackend != "" {
		if backend != "" && backend != forcedBackend {
			writeError(w, http.StatusBadRequest, "sandbox_backend does not match this endpoint")
			return
		}
		backend = forcedBackend
	}
	if backend != service.SandboxBackendAliyunFC && backend != service.SandboxBackendASB {
		writeError(w, http.StatusBadRequest, "sandbox_backend must be 'aliyun_fc' or 'asb'")
		return
	}
	if err := service.ValidateStableProviderScope(backend, req.ProviderScope); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(req.Note) > 2000 {
		writeError(w, http.StatusBadRequest, "note is too long")
		return
	}
	var artifactBuiltAt *time.Time
	if backend == service.SandboxBackendASB {
		parsed, parseErr := time.Parse(time.RFC3339, req.ArtifactBuiltAt)
		if parseErr != nil {
			writeError(w, http.StatusBadRequest, "artifact_built_at must be an RFC3339 timestamp for ASB releases")
			return
		}
		if parsed.After(time.Now().Add(5 * time.Minute)) {
			writeError(w, http.StatusBadRequest, "artifact_built_at cannot be in the future")
			return
		}
		parsed = parsed.UTC()
		artifactBuiltAt = &parsed
	}
	release, _, err := h.FCE2BStable.CreateRelease(r.Context(), service.CreateFCE2BStableReleaseInput{
		ProviderScope:       req.ProviderScope,
		IdempotencyKey:      idempotencyKey,
		SandboxBackend:      backend,
		ArtifactRef:         req.ArtifactRef,
		ArtifactBuildID:     req.ArtifactBuildID,
		ArtifactBuiltAt:     artifactBuiltAt,
		ArtifactDigest:      req.ArtifactDigest,
		GitCommit:           req.GitCommit,
		ProviderFingerprint: req.ProviderFingerprint,
		TemplateID:          req.TemplateID,
		Note:                req.Note,
		ActorUserID:         actor,
	})
	if err != nil {
		switch {
		case errors.Is(err, service.ErrFCE2BStableIdempotencyConflict),
			errors.Is(err, service.ErrFCE2BStableReleaseConflict),
			errors.Is(err, service.ErrFCE2BStableReleaseState):
			writeError(w, http.StatusConflict, err.Error())
		default:
			writeError(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusAccepted, release)
}

func parseCloudSandboxBackendQuery(
	w http.ResponseWriter,
	r *http.Request,
) (service.SandboxBackendKind, bool) {
	backend := service.SandboxBackendKind(
		strings.ToLower(strings.TrimSpace(r.URL.Query().Get("sandbox_backend"))),
	)
	switch backend {
	case service.SandboxBackendAliyunFC, service.SandboxBackendASB:
		return backend, true
	default:
		writeError(w, http.StatusBadRequest, "sandbox_backend must be 'aliyun_fc' or 'asb'")
		return "", false
	}
}

func (h *Handler) GetFCE2BStableRelease(w http.ResponseWriter, r *http.Request) {
	if h.FCE2BStable == nil {
		writeError(w, http.StatusServiceUnavailable, "FC/E2B stable channel is unavailable")
		return
	}
	releaseID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "releaseId"), "release_id")
	if !ok {
		return
	}
	release, err := h.FCE2BStable.GetRelease(r.Context(), releaseID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "stable release not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load stable release")
		return
	}
	writeJSON(w, http.StatusOK, release)
}

func (h *Handler) ListFCE2BStableReleases(w http.ResponseWriter, r *http.Request) {
	h.listCloudSandboxStableReleases(w, r, service.SandboxBackendAliyunFC)
}

func (h *Handler) ListCloudSandboxStableReleases(w http.ResponseWriter, r *http.Request) {
	backend, ok := parseCloudSandboxBackendQuery(w, r)
	if !ok {
		return
	}
	h.listCloudSandboxStableReleases(w, r, backend)
}

func (h *Handler) listCloudSandboxStableReleases(
	w http.ResponseWriter,
	r *http.Request,
	backend service.SandboxBackendKind,
) {
	if _, ok := h.requireFCE2BStablePublisher(w, r); !ok {
		return
	}
	if h.FCE2BStable == nil {
		writeError(w, http.StatusServiceUnavailable, "cloud sandbox stable channel is unavailable")
		return
	}
	limit := 20
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 100")
			return
		}
		limit = parsed
	}
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if len(status) > 64 {
		writeError(w, http.StatusBadRequest, "status is too long")
		return
	}
	var scope *string
	if r.URL.Query().Has("provider_scope") {
		value := r.URL.Query().Get("provider_scope")
		if err := service.ValidateStableProviderScope(backend, value); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		scope = &value
	}
	releases, err := h.FCE2BStable.ListReleases(r.Context(), backend, status, limit, scope)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load stable releases")
		return
	}
	writeJSON(w, http.StatusOK, releases)
}

func (h *Handler) PauseFCE2BStableRelease(w http.ResponseWriter, r *http.Request) {
	if h.FCE2BStable == nil {
		writeError(w, http.StatusServiceUnavailable, "FC/E2B stable channel is unavailable")
		return
	}
	h.mutateFCE2BStableRelease(w, r, h.FCE2BStable.Pause)
}

func (h *Handler) ResumeFCE2BStableRelease(w http.ResponseWriter, r *http.Request) {
	if h.FCE2BStable == nil {
		writeError(w, http.StatusServiceUnavailable, "FC/E2B stable channel is unavailable")
		return
	}
	h.mutateFCE2BStableRelease(w, r, h.FCE2BStable.Resume)
}

func (h *Handler) StartFCE2BStableRollout(w http.ResponseWriter, r *http.Request) {
	if h.FCE2BStable == nil {
		writeError(w, http.StatusServiceUnavailable, "FC/E2B stable channel is unavailable")
		return
	}
	h.mutateFCE2BStableRelease(w, r, h.FCE2BStable.StartRollout)
}

func (h *Handler) AdvanceFCE2BStableRollout(w http.ResponseWriter, r *http.Request) {
	if h.FCE2BStable == nil {
		writeError(w, http.StatusServiceUnavailable, "FC/E2B stable channel is unavailable")
		return
	}
	h.mutateFCE2BStableRelease(w, r, h.FCE2BStable.AdvanceRollout)
}

func (h *Handler) CompleteFCE2BStableObservation(w http.ResponseWriter, r *http.Request) {
	if h.FCE2BStable == nil {
		writeError(w, http.StatusServiceUnavailable, "FC/E2B stable channel is unavailable")
		return
	}
	h.mutateFCE2BStableRelease(w, r, h.FCE2BStable.CompleteObservation)
}

func (h *Handler) TerminateFCE2BStableRelease(w http.ResponseWriter, r *http.Request) {
	if h.FCE2BStable == nil {
		writeError(w, http.StatusServiceUnavailable, "FC/E2B stable channel is unavailable")
		return
	}
	h.mutateFCE2BStableRelease(w, r, h.FCE2BStable.Terminate)
}

func (h *Handler) RollbackFCE2BStableRelease(w http.ResponseWriter, r *http.Request) {
	if h.FCE2BStable == nil {
		writeError(w, http.StatusServiceUnavailable, "FC/E2B stable channel is unavailable")
		return
	}
	h.mutateFCE2BStableRelease(w, r, h.FCE2BStable.Rollback)
}

func (h *Handler) mutateFCE2BStableRelease(
	w http.ResponseWriter,
	r *http.Request,
	mutate func(context.Context, pgtype.UUID) (service.FCE2BStableRelease, error),
) {
	if _, ok := h.requireFCE2BStablePublisher(w, r); !ok {
		return
	}
	if h.FCE2BStable == nil {
		writeError(w, http.StatusServiceUnavailable, "FC/E2B stable channel is unavailable")
		return
	}
	releaseID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "releaseId"), "release_id")
	if !ok {
		return
	}
	release, err := mutate(r.Context(), releaseID)
	if err != nil {
		if errors.Is(err, service.ErrFCE2BStableReleaseState) ||
			errors.Is(err, service.ErrFCE2BStableAdvanceBlocked) ||
			errors.Is(err, service.ErrFCE2BStableObservationBlocked) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		slog.Error(
			"FC/E2B stable release mutation failed",
			"error", err,
			"release_id", chi.URLParam(r, "releaseId"),
			"path", r.URL.Path,
		)
		writeError(w, http.StatusInternalServerError, "failed to update stable release")
		return
	}
	writeJSON(w, http.StatusOK, release)
}
