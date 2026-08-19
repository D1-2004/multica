package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/storage"
)

const (
	a2aMaxMessageParts  = 32
	a2aMaxRawBytes      = 10 << 20
	a2aMaxDataBytes     = 1 << 20
	a2aMaxURLObjectSize = 100 << 20
	a2aObjectURLTTL     = 15 * time.Minute
)

var blockedA2APrefixes = [...]netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001:db8::/32"),
}

type storedA2APart struct {
	Kind      string         `json:"kind"`
	Text      string         `json:"text,omitempty"`
	Data      any            `json:"data,omitempty"`
	ObjectKey string         `json:"objectKey,omitempty"`
	Filename  string         `json:"filename,omitempty"`
	MediaType string         `json:"mediaType,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	SizeBytes int64          `json:"sizeBytes,omitempty"`
}

type materializedA2APart struct {
	Stored    storedA2APart
	ObjectURL string
}

func validateA2AParts(parts a2a.ContentParts) error {
	if len(parts) == 0 || len(parts) > a2aMaxMessageParts {
		return a2a.NewError(a2a.ErrInvalidParams, "message must contain 1 to 32 parts")
	}
	var rawBytes int
	var dataBytes int
	for _, part := range parts {
		if part == nil || part.Content == nil {
			return a2a.ErrUnsupportedContentType
		}
		if _, _, err := normalizedA2AFilenameAndMediaType(part); err != nil {
			return a2a.NewError(a2a.ErrInvalidParams, err.Error())
		}
		switch content := part.Content.(type) {
		case a2a.Text:
			if !utf8.ValidString(string(content)) {
				return a2a.NewError(a2a.ErrInvalidParams, "text part must contain valid UTF-8")
			}
		case a2a.Raw:
			rawBytes += len(content)
			if rawBytes > a2aMaxRawBytes {
				return a2a.NewError(a2a.ErrInvalidParams, "raw parts exceed the 10 MiB total limit")
			}
		case a2a.Data:
			encoded, err := json.Marshal(content.Value)
			if err != nil {
				return a2a.NewError(a2a.ErrInvalidParams, "data part is not valid JSON")
			}
			dataBytes += len(encoded)
			if dataBytes > a2aMaxDataBytes {
				return a2a.NewError(a2a.ErrInvalidParams, "data parts exceed the 1 MiB total limit")
			}
		case a2a.URL:
			if err := validateA2APublicHTTPSURL(string(content)); err != nil {
				return a2a.NewError(a2a.ErrInvalidParams, err.Error())
			}
		default:
			return a2a.ErrUnsupportedContentType
		}
	}
	return nil
}

func normalizedA2AFilenameAndMediaType(part *a2a.Part) (string, string, error) {
	filename, err := normalizeA2AFilename(part.Filename)
	if err != nil {
		return "", "", err
	}
	mediaType := strings.TrimSpace(part.MediaType)
	if mediaType != "" {
		parsed, parameters, err := mime.ParseMediaType(mediaType)
		if err != nil {
			return "", "", errors.New("part mediaType is invalid")
		}
		mediaType = mime.FormatMediaType(strings.ToLower(parsed), parameters)
	}
	if _, ok := part.Content.(a2a.Text); ok && mediaType == "" {
		mediaType = a2aTextMIMEType
	}
	if _, ok := part.Content.(a2a.Data); ok && mediaType == "" {
		mediaType = "application/json"
	}
	return filename, mediaType, nil
}

func normalizeA2AFilename(raw string) (string, error) {
	filename := strings.TrimSpace(raw)
	if filename == "" {
		return "", nil
	}
	if filename != raw || strings.ContainsAny(filename, "/\\") || path.Base(filename) != filename {
		return "", errors.New("part filename is invalid")
	}
	if filename == "." || filename == ".." || strings.ContainsRune(filename, '\x00') ||
		utf8.RuneCountInString(filename) > 255 || strings.TrimSpace(filename) != filename {
		return "", errors.New("part filename is invalid")
	}
	return filename, nil
}

func normalizeA2AExtensions(values []string, field string) ([]string, error) {
	if len(values) > 16 {
		return nil, fmt.Errorf("%s supports at most 16 URIs", field)
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		parsed, err := url.Parse(value)
		if err != nil || value == "" || value != raw || len(value) > 2048 || !parsed.IsAbs() ||
			strings.ContainsAny(value, "\r\n") {
			return nil, fmt.Errorf("%s contains an invalid absolute URI", field)
		}
		if _, duplicate := seen[value]; duplicate {
			return nil, fmt.Errorf("%s must not contain duplicates", field)
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result, nil
}

func validateA2APublicHTTPSURL(raw string) error {
	if strings.TrimSpace(raw) != raw {
		return errors.New("URL parts must use an absolute public HTTPS URL")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return errors.New("URL parts must use an absolute public HTTPS URL")
	}
	if parsed.User != nil || parsed.Fragment != "" {
		return errors.New("URL parts must not contain credentials or a fragment")
	}
	if parsed.Port() != "" {
		port, err := strconv.Atoi(parsed.Port())
		if err != nil || port < 1 || port > 65535 {
			return errors.New("URL part port is invalid")
		}
	}
	if ip, err := netip.ParseAddr(parsed.Hostname()); err == nil && blockedA2AAddress(ip.Unmap()) {
		return errors.New("URL part resolves to a blocked address")
	}
	return nil
}

func blockedA2AAddress(address netip.Addr) bool {
	if !address.IsValid() ||
		!address.IsGlobalUnicast() ||
		address.IsLoopback() ||
		address.IsPrivate() ||
		address.IsLinkLocalUnicast() ||
		address.IsLinkLocalMulticast() ||
		address.IsMulticast() ||
		address.IsUnspecified() {
		return true
	}
	for _, prefix := range blockedA2APrefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func newA2AOutboundHTTPClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy:                 nil,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          20,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("invalid outbound address: %w", err)
		}
		addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, fmt.Errorf("resolve outbound host: %w", err)
		}
		for _, candidate := range addresses {
			if blockedA2AAddress(candidate.Unmap()) {
				return nil, errors.New("outbound target resolves to a blocked address")
			}
		}
		if len(addresses) == 0 {
			return nil, errors.New("outbound host has no addresses")
		}
		var lastErr error
		for _, candidate := range addresses {
			connection, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(candidate.String(), port))
			if dialErr == nil {
				return connection, nil
			}
			lastErr = dialErr
		}
		return nil, fmt.Errorf("connect to outbound target: %w", lastErr)
	}
	// Redirects are prohibited for A2A URL Parts and webhook callbacks.
	client := &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return client
}

func materializeA2AParts(
	ctx context.Context,
	store storage.Storage,
	httpClient *http.Client,
	objectPrefix string,
	parts a2a.ContentParts,
) ([]materializedA2APart, error) {
	if err := validateA2AParts(parts); err != nil {
		return nil, err
	}
	for _, part := range parts {
		switch part.Content.(type) {
		case a2a.Raw, a2a.URL:
			if store == nil {
				return nil, a2a.NewError(a2a.ErrInternalError, "A2A object storage is not configured")
			}
			if _, ok := store.(storage.Presigner); !ok {
				return nil, a2a.ErrUnsupportedContentType
			}
		}
	}
	result := make([]materializedA2APart, 0, len(parts))
	uploadedKeys := make([]string, 0)
	cleanup := func() {
		if store != nil && len(uploadedKeys) > 0 {
			store.DeleteKeys(context.Background(), uploadedKeys)
		}
	}
	for index, part := range parts {
		filename, mediaType, _ := normalizedA2AFilenameAndMediaType(part)
		stored := storedA2APart{
			Filename:  filename,
			MediaType: mediaType,
			Metadata:  part.Metadata,
		}
		materialized := materializedA2APart{Stored: stored}
		switch content := part.Content.(type) {
		case a2a.Text:
			materialized.Stored.Kind = "text"
			materialized.Stored.Text = string(content)
		case a2a.Data:
			materialized.Stored.Kind = "data"
			materialized.Stored.Data = content.Value
		case a2a.Raw:
			if store == nil {
				cleanup()
				return nil, a2a.NewError(a2a.ErrInternalError, "A2A object storage is not configured")
			}
			if mediaType == "" {
				mediaType = http.DetectContentType(content)
				materialized.Stored.MediaType = mediaType
			}
			if filename == "" {
				filename = fmt.Sprintf("part-%02d%s", index+1, a2AExtensionForMediaType(mediaType))
				materialized.Stored.Filename = filename
			}
			key := objectPrefix + "/" + uuid.NewString() + path.Ext(filename)
			uploadedURL, err := store.Upload(ctx, key, []byte(content), mediaType, filename)
			if err != nil {
				cleanup()
				return nil, a2a.NewError(a2a.ErrInternalError, "unable to store raw A2A part")
			}
			uploadedKeys = append(uploadedKeys, key)
			materialized.Stored.Kind = "object"
			materialized.Stored.ObjectKey = key
			materialized.Stored.SizeBytes = int64(len(content))
			materialized.ObjectURL = uploadedURL
		case a2a.URL:
			if store == nil {
				cleanup()
				return nil, a2a.NewError(a2a.ErrInternalError, "A2A object storage is not configured")
			}
			if httpClient == nil {
				httpClient = newA2AOutboundHTTPClient(30 * time.Second)
			}
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, string(content), nil)
			if err != nil {
				cleanup()
				return nil, a2a.NewError(a2a.ErrInvalidParams, "URL part is invalid")
			}
			response, err := httpClient.Do(request)
			if err != nil {
				cleanup()
				return nil, a2a.NewError(a2a.ErrInvalidParams, "URL part could not be fetched")
			}
			if response.StatusCode < 200 || response.StatusCode >= 300 {
				_ = response.Body.Close()
				cleanup()
				return nil, a2a.NewError(a2a.ErrInvalidParams, "URL part returned a non-success status")
			}
			data, readErr := io.ReadAll(io.LimitReader(response.Body, a2aMaxURLObjectSize+1))
			_ = response.Body.Close()
			if readErr != nil || len(data) > a2aMaxURLObjectSize {
				cleanup()
				return nil, a2a.NewError(a2a.ErrInvalidParams, "URL part exceeds the 100 MiB limit")
			}
			if mediaType == "" {
				mediaType, _, _ = mime.ParseMediaType(response.Header.Get("Content-Type"))
				if mediaType == "" {
					mediaType = http.DetectContentType(data)
				}
				materialized.Stored.MediaType = strings.ToLower(mediaType)
			}
			if filename == "" {
				parsed, _ := url.Parse(string(content))
				candidate, filenameErr := normalizeA2AFilename(path.Base(parsed.Path))
				if filenameErr == nil {
					filename = candidate
				}
				if filename == "" {
					filename = fmt.Sprintf("part-%02d%s", index+1, a2AExtensionForMediaType(mediaType))
				}
				materialized.Stored.Filename = filename
			}
			key := objectPrefix + "/" + uuid.NewString() + path.Ext(filename)
			uploadedURL, err := store.Upload(ctx, key, data, materialized.Stored.MediaType, filename)
			if err != nil {
				cleanup()
				return nil, a2a.NewError(a2a.ErrInternalError, "unable to store URL A2A part")
			}
			uploadedKeys = append(uploadedKeys, key)
			materialized.Stored.Kind = "object"
			materialized.Stored.ObjectKey = key
			materialized.Stored.SizeBytes = int64(len(data))
			materialized.ObjectURL = uploadedURL
		}
		result = append(result, materialized)
	}
	return result, nil
}

func a2AExtensionForMediaType(mediaType string) string {
	exts, _ := mime.ExtensionsByType(mediaType)
	if len(exts) > 0 {
		return exts[0]
	}
	return ".bin"
}

func encodeStoredA2AParts(parts []materializedA2APart) ([]byte, error) {
	stored := make([]storedA2APart, 0, len(parts))
	for _, part := range parts {
		stored = append(stored, part.Stored)
	}
	return json.Marshal(stored)
}

func decodeStoredA2AParts(ctx context.Context, store storage.Storage, raw []byte) (a2a.ContentParts, error) {
	var stored []storedA2APart
	if err := json.Unmarshal(raw, &stored); err != nil {
		return nil, fmt.Errorf("decode stored A2A parts: %w", err)
	}
	parts := make(a2a.ContentParts, 0, len(stored))
	for _, source := range stored {
		var part *a2a.Part
		switch source.Kind {
		case "text":
			part = a2a.NewTextPart(source.Text)
		case "data":
			part = a2a.NewDataPart(source.Data)
		case "object":
			presigner, ok := store.(storage.Presigner)
			if !ok || strings.TrimSpace(source.ObjectKey) == "" {
				return nil, errors.New("A2A object storage cannot create signed URLs")
			}
			signedURL, err := presigner.PresignGet(ctx, source.ObjectKey, a2aObjectURLTTL)
			if err != nil {
				return nil, fmt.Errorf("sign A2A object URL: %w", err)
			}
			part = a2a.NewFileURLPart(a2a.URL(signedURL), source.MediaType)
		default:
			return nil, fmt.Errorf("unknown stored A2A part kind %q", source.Kind)
		}
		part.Filename = source.Filename
		part.MediaType = source.MediaType
		part.Metadata = source.Metadata
		parts = append(parts, part)
	}
	return parts, nil
}

func renderA2AInputForAgent(parts []materializedA2APart) string {
	var builder strings.Builder
	for index, part := range parts {
		if index > 0 {
			builder.WriteString("\n\n")
		}
		switch part.Stored.Kind {
		case "text":
			builder.WriteString(part.Stored.Text)
		case "data":
			encoded, _ := json.MarshalIndent(part.Stored.Data, "", "  ")
			fmt.Fprintf(&builder, "A2A structured input part %d (%s):\n```json\n%s\n```", index+1, part.Stored.MediaType, encoded)
		case "object":
			fmt.Fprintf(
				&builder,
				"A2A file input part %d: filename=%q media_type=%s. The runtime will materialize this attachment before execution.",
				index+1,
				part.Stored.Filename,
				part.Stored.MediaType,
			)
		}
	}
	return strings.TrimSpace(builder.String())
}
