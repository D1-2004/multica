package sitehosting

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	uploadTokenPrefix = "mhs_"
	defaultTokenTTL   = 10 * time.Minute
)

type Config struct {
	APIBaseURL    string
	SitePublicURL string
	TempDir       string
	TokenTTL      time.Duration
	Limits        Limits
}

type PrepareInput struct {
	WorkspaceID    string
	AgentID        string
	SiteID         string
	Entrypoint     string
	SPAFallback    bool
	ExpectedSHA256 string
	ExpectedLength int64
}

type PreparedDeploy struct {
	SiteID       string    `json:"site_id"`
	RevisionID   string    `json:"revision_id"`
	UploadID     string    `json:"upload_id"`
	UploadURL    string    `json:"upload_url"`
	UploadMethod string    `json:"upload_method"`
	UploadToken  string    `json:"upload_token"`
	ExpiresAt    time.Time `json:"expires_at"`
	Archive      string    `json:"archive"`
	Entrypoint   string    `json:"entrypoint"`
	SiteURL      string    `json:"site_url"`
	Limits       LimitsDTO `json:"limits"`
}

type LimitsDTO struct {
	MaxArchiveBytes  int64 `json:"max_archive_bytes"`
	MaxExpandedBytes int64 `json:"max_expanded_bytes"`
	MaxFileBytes     int64 `json:"max_file_bytes"`
	MaxFiles         int   `json:"max_files"`
}

type Service struct {
	store   Store
	objects ObjectStore
	config  Config
	now     func() time.Time
}

func NewService(store Store, objects ObjectStore, config Config) *Service {
	if config.TokenTTL <= 0 {
		config.TokenTTL = defaultTokenTTL
	}
	if config.Limits.MaxArchiveBytes <= 0 {
		config.Limits = DefaultLimits()
	}
	return &Service{store: store, objects: objects, config: config, now: time.Now}
}

func (s *Service) Available() bool {
	return s != nil && s.store != nil && s.objects != nil && validBaseURL(s.config.APIBaseURL) && validBaseURL(s.config.SitePublicURL)
}

func (s *Service) Prepare(ctx context.Context, input PrepareInput) (PreparedDeploy, error) {
	if !s.Available() {
		return PreparedDeploy{}, ErrUnavailable
	}
	input.WorkspaceID = strings.TrimSpace(input.WorkspaceID)
	input.AgentID = strings.TrimSpace(input.AgentID)
	input.SiteID = strings.TrimSpace(input.SiteID)
	input.Entrypoint = strings.TrimSpace(input.Entrypoint)
	if input.Entrypoint == "" {
		input.Entrypoint = "index.html"
	}
	entrypoint, err := normalizeArchivePath(input.Entrypoint)
	if err != nil {
		return PreparedDeploy{}, fmt.Errorf("invalid entrypoint: %w", err)
	}
	if input.WorkspaceID == "" || input.AgentID == "" {
		return PreparedDeploy{}, ErrSiteForbidden
	}
	expectedSHA := strings.ToLower(strings.TrimSpace(input.ExpectedSHA256))
	decodedSHA, err := hex.DecodeString(expectedSHA)
	if err != nil || len(decodedSHA) != sha256.Size {
		return PreparedDeploy{}, fmt.Errorf("expected_sha256 must be 64 hexadecimal characters")
	}
	if input.ExpectedLength <= 0 || input.ExpectedLength > s.config.Limits.MaxArchiveBytes {
		return PreparedDeploy{}, fmt.Errorf("content_length must be between 1 and %d", s.config.Limits.MaxArchiveBytes)
	}
	token, tokenHash, err := newUploadToken()
	if err != nil {
		return PreparedDeploy{}, err
	}
	now := s.now().UTC()
	upload := Upload{
		ID: uuid.NewString(), SiteID: uuid.NewString(), PublicSiteID: randomOpaqueID(),
		RevisionID: uuid.NewString(), WorkspaceID: input.WorkspaceID, OwnerAgentID: input.AgentID,
		TokenHash: tokenHash, ExpectedSHA256: expectedSHA, ExpectedLength: input.ExpectedLength,
		Entrypoint: entrypoint, SPAFallback: input.SPAFallback, ExpiresAt: now.Add(s.config.TokenTTL),
	}
	if input.SiteID != "" {
		upload.SiteID = input.SiteID
		upload.PublicSiteID = ""
	}
	upload, err = s.store.Prepare(ctx, PrepareRecord{ExistingSiteID: input.SiteID, Upload: upload})
	if err != nil {
		return PreparedDeploy{}, err
	}
	return PreparedDeploy{
		SiteID: upload.SiteID, RevisionID: upload.RevisionID, UploadID: upload.ID,
		UploadURL: strings.TrimRight(s.config.APIBaseURL, "/") + "/api/sitehosting/uploads/" + upload.ID,
		UploadMethod: "PUT", UploadToken: token, ExpiresAt: upload.ExpiresAt,
		Archive: "zip", Entrypoint: upload.Entrypoint, SiteURL: s.siteURL(upload.PublicSiteID),
		Limits: LimitsDTO{MaxArchiveBytes: s.config.Limits.MaxArchiveBytes, MaxExpandedBytes: s.config.Limits.MaxExpandedBytes, MaxFileBytes: s.config.Limits.MaxFileBytes, MaxFiles: s.config.Limits.MaxFiles},
	}, nil
}

