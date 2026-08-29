package handler

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/sitehosting"
)

type StaticSiteHostingService interface {
	Prepare(context.Context, sitehosting.PrepareInput) (sitehosting.PreparedDeploy, error)
	GetStatus(context.Context, string, string, string) (sitehosting.SiteStatus, error)
	HandleUpload(http.ResponseWriter, *http.Request, string)
	ServePublic(http.ResponseWriter, *http.Request, string, string)
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
