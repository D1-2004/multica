package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

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
	TemplateID      string `json:"template_id"`
	ExpectedBuildID string `json:"expected_build_id"`
	Note            string `json:"note"`
}

func (h *Handler) canPublishFCE2BStable(r *http.Request) bool {
	userID := strings.TrimSpace(requestUserID(r))
	if userID == "" {
		return false
	}
	_, ok := h.cfg.StableRuntimePublisherUserIDs[userID]
	return ok
}

func (h *Handler) requireFCE2BStablePublisher(w http.ResponseWriter, r *http.Request) (pgtype.UUID, bool) {
	userID, err := util.ParseUUID(requestUserID(r))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return pgtype.UUID{}, false
	}
	if _, ok := h.cfg.StableRuntimePublisherUserIDs[util.UUIDToString(userID)]; !ok {
		writeError(w, http.StatusForbidden, "FC/E2B stable publishing is not permitted")
		return pgtype.UUID{}, false
	}
	return userID, true
}

func (h *Handler) GetFCE2BStableChannel(w http.ResponseWriter, r *http.Request) {
	if h.FCE2BStable == nil {
		writeError(w, http.StatusServiceUnavailable, "FC/E2B stable channel is unavailable")
		return
	}
	channel, err := h.FCE2BStable.GetChannel(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load FC/E2B stable channel")
		return
	}
	writeJSON(w, http.StatusOK, stableChannelResponse{
		Current:       channel.Current,
		ActiveRelease: channel.ActiveRelease,
		CanPublish:    h.canPublishFCE2BStable(r),
	})
}

func (h *Handler) ListFCE2BStableRuntimes(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireFCE2BStablePublisher(w, r); !ok {
		return
	}
	if h.FCE2BStable == nil {
		writeError(w, http.StatusServiceUnavailable, "FC/E2B stable channel is unavailable")
		return
	}
	runtimes, err := h.FCE2BStable.ListRuntimeOverview(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load FC/E2B stable runtimes")
		return
	}
	writeJSON(w, http.StatusOK, runtimes)
}

func (h *Handler) CreateFCE2BStableRelease(w http.ResponseWriter, r *http.Request) {
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
	req.TemplateID = strings.TrimSpace(req.TemplateID)
	req.ExpectedBuildID = strings.TrimSpace(req.ExpectedBuildID)
	req.Note = strings.TrimSpace(req.Note)
	if req.TemplateID == "" || req.ExpectedBuildID == "" {
		writeError(w, http.StatusBadRequest, "template_id and expected_build_id are required")
		return
	}
	if len(req.Note) > 2000 {
		writeError(w, http.StatusBadRequest, "note is too long")
		return
	}
	release, _, err := h.FCE2BStable.CreateRelease(r.Context(), service.CreateFCE2BStableReleaseInput{
		IdempotencyKey:  idempotencyKey,
		TemplateID:      req.TemplateID,
		ExpectedBuildID: req.ExpectedBuildID,
		Note:            req.Note,
		ActorUserID:     actor,
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
		writeError(w, http.StatusInternalServerError, "failed to update stable release")
		return
	}
	writeJSON(w, http.StatusOK, release)
}