func (s *Service) Upload(ctx context.Context, uploadID, token string, body io.Reader, contentLength int64) error {
	if !s.Available() {
		return ErrUnavailable
	}
	tokenHash, ok := parseUploadToken(token)
	if !ok {
		return ErrUploadCapabilityInvalid
	}
	upload, err := s.store.ClaimUpload(ctx, uploadID, tokenHash, s.now().UTC())
	if err != nil {
		return err
	}
	fail := func(cause error) error {
		_ = s.store.FailRevision(ctx, upload.RevisionID, publicFailureReason(cause))
		return cause
	}
	if contentLength != upload.ExpectedLength || contentLength <= 0 || contentLength > s.config.Limits.MaxArchiveBytes {
		return fail(fmt.Errorf("upload Content-Length does not match prepared length"))
	}
	temp, err := os.CreateTemp(s.config.TempDir, "multica-site-*.zip")
	if err != nil {
		return fail(fmt.Errorf("create upload staging file: %w", err))
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	defer temp.Close()

	hasher := sha256.New()
	limited := io.LimitReader(body, upload.ExpectedLength+1)
	written, err := io.Copy(temp, io.TeeReader(limited, hasher))
	if err != nil {
		return fail(fmt.Errorf("stream upload body: %w", err))
	}
	if written != upload.ExpectedLength {
		return fail(fmt.Errorf("upload body length does not match prepared length"))
	}
	actualSHA := hex.EncodeToString(hasher.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(actualSHA), []byte(upload.ExpectedSHA256)) != 1 {
		return fail(fmt.Errorf("upload SHA-256 does not match prepared digest"))
	}
	if err := temp.Close(); err != nil {
		return fail(fmt.Errorf("close upload staging file: %w", err))
	}
	manifest, err := ValidateArchive(tempPath, s.config.Limits, upload.Entrypoint)
	if err != nil {
		return fail(err)
	}
	uploadedKeys, err := s.publishArchive(ctx, tempPath, upload, &manifest)
	if err != nil {
		for _, key := range uploadedKeys {
			_ = s.objects.Delete(ctx, key)
		}
		return fail(err)
	}
	err = s.store.ActivateRevision(ctx, Activation{
		UploadID: upload.ID, SiteID: upload.SiteID, PublicSiteID: upload.PublicSiteID,
		RevisionID: upload.RevisionID, Manifest: manifest, ArchiveSHA: actualSHA,
		SPAFallback: upload.SPAFallback, ActivatedAt: s.now().UTC(),
	})
	if err != nil {
		for _, key := range uploadedKeys {
			_ = s.objects.Delete(ctx, key)
		}
		return fail(fmt.Errorf("activate static site revision: %w", err))
	}
	return nil
}

func (s *Service) publishArchive(ctx context.Context, archivePath string, upload Upload, manifest *Manifest) ([]string, error) {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return nil, fmt.Errorf("open validated archive: %w", err)
	}
	defer reader.Close()
	entries := make(map[string]*zip.File, len(reader.File))
	for _, entry := range reader.File {
		entries[normArchiveName(entry.Name)] = entry
	}
	paths := make([]string, 0, len(manifest.Files))
	for name := range manifest.Files {
		paths = append(paths, name)
	}
	sort.Strings(paths)
	uploaded := make([]string, 0, len(paths))
	for _, name := range paths {
		metadata := manifest.Files[name]
		entry := entries[name]
		if entry == nil {
			return uploaded, fmt.Errorf("validated archive entry %q disappeared", name)
		}
		source, err := entry.Open()
		if err != nil {
			return uploaded, fmt.Errorf("open archive entry %q: %w", name, err)
		}
		hasher := sha256.New()
		key := objectKey(upload.SiteID, upload.RevisionID, name)
		limited := &io.LimitedReader{R: source, N: metadata.Size + 1}
		err = s.objects.Put(ctx, key, io.TeeReader(limited, hasher), metadata.Size, metadata.ContentType)
		closeErr := source.Close()
		if err != nil {
			_ = s.objects.Delete(ctx, key)
			return uploaded, fmt.Errorf("publish archive entry %q: %w", name, err)
		}
		if limited.N != 1 {
			_ = s.objects.Delete(ctx, key)
			return uploaded, fmt.Errorf("publish archive entry %q: object store did not consume the exact file size", name)
		}
		if closeErr != nil {
			return uploaded, fmt.Errorf("close archive entry %q: %w", name, closeErr)
		}
		metadata.ETag = `"` + hex.EncodeToString(hasher.Sum(nil)) + `"`
		manifest.Files[name] = metadata
		uploaded = append(uploaded, key)
	}
	return uploaded, nil
}

