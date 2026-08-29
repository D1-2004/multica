package sitehosting

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func prepareUploadedSite(t *testing.T, spaFallback bool) (*Service, PreparedDeploy) {
	t.Helper()
	service := newTestService(&memoryStore{}, &memoryObjectStore{})
	body := zipBytes(t, map[string]string{
		"index.html": "<!doctype html><link rel=stylesheet href=assets/site.css>",
		"assets/site.css": "body{color:green}",
		"assets/app.js": "console.log('site')",
	})
	sum := sha256.Sum256(body)
	prepared, err := service.Prepare(context.Background(), PrepareInput{
		WorkspaceID: "w", AgentID: "a", SPAFallback: spaFallback,
		ExpectedSHA256: hex.EncodeToString(sum[:]), ExpectedLength: int64(len(body)),
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPut, prepared.UploadURL, bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+prepared.UploadToken)
	request.Header.Set("Content-Type", "application/zip")
	response := httptest.NewRecorder()
	service.HandleUpload(response, request, prepared.UploadID)
	if response.Code != http.StatusNoContent {
		t.Fatalf("upload status=%d body=%s", response.Code, response.Body.String())
	}
	return service, prepared
}

func TestHandleUploadRequiresRawZipCapability(t *testing.T) {
	service := newTestService(&memoryStore{}, &memoryObjectStore{})
	request := httptest.NewRequest(http.MethodPut, "/api/sitehosting/uploads/id", strings.NewReader("body"))
	request.Header.Set("Content-Type", "multipart/form-data; boundary=x")
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	service.HandleUpload(response, request, "id")
	if response.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestServePublicSiteReturnsInlineHTMLAndAssetsWithSecurityHeaders(t *testing.T) {
	service, prepared := prepareUploadedSite(t, false)

	rootResponse := httptest.NewRecorder()
	service.ServePublic(rootResponse, httptest.NewRequest(http.MethodGet, prepared.SiteURL, nil), prepared.PublicSiteIDForTest(), "")
	if rootResponse.Code != http.StatusOK {
		t.Fatalf("root status=%d body=%s", rootResponse.Code, rootResponse.Body.String())
	}
	if got := rootResponse.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Fatalf("Content-Type=%q", got)
	}
	if got := rootResponse.Header().Get("Content-Disposition"); got != "inline" {
		t.Fatalf("Content-Disposition=%q", got)
	}
	for _, header := range []string{"Content-Security-Policy", "X-Content-Type-Options", "Referrer-Policy", "Permissions-Policy"} {
		if rootResponse.Header().Get(header) == "" {
			t.Fatalf("missing security header %s", header)
		}
	}

	assetResponse := httptest.NewRecorder()
	service.ServePublic(assetResponse, httptest.NewRequest(http.MethodGet, prepared.SiteURL+"assets/app.js", nil), prepared.PublicSiteIDForTest(), "assets/app.js")
	if assetResponse.Code != http.StatusOK || !strings.Contains(assetResponse.Header().Get("Content-Type"), "javascript") {
		t.Fatalf("asset status=%d type=%q body=%s", assetResponse.Code, assetResponse.Header().Get("Content-Type"), assetResponse.Body.String())
	}

	conditional := httptest.NewRequest(http.MethodGet, prepared.SiteURL+"assets/app.js", nil)
	conditional.Header.Set("If-None-Match", assetResponse.Header().Get("ETag"))
	conditionalResponse := httptest.NewRecorder()
	service.ServePublic(conditionalResponse, conditional, prepared.PublicSiteIDForTest(), "assets/app.js")
	if conditionalResponse.Code != http.StatusNotModified {
		t.Fatalf("conditional status=%d", conditionalResponse.Code)
	}
}

func TestServePublicSiteHasNoDirectoryListingAndOptionalSPAFallback(t *testing.T) {
	service, prepared := prepareUploadedSite(t, true)
	directory := httptest.NewRecorder()
	service.ServePublic(directory, httptest.NewRequest(http.MethodGet, prepared.SiteURL+"assets/", nil), prepared.PublicSiteIDForTest(), "assets/")
	if directory.Code != http.StatusNotFound {
		t.Fatalf("directory status=%d", directory.Code)
	}

	spa := httptest.NewRecorder()
	service.ServePublic(spa, httptest.NewRequest(http.MethodGet, prepared.SiteURL+"dashboard/settings", nil), prepared.PublicSiteIDForTest(), "dashboard/settings")
	if spa.Code != http.StatusOK || !strings.Contains(spa.Body.String(), "doctype html") {
		t.Fatalf("spa status=%d body=%s", spa.Code, spa.Body.String())
	}
}

func (p PreparedDeploy) PublicSiteIDForTest() string {
	prefix := strings.TrimSuffix(p.SiteURL, "/")
	return prefix[strings.LastIndex(prefix, "/")+1:]
}
