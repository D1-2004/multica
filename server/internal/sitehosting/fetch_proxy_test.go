package sitehosting

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
)

func TestHandleFetchProxyForwardsAllowedRequestAndFiltersHeaders(t *testing.T) {
	service, prepared := prepareUploadedSite(t, false)
	service.proxyLookup = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	}
	var captured *http.Request
	service.proxyRoundTrip = func(_ context.Context, _ *url.URL, _ []netip.Addr, request *http.Request) (*http.Response, error) {
		captured = request
		return &http.Response{
			StatusCode: http.StatusAccepted,
			Header: http.Header{
				"Content-Type": []string{"application/json"},
				"Set-Cookie":   []string{"session=secret"},
				"X-Upstream":   []string{"preserved"},
			},
			Body: io.NopCloser(strings.NewReader(`{"accepted":true}`)),
		}, nil
	}
	body := []byte(`{"feedback":"works"}`)
	envelope := fetchProxyEnvelope{
		Version: 1,
		URL: "https://connector.dingtalk.com/webhook/flow/redacted",
		Method: http.MethodPost,
		Headers: [][]string{
			{"Content-Type", "application/json"},
			{"Authorization", "Bearer secret"},
			{"Cookie", "session=secret"},
			{"Origin", "https://attacker.example"},
			{"X-Forwarded-For", "127.0.0.1"},
		},
		BodyBase64: base64.StdEncoding.EncodeToString(body),
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/sitehosting/sites/"+prepared.PublicSiteIDForTest()+"/fetch-proxy", bytes.NewReader(encoded))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	service.HandleFetchProxy(response, request, prepared.PublicSiteIDForTest())

	if response.Code != http.StatusAccepted || response.Body.String() != `{"accepted":true}` {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}
	if response.Header().Get(fetchProxyMarkerHeader) != "upstream" || response.Header().Get("X-Upstream") != "preserved" {
		t.Fatalf("response headers=%v", response.Header())
	}
	if response.Header().Get("Set-Cookie") != "" {
		t.Fatalf("Set-Cookie leaked: %v", response.Header().Values("Set-Cookie"))
	}
	if captured == nil || captured.Method != http.MethodPost || captured.URL.String() != envelope.URL {
		t.Fatalf("captured request=%v", captured)
	}
	capturedBody, err := io.ReadAll(captured.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(capturedBody, body) || captured.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("body=%q headers=%v", capturedBody, captured.Header)
	}
	for _, sensitive := range []string{"Authorization", "Cookie", "Origin", "Referer", "Host", "X-Forwarded-For"} {
		if captured.Header.Get(sensitive) != "" {
			t.Fatalf("sensitive header %s leaked", sensitive)
		}
	}
}

func TestHandleFetchProxyRejectsUnconfiguredOriginBeforeDNS(t *testing.T) {
	service, prepared := prepareUploadedSite(t, false)
	lookedUp := false
	service.proxyLookup = func(context.Context, string) ([]netip.Addr, error) {
		lookedUp = true
		return nil, nil
	}
	envelope := fetchProxyEnvelope{Version: 1, URL: "https://unconfigured.example/hook", Method: http.MethodPost}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/fetch-proxy", bytes.NewReader(encoded))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	service.HandleFetchProxy(response, request, prepared.PublicSiteIDForTest())

	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "target_not_allowed") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if lookedUp {
		t.Fatal("unconfigured target must be rejected before DNS lookup")
	}
}

func TestHandleFetchProxyUsesCurrentConfiguredOrigin(t *testing.T) {
	service, prepared := prepareUploadedSite(t, false)
	configured := []string{"https://feedback.example.test"}
	service.config.ConnectSrcProvider = func() []string { return configured }
	service.proxyLookup = func(_ context.Context, host string) ([]netip.Addr, error) {
		if host != "feedback.example.test" {
			t.Fatalf("host=%q", host)
		}
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	}
	service.proxyRoundTrip = func(_ context.Context, _ *url.URL, _ []netip.Addr, _ *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusNoContent, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
	}
	envelope := fetchProxyEnvelope{Version: 1, URL: "https://feedback.example.test/exact/path?flow=1", Method: http.MethodPost}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/fetch-proxy", bytes.NewReader(encoded))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	service.HandleFetchProxy(response, request, prepared.PublicSiteIDForTest())

	if response.Code != http.StatusNoContent || response.Header().Get(fetchProxyMarkerHeader) != "upstream" {
		t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
}

func TestHandleFetchProxyRejectsAnyUnsafeResolvedAddress(t *testing.T) {
	service, prepared := prepareUploadedSite(t, false)
	service.proxyLookup = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("169.254.169.254")}, nil
	}
	service.proxyRoundTrip = func(context.Context, *url.URL, []netip.Addr, *http.Request) (*http.Response, error) {
		t.Fatal("blocked address must not be requested")
		return nil, nil
	}
	envelope := fetchProxyEnvelope{Version: 1, URL: "https://connector.dingtalk.com/hook", Method: http.MethodGet}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/fetch-proxy", bytes.NewReader(encoded))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	service.HandleFetchProxy(response, request, prepared.PublicSiteIDForTest())

	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "target_address_blocked") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestHandleFetchProxyDoesNotExposeUpstreamRedirect(t *testing.T) {
	service, prepared := prepareUploadedSite(t, false)
	service.proxyLookup = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	}
	service.proxyRoundTrip = func(context.Context, *url.URL, []netip.Addr, *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusTemporaryRedirect,
			Header: http.Header{"Location": []string{"https://redirect.example/unsafe"}},
			Body: io.NopCloser(strings.NewReader("redirect")),
		}, nil
	}
	envelope := fetchProxyEnvelope{Version: 1, URL: "https://connector.dingtalk.com/hook", Method: http.MethodPost}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/fetch-proxy", bytes.NewReader(encoded))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	service.HandleFetchProxy(response, request, prepared.PublicSiteIDForTest())

	if response.Code != http.StatusBadGateway || response.Header().Get("Location") != "" || response.Header().Get(fetchProxyMarkerHeader) != "" {
		t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
}

func TestFetchProxyAddressPolicy(t *testing.T) {
	for _, raw := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "192.0.2.1", "::1", "fc00::1", "2001:db8::1"} {
		if allSafeProxyAddresses([]netip.Addr{netip.MustParseAddr(raw)}) {
			t.Fatalf("address %s must be blocked", raw)
		}
	}
	for _, raw := range []string{"93.184.216.34", "2606:4700::6810:85e5"} {
		if !allSafeProxyAddresses([]netip.Addr{netip.MustParseAddr(raw)}) {
			t.Fatalf("address %s should be allowed", raw)
		}
	}
}
