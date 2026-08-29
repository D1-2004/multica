package sitehosting

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	fetchProxyRequestBytes  int64 = 1 << 20
	fetchProxyResponseBytes int64 = 2 << 20
	fetchProxyTimeout             = 10 * time.Second
	fetchProxyMarkerHeader        = "X-Multica-Fetch-Proxy-Result"
)

type fetchProxyEnvelope struct {
	Version    int        `json:"version"`
	URL        string     `json:"url"`
	Method     string     `json:"method"`
	Headers    [][]string `json:"headers"`
	BodyBase64 string     `json:"body_base64"`
}

type proxyLookupFunc func(context.Context, string) ([]netip.Addr, error)
type proxyRoundTripFunc func(context.Context, *url.URL, []netip.Addr, *http.Request) (*http.Response, error)

func (s *Service) HandleFetchProxy(w http.ResponseWriter, r *http.Request, publicSiteID string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeFetchProxyError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if _, err := s.ResolvePublic(r.Context(), strings.TrimSpace(publicSiteID)); err != nil {
		writeFetchProxyError(w, http.StatusNotFound, "site_not_found")
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeFetchProxyError(w, http.StatusUnsupportedMediaType, "invalid_content_type")
		return
	}

	maxEnvelopeBytes := int64(base64.StdEncoding.EncodedLen(int(fetchProxyRequestBytes))) + (64 << 10)
	r.Body = http.MaxBytesReader(w, r.Body, maxEnvelopeBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var envelope fetchProxyEnvelope
	if err := decoder.Decode(&envelope); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		writeFetchProxyError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if envelope.Version != 1 {
		writeFetchProxyError(w, http.StatusBadRequest, "unsupported_version")
		return
	}
	method := strings.ToUpper(strings.TrimSpace(envelope.Method))
	if !allowedFetchProxyMethod(method) {
		writeFetchProxyError(w, http.StatusBadRequest, "method_not_allowed")
		return
	}
	body, err := base64.StdEncoding.DecodeString(envelope.BodyBase64)
	if err != nil || int64(len(body)) > fetchProxyRequestBytes || ((method == http.MethodGet || method == http.MethodHead) && len(body) != 0) {
		writeFetchProxyError(w, http.StatusRequestEntityTooLarge, "request_too_large")
		return
	}
	target, err := parseFetchProxyTarget(envelope.URL)
	if err != nil || !s.fetchProxyOriginAllowed(target) {
		writeFetchProxyError(w, http.StatusForbidden, "target_not_allowed")
		return
	}
	proxyContext, cancel := context.WithTimeout(r.Context(), fetchProxyTimeout)
	defer cancel()
	addresses, err := s.proxyLookup(proxyContext, target.Hostname())
	if err != nil || len(addresses) == 0 || !allSafeProxyAddresses(addresses) {
		writeFetchProxyError(w, http.StatusForbidden, "target_address_blocked")
		return
	}

	upstreamRequest, err := http.NewRequestWithContext(proxyContext, method, target.String(), bytes.NewReader(body))
	if err != nil {
		writeFetchProxyError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	copySafeProxyRequestHeaders(upstreamRequest.Header, envelope.Headers)
	upstreamRequest.Header.Set("User-Agent", "Multica-Site-Fetch-Proxy/1")
	response, err := s.proxyRoundTrip(proxyContext, target, addresses, upstreamRequest)
	if err != nil {
		writeFetchProxyError(w, http.StatusBadGateway, "upstream_failed")
		return
	}
	defer response.Body.Close()
	if response.StatusCode >= http.StatusMultipleChoices && response.StatusCode < http.StatusBadRequest {
		writeFetchProxyError(w, http.StatusBadGateway, "upstream_redirect_blocked")
		return
	}
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, fetchProxyResponseBytes+1))
	if err != nil {
		writeFetchProxyError(w, http.StatusBadGateway, "upstream_failed")
		return
	}
	if int64(len(responseBody)) > fetchProxyResponseBytes {
		writeFetchProxyError(w, http.StatusBadGateway, "response_too_large")
		return
	}

	copySafeProxyResponseHeaders(w.Header(), response.Header)
	w.Header().Set(fetchProxyMarkerHeader, "upstream")
	if method != http.MethodHead && response.StatusCode != http.StatusNoContent && response.StatusCode != http.StatusResetContent && response.StatusCode != http.StatusNotModified {
		w.Header().Set("Content-Length", strconv.Itoa(len(responseBody)))
	}
	w.WriteHeader(response.StatusCode)
	if method != http.MethodHead {
		_, _ = w.Write(responseBody)
	}
}

func allowedFetchProxyMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

func parseFetchProxyTarget(raw string) (*url.URL, error) {
	target, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || target.Scheme != "https" || target.Host == "" || target.User != nil || target.Fragment != "" || target.Opaque != "" {
		return nil, errors.New("invalid fetch proxy target")
	}
	if target.Hostname() == "" {
		return nil, errors.New("invalid fetch proxy target host")
	}
	if port := target.Port(); port != "" {
		if parsed, err := strconv.ParseUint(port, 10, 16); err != nil || parsed == 0 {
			return nil, errors.New("invalid fetch proxy target port")
		}
	}
	return target, nil
}

func (s *Service) fetchProxyOriginAllowed(target *url.URL) bool {
	origin, ok := normalizeConnectSource(target.Scheme + "://" + target.Host)
	if !ok {
		return false
	}
	if origin == defaultConnectSrc {
		return true
	}
	for _, raw := range s.connectSrc() {
		configured, valid := normalizeConnectSource(raw)
		if valid && configured == origin {
			return true
		}
	}
	return false
}

func copySafeProxyRequestHeaders(destination http.Header, headers [][]string) {
	for _, pair := range headers {
		if len(pair) != 2 || strings.ContainsAny(pair[1], "\r\n") {
			continue
		}
		name := http.CanonicalHeaderKey(strings.TrimSpace(pair[0]))
		switch name {
		case "Accept", "Accept-Language", "Content-Type":
			destination.Add(name, pair[1])
		}
	}
}

func copySafeProxyResponseHeaders(destination, source http.Header) {
	blocked := map[string]struct{}{
		"Connection": {}, "Content-Length": {}, "Keep-Alive": {}, "Proxy-Authenticate": {},
		"Proxy-Authorization": {}, "Set-Cookie": {}, "Set-Cookie2": {}, "Te": {}, "Trailer": {},
		"Transfer-Encoding": {}, "Upgrade": {}, "Www-Authenticate": {}, fetchProxyMarkerHeader: {},
	}
	for _, value := range source.Values("Connection") {
		for _, token := range strings.Split(value, ",") {
			blocked[http.CanonicalHeaderKey(strings.TrimSpace(token))] = struct{}{}
		}
	}
	for name, values := range source {
		canonical := http.CanonicalHeaderKey(name)
		if _, denied := blocked[canonical]; denied {
			continue
		}
		for _, value := range values {
			destination.Add(canonical, value)
		}
	}
}

func writeFetchProxyError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code}})
}

func defaultProxyLookup(ctx context.Context, host string) ([]netip.Addr, error) {
	if address, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{address}, nil
	}
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

func defaultProxyRoundTrip(ctx context.Context, target *url.URL, addresses []netip.Addr, request *http.Request) (*http.Response, error) {
	host := target.Hostname()
	port := target.Port()
	if port == "" {
		port = "443"
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: -1}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(dialContext context.Context, network, address string) (net.Conn, error) {
			dialHost, dialPort, err := net.SplitHostPort(address)
			if err != nil || !strings.EqualFold(dialHost, host) || dialPort != port {
				return nil, errors.New("fetch proxy dial target changed")
			}
			var lastErr error
			for _, pinned := range addresses {
				connection, err := dialer.DialContext(dialContext, network, net.JoinHostPort(pinned.String(), port))
				if err == nil {
					return connection, nil
				}
				lastErr = err
			}
			return nil, lastErr
		},
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host},
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: fetchProxyTimeout,
		DisableCompression:    true,
		DisableKeepAlives:     true,
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   fetchProxyTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return client.Do(request.WithContext(ctx))
}

var blockedFetchProxyPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/128"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001:2::/48"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
}

func allSafeProxyAddresses(addresses []netip.Addr) bool {
	for _, address := range addresses {
		address = address.Unmap()
		if !address.IsValid() || !address.IsGlobalUnicast() {
			return false
		}
		for _, prefix := range blockedFetchProxyPrefixes {
			if prefix.Contains(address) {
				return false
			}
		}
	}
	return len(addresses) > 0
}
