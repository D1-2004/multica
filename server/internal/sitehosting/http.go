package sitehosting

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

func (s *Service) HandleUpload(w http.ResponseWriter, r *http.Request, uploadID string) {
	if r.Method != http.MethodPut {
		w.Header().Set("Allow", http.MethodPut)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/zip" {
		http.Error(w, "Content-Type must be application/zip", http.StatusUnsupportedMediaType)
		return
	}
	if r.ContentLength <= 0 {
		http.Error(w, "Content-Length is required", http.StatusLengthRequired)
		return
	}
	token, ok := siteUploadCapability(r.Header)
	if !ok {
		http.Error(w, "invalid upload capability", http.StatusUnauthorized)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.config.Limits.MaxArchiveBytes+1)
	err = s.Upload(r.Context(), uploadID, token, r.Body, r.ContentLength)
	if err == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	switch {
	case errors.Is(err, ErrUploadCapabilityInvalid):
		http.Error(w, "invalid upload capability", http.StatusUnauthorized)
	case errors.Is(err, ErrUnavailable):
		http.Error(w, "static site hosting is unavailable", http.StatusServiceUnavailable)
	default:
		http.Error(w, "static site upload was rejected", http.StatusBadRequest)
	}
}

func siteUploadCapability(header http.Header) (string, bool) {
	dedicated := header.Values(protocol.StaticSiteUploadTokenHeader)
	if len(dedicated) > 0 {
		token, ok := singleOpaqueToken(dedicated, uploadTokenPrefix)
		if !ok {
			return "", false
		}
		authorization := header.Values("Authorization")
		if len(authorization) == 0 {
			return token, true
		}
		relayToken, ok := singleBearerValue(authorization)
		if !ok || !strings.HasPrefix(relayToken, "mat_") {
			return "", false
		}
		return token, true
	}
	bearer, ok := singleBearerValue(header.Values("Authorization"))
	if !ok || !strings.HasPrefix(bearer, uploadTokenPrefix) {
		return "", false
	}
	return bearer, true
}

func singleOpaqueToken(values []string, requiredPrefix string) (string, bool) {
	if len(values) != 1 || values[0] != strings.TrimSpace(values[0]) ||
		!strings.HasPrefix(values[0], requiredPrefix) || strings.ContainsAny(values[0], " \t\r\n,") {
		return "", false
	}
	return values[0], true
}

func singleBearerValue(values []string) (string, bool) {
	if len(values) != 1 {
		return "", false
	}
	scheme, token, found := strings.Cut(strings.TrimSpace(values[0]), " ")
	token = strings.TrimSpace(token)
	if !found || !strings.EqualFold(scheme, "Bearer") || token == "" || strings.ContainsAny(token, " \t\r\n,") {
		return "", false
	}
	return token, true
}

func (s *Service) ServePublic(w http.ResponseWriter, r *http.Request, publicSiteID, requestedPath string) {
	setPublicSecurityHeaders(w.Header(), s.connectSrc())
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	resolved, err := s.ResolvePublic(r.Context(), strings.TrimSpace(publicSiteID))
	if err != nil {
		if errors.Is(err, ErrUnavailable) {
			http.Error(w, "static site hosting is unavailable", http.StatusServiceUnavailable)
			return
		}
		http.NotFound(w, r)
		return
	}
	name, ok := publicObjectPath(requestedPath, resolved.Manifest.Entrypoint)
	if !ok {
		http.NotFound(w, r)
		return
	}
	metadata, found := resolved.Manifest.Files[name]
	if !found && resolved.SPAFallback && path.Ext(name) == "" {
		name = resolved.Manifest.Entrypoint
		metadata, found = resolved.Manifest.Files[name]
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", metadata.ContentType)
	w.Header().Set("Content-Disposition", "inline")
	w.Header().Set("Content-Length", strconv.FormatInt(metadata.Size, 10))
	if metadata.ETag != "" {
		w.Header().Set("ETag", metadata.ETag)
	}
	if strings.HasPrefix(metadata.ContentType, "text/html") {
		w.Header().Set("Cache-Control", "no-cache")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=300, must-revalidate")
	}
	if metadata.ETag != "" && r.Header.Get("If-None-Match") == metadata.ETag {
		w.Header().Del("Content-Length")
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	reader, err := s.objects.Get(r.Context(), objectKey(resolved.SiteID, resolved.RevisionID, name))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer reader.Close()
	_, _ = ioCopyExact(w, reader, metadata.Size)
}

func publicObjectPath(raw, entrypoint string) (string, bool) {
	raw = strings.TrimPrefix(raw, "/")
	if raw == "" {
		return entrypoint, true
	}
	if strings.HasSuffix(raw, "/") {
		return "", false
	}
	name, err := normalizeArchivePath(raw)
	return name, err == nil
}

const defaultConnectSrc = "https://connector.dingtalk.com"

func (s *Service) connectSrc() []string {
	if s == nil || s.config.ConnectSrcProvider == nil {
		return nil
	}
	return s.config.ConnectSrcProvider()
}

func setPublicSecurityHeaders(header http.Header, configured []string) {
	connectSources := []string{"'self'", defaultConnectSrc}
	seen := map[string]struct{}{defaultConnectSrc: {}}
	for _, raw := range configured {
		source, ok := normalizeConnectSource(raw)
		if !ok {
			continue
		}
		if _, exists := seen[source]; exists {
			continue
		}
		seen[source] = struct{}{}
		connectSources = append(connectSources, source)
	}
	header.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self' data:; connect-src "+strings.Join(connectSources, " ")+"; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
	header.Set("X-Frame-Options", "DENY")
	header.Set("Cross-Origin-Resource-Policy", "same-origin")
}

func normalizeConnectSource(raw string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !strings.EqualFold(parsed.Scheme, "https") || parsed.Host == "" || parsed.User != nil ||
		!validCSPHost(parsed.Host) || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
		return "", false
	}
	return "https://" + strings.ToLower(parsed.Host), true
}

func validCSPHost(host string) bool {
	for _, r := range host {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			continue
		}
		switch r {
		case '.', '-', ':', '[', ']':
		default:
			return false
		}
	}
	return true
}

func ioCopyExact(destination http.ResponseWriter, source io.Reader, size int64) (int64, error) {
	written, err := io.Copy(destination, io.LimitReader(source, size+1))
	if err != nil {
		return written, fmt.Errorf("stream hosted object: %w", err)
	}
	if written != size {
		return written, fmt.Errorf("hosted object size mismatch")
	}
	return written, nil
}