func (s *Service) GetStatus(ctx context.Context, siteID, workspaceID, agentID string) (SiteStatus, error) {
	if !s.Available() {
		return SiteStatus{}, ErrUnavailable
	}
	status, err := s.store.GetStatus(ctx, siteID, workspaceID, agentID)
	if err != nil {
		return SiteStatus{}, err
	}
	status.SiteURL = s.siteURL(status.PublicSiteID)
	return status, nil
}

func (s *Service) ResolvePublic(ctx context.Context, publicSiteID string) (ResolvedSite, error) {
	if !s.Available() {
		return ResolvedSite{}, ErrUnavailable
	}
	return s.store.ResolvePublic(ctx, publicSiteID)
}

func (s *Service) siteURL(publicSiteID string) string {
	return strings.TrimRight(s.config.SitePublicURL, "/") + "/sites/" + publicSiteID + "/"
}

func objectKey(siteID, revisionID, name string) string {
	return path.Join("hosted-sites", siteID, "revisions", revisionID, name)
}

func newUploadToken() (string, []byte, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, fmt.Errorf("generate upload capability: %w", err)
	}
	token := uploadTokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))
	return token, sum[:], nil
}

func parseUploadToken(token string) ([]byte, bool) {
	token = strings.TrimSpace(token)
	if !strings.HasPrefix(token, uploadTokenPrefix) {
		return nil, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, uploadTokenPrefix))
	if err != nil || len(raw) != 32 {
		return nil, false
	}
	sum := sha256.Sum256([]byte(token))
	return sum[:], true
}

func randomOpaqueID() string {
	raw := make([]byte, 18)
	if _, err := rand.Read(raw); err != nil {
		return strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

func validBaseURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	if parsed.Scheme == "https" {
		return true
	}
	return parsed.Scheme == "http" && (parsed.Hostname() == "localhost" || net.ParseIP(parsed.Hostname()).IsLoopback())
}

func publicFailureReason(err error) string {
	if err == nil {
		return "static site publication failed"
	}
	message := err.Error()
	if len(message) > 500 {
		message = message[:500]
	}
	return message
}

func normArchiveName(name string) string {
	normalized, err := normalizeArchivePath(name)
	if err != nil {
		return ""
	}
	return normalized
}
