package sitehosting

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

const testSiteUploadTokenHeader = "X-Multica-Site-Upload-Token"

func prepareUploadedSite(t *testing.T, spaFallback bool) (*Service, PreparedDeploy) {
	t.Helper()
	service := newTestService(&memoryStore{}, &memoryObjectStore{})
	body := zipBytes(t, map[string]string{
		"index.html": "<!doctype html><link rel=stylesheet href=assets/site.css><script src=assets/app.js></script>",
		"assets/site.css": "body{color:green}",
		"assets/app.js": "console.log('site')",
	})
	sum := sha256.Sum256(body)
	prepared, err := service.Prepare(context.Background(), PrepareInput{
		OwnerUserID: "u", SPAFallback: spaFallback,
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

func TestHandleUploadAcceptsRelayAuthorizationWithDedicatedCapability(t *testing.T) {
	service := newTestService(&memoryStore{}, &memoryObjectStore{})
	body := zipBytes(t, map[string]string{"index.html": "<!doctype html><title>relay upload</title>"})
	sum := sha256.Sum256(body)
	prepared, err := service.Prepare(context.Background(), PrepareInput{
		OwnerUserID: "u", ExpectedSHA256: hex.EncodeToString(sum[:]), ExpectedLength: int64(len(body)),
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPut, prepared.UploadURL, bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer mat_task-token")
	request.Header.Set(testSiteUploadTokenHeader, prepared.UploadToken)
	request.Header.Set("Content-Type", "application/zip")
	response := httptest.NewRecorder()

	service.HandleUpload(response, request, prepared.UploadID)

	if response.Code != http.StatusNoContent {
		t.Fatalf("upload status=%d body=%s", response.Code, response.Body.String())
	}
	reuse := httptest.NewRequest(http.MethodPut, prepared.UploadURL, bytes.NewReader(body))
	reuse.Header.Set("Authorization", "Bearer mat_task-token")
	reuse.Header.Set(testSiteUploadTokenHeader, prepared.UploadToken)
	reuse.Header.Set("Content-Type", "application/zip")
	reuseResponse := httptest.NewRecorder()
	service.HandleUpload(reuseResponse, reuse, prepared.UploadID)
	if reuseResponse.Code != http.StatusUnauthorized {
		t.Fatalf("reused capability status=%d", reuseResponse.Code)
	}
}

func TestHandleUploadRejectsAmbiguousCapabilityHeaders(t *testing.T) {
	body := zipBytes(t, map[string]string{"index.html": "<!doctype html><title>ambiguous</title>"})
	sum := sha256.Sum256(body)
	newUpload := func(t *testing.T) (*Service, PreparedDeploy) {
		t.Helper()
		service := newTestService(&memoryStore{}, &memoryObjectStore{})
		prepared, err := service.Prepare(context.Background(), PrepareInput{
			OwnerUserID: "u", ExpectedSHA256: hex.EncodeToString(sum[:]), ExpectedLength: int64(len(body)),
		})
		if err != nil {
			t.Fatal(err)
		}
		return service, prepared
	}
	t.Run("capability also in authorization", func(t *testing.T) {
		service, prepared := newUpload(t)
		request := httptest.NewRequest(http.MethodPut, prepared.UploadURL, bytes.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+prepared.UploadToken)
		request.Header.Set(testSiteUploadTokenHeader, prepared.UploadToken)
		request.Header.Set("Content-Type", "application/zip")
		response := httptest.NewRecorder()
		service.HandleUpload(response, request, prepared.UploadID)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	})
	t.Run("multiple dedicated values", func(t *testing.T) {
		service, prepared := newUpload(t)
		request := httptest.NewRequest(http.MethodPut, prepared.UploadURL, bytes.NewReader(body))
		request.Header.Set("Authorization", "Bearer mat_task-token")
		request.Header[testSiteUploadTokenHeader] = []string{prepared.UploadToken, "mhs_second"}
		request.Header.Set("Content-Type", "application/zip")
		response := httptest.NewRecorder()
		service.HandleUpload(response, request, prepared.UploadID)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	})
	t.Run("comma joined dedicated values", func(t *testing.T) {
		service, prepared := newUpload(t)
		request := httptest.NewRequest(http.MethodPut, prepared.UploadURL, bytes.NewReader(body))
		request.Header.Set("Authorization", "Bearer mat_task-token")
		request.Header.Set(testSiteUploadTokenHeader, prepared.UploadToken+",mhs_second")
		request.Header.Set("Content-Type", "application/zip")
		response := httptest.NewRecorder()
		service.HandleUpload(response, request, prepared.UploadID)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	})
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
	runtimeScript := `<script src="/api/sitehosting/runtime/fetch-proxy.js" data-public-site-id="` + prepared.PublicSiteIDForTest() + `"></script>`
	runtimeOffset := strings.Index(rootResponse.Body.String(), runtimeScript)
	businessOffset := strings.Index(rootResponse.Body.String(), "<script src=assets/app.js>")
	if runtimeOffset < 0 || businessOffset < 0 || runtimeOffset >= businessOffset {
		t.Fatalf("runtime must be injected before business scripts; body=%q", rootResponse.Body.String())
	}
	if got := rootResponse.Header().Get("Content-Length"); got != strconv.Itoa(rootResponse.Body.Len()) {
		t.Fatalf("Content-Length=%q body length=%d", got, rootResponse.Body.Len())
	}
	rootConditional := httptest.NewRequest(http.MethodGet, prepared.SiteURL, nil)
	rootConditional.Header.Set("If-None-Match", rootResponse.Header().Get("ETag"))
	rootConditionalResponse := httptest.NewRecorder()
	service.ServePublic(rootConditionalResponse, rootConditional, prepared.PublicSiteIDForTest(), "")
	if rootConditionalResponse.Code != http.StatusNotModified || rootConditionalResponse.Body.Len() != 0 {
		t.Fatalf("HTML conditional status=%d body=%q", rootConditionalResponse.Code, rootConditionalResponse.Body.String())
	}
	for _, header := range []string{"Content-Security-Policy", "X-Content-Type-Options", "Referrer-Policy", "Permissions-Policy"} {
		if rootResponse.Header().Get(header) == "" {
			t.Fatalf("missing security header %s", header)
		}
	}
	csp := rootResponse.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "connect-src 'self' https://connector.dingtalk.com") || strings.Contains(csp, "connect-src 'none'") {
		t.Fatalf("unexpected hosted-site CSP %q", csp)
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

func TestServePublicSiteUsesCurrentSafeConfiguredConnectSources(t *testing.T) {
	service, prepared := prepareUploadedSite(t, false)
	sources := []string{
		"https://feedback.example.test",
		" https://FEEDBACK.EXAMPLE.TEST/ ",
		defaultConnectSrc,
		"http://insecure.example.test",
		"https://path.example.test/hook",
		"https://evil.example.test;script-src",
		"*",
		"",
	}
	service.config.ConnectSrcProvider = func() []string { return sources }

	request := func() string {
		response := httptest.NewRecorder()
		service.ServePublic(response, httptest.NewRequest(http.MethodGet, prepared.SiteURL, nil), prepared.PublicSiteIDForTest(), "")
		if response.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		return response.Header().Get("Content-Security-Policy")
	}

	first := request()
	want := "connect-src 'self' https://connector.dingtalk.com https://feedback.example.test;"
	if !strings.Contains(first, want) {
		t.Fatalf("CSP missing %q; got %q", want, first)
	}
	for _, rejected := range []string{"http://", "path.example.test", "evil.example.test", "*"} {
		if strings.Contains(first, rejected) {
			t.Fatalf("CSP includes rejected source %q; got %q", rejected, first)
		}
	}
	if strings.Count(first, defaultConnectSrc) != 1 || strings.Count(first, "https://feedback.example.test") != 1 {
		t.Fatalf("CSP must deduplicate sources; got %q", first)
	}

	sources = []string{"https://updated.example.test"}
	second := request()
	if !strings.Contains(second, "connect-src 'self' https://connector.dingtalk.com https://updated.example.test;") || strings.Contains(second, "feedback.example.test") {
		t.Fatalf("CSP did not use the current provider value; got %q", second)
	}
}

func TestServeFetchProxyRuntime(t *testing.T) {
	service := newTestService(&memoryStore{}, &memoryObjectStore{})
	response := httptest.NewRecorder()
	service.ServeFetchProxyRuntime(response, httptest.NewRequest(http.MethodGet, "/api/sitehosting/runtime/fetch-proxy.js", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); got != "text/javascript; charset=utf-8" {
		t.Fatalf("Content-Type=%q", got)
	}
	for _, required := range []string{"__MULTICA_FETCH_PROXY_ALLOWLIST__", "X-Multica-Fetch-Proxy-Result", "window.fetch"} {
		if !strings.Contains(response.Body.String(), required) {
			t.Fatalf("runtime is missing %q", required)
		}
	}
	conditional := httptest.NewRequest(http.MethodGet, "/api/sitehosting/runtime/fetch-proxy.js", nil)
	conditional.Header.Set("If-None-Match", response.Header().Get("ETag"))
	conditionalResponse := httptest.NewRecorder()
	service.ServeFetchProxyRuntime(conditionalResponse, conditional)
	if conditionalResponse.Code != http.StatusNotModified || conditionalResponse.Body.Len() != 0 {
		t.Fatalf("conditional status=%d body=%q", conditionalResponse.Code, conditionalResponse.Body.String())
	}
}

func (p PreparedDeploy) PublicSiteIDForTest() string {
	prefix := strings.TrimSuffix(p.SiteURL, "/")
	return prefix[strings.LastIndex(prefix, "/")+1:]
}
