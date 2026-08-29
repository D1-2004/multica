package handler

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/sitehosting"
)

type StaticSiteHostingService interface {
	Prepare(context.Context, sitehosting.PrepareInput) (sitehosting.PreparedDeploy, error)
	GetStatus(context.Context, string, string) (sitehosting.SiteStatus, error)
	ListSites(context.Context, string) ([]sitehosting.SiteStatus, error)
	DeleteSite(context.Context, string, string) error
	HandleUpload(http.ResponseWriter, *http.Request, string)
	ServePublic(http.ResponseWriter, *http.Request, string, string)
}

func (h *Handler) ListStaticSites(w http.ResponseWriter, r *http.Request) {
	if h.SiteHosting == nil {
		writeError(w, http.StatusServiceUnavailable, "static site hosting is unavailable")
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	sites, err := h.SiteHosting.ListSites(r.Context(), userID)
	if err != nil {
		if errors.Is(err, sitehosting.ErrUnavailable) {
			writeError(w, http.StatusServiceUnavailable, "static site hosting is unavailable")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to list static sites")
		return
	}
	writeJSON(w, http.StatusOK, sites)
}

func (h *Handler) DeleteStaticSite(w http.ResponseWriter, r *http.Request) {
	if h.SiteHosting == nil {
		writeError(w, http.StatusServiceUnavailable, "static site hosting is unavailable")
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	siteUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "siteId"), "siteId")
	if !ok {
		return
	}
	err := h.SiteHosting.DeleteSite(r.Context(), uuidToString(siteUUID), userID)
	if err == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if errors.Is(err, sitehosting.ErrUnavailable) {
		writeError(w, http.StatusServiceUnavailable, "static site hosting is unavailable")
		return
	}
	if errors.Is(err, sitehosting.ErrSiteForbidden) || errors.Is(err, sitehosting.ErrSiteNotFound) {
		writeError(w, http.StatusNotFound, "static site not found")
		return
	}
	writeError(w, http.StatusInternalServerError, "failed to delete static site")
}

func (h *Handler) UploadStaticSite(w http.ResponseWriter, r *http.Request) {
	if h.SiteHosting == nil {
		writeError(w, http.StatusServiceUnavailable, "static site hosting is unavailable")
		return
	}
	h.SiteHosting.HandleUpload(w, r, chi.URLParam(r, "uploadId"))
}

func (h *Handler) ServeStaticSite(w http.ResponseWriter, r *http.Request) {
	if h.SiteHosting == nil {
		writeError(w, http.StatusServiceUnavailable, "static site hosting is unavailable")
		return
	}
	h.SiteHosting.ServePublic(w, r, chi.URLParam(r, "publicSiteId"), chi.URLParam(r, "*"))
}
