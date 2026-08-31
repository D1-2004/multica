package sitehosting

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type memoryStore struct {
	mu      sync.Mutex
	uploads map[string]Upload
	sites   map[string]SiteStatus
	active  map[string]ResolvedSite
	public  map[string]string
	owner   map[string]string
	workspace map[string]string
}

func (m *memoryStore) Prepare(_ context.Context, input PrepareRecord) (Upload, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.uploads == nil {
		m.uploads = map[string]Upload{}
	}
	if m.public == nil {
		m.public = map[string]string{}
		m.owner = map[string]string{}
		m.workspace = map[string]string{}
	}
	upload := input.Upload
	if input.ExistingSiteID == "" {
		m.public[upload.SiteID] = upload.PublicSiteID
		m.owner[upload.SiteID] = upload.OwnerUserID
		m.workspace[upload.SiteID] = upload.WorkspaceID
	} else {
		owner, ok := m.owner[input.ExistingSiteID]
		workspaceID := m.workspace[input.ExistingSiteID]
		if !ok || owner != upload.OwnerUserID || workspaceID != upload.WorkspaceID {
			return Upload{}, ErrSiteForbidden
		}
		upload.PublicSiteID = m.public[input.ExistingSiteID]
	}
	m.uploads[upload.ID] = upload
	return upload, nil
}

func (m *memoryStore) ClaimUpload(_ context.Context, uploadID string, tokenHash []byte, now time.Time) (Upload, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	upload, ok := m.uploads[uploadID]
	if !ok || upload.UsedAt != nil || !upload.ExpiresAt.After(now) || !bytes.Equal(upload.TokenHash, tokenHash) {
		return Upload{}, ErrUploadCapabilityInvalid
	}
	upload.UsedAt = &now
	m.uploads[uploadID] = upload
	return upload, nil
}

func (m *memoryStore) ActivateRevision(_ context.Context, activation Activation) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active == nil {
		m.active = map[string]ResolvedSite{}
	}
	m.active[activation.PublicSiteID] = ResolvedSite{
		SiteID: activation.SiteID, PublicSiteID: activation.PublicSiteID,
		RevisionID: activation.RevisionID, Manifest: activation.Manifest,
		SPAFallback: activation.SPAFallback,
	}
	return nil
}

func (m *memoryStore) FailRevision(context.Context, string, string) error { return nil }

func (m *memoryStore) GetStatus(_ context.Context, siteID, ownerUserID, workspaceID string) (SiteStatus, error) {
	status, ok := m.sites[siteID]
	if !ok || status.OwnerUserID != ownerUserID || status.WorkspaceID != workspaceID {
		return SiteStatus{}, ErrSiteForbidden
	}
	return status, nil
}

func (m *memoryStore) ListSites(_ context.Context, ownerUserID, workspaceID string) ([]SiteStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []SiteStatus
	for _, status := range m.sites {
		if status.OwnerUserID == ownerUserID && status.WorkspaceID == workspaceID && status.Status == "active" {
			result = append(result, status)
		}
	}
	return result, nil
}

func (m *memoryStore) DeleteSite(_ context.Context, siteID, ownerUserID, workspaceID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	status, ok := m.sites[siteID]
	if !ok || status.OwnerUserID != ownerUserID || status.WorkspaceID != workspaceID || status.Status != "active" {
		return ErrSiteForbidden
	}
	status.Status = "deleted"
	m.sites[siteID] = status
	return nil
}

func (m *memoryStore) ResolvePublic(_ context.Context, publicSiteID string) (ResolvedSite, error) {
	resolved, ok := m.active[publicSiteID]
	if !ok {
		return ResolvedSite{}, ErrSiteNotFound
	}
	return resolved, nil
}

type memoryObjectStore struct {
	mu      sync.Mutex
	objects map[string][]byte
	failKey string
}

func (m *memoryObjectStore) Put(_ context.Context, key string, reader io.Reader, size int64, _ string) error {
	if key == m.failKey {
		return errors.New("injected object failure")
	}
	data, err := io.ReadAll(io.LimitReader(reader, size+1))
	if err != nil {
		return err
	}
	if int64(len(data)) != size {
		return errors.New("object size mismatch")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.objects == nil {
		m.objects = map[string][]byte{}
	}
	m.objects[key] = data
	return nil
}

func (m *memoryObjectStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.objects[key]
	if !ok {
		return nil, os.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (m *memoryObjectStore) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objects, key)
	return nil
}

func zipBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	zw := zip.NewWriter(&buffer)
	for name, body := range files {
		writer, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(writer, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func newTestService(store Store, objects ObjectStore) *Service {
	service := NewService(store, objects, Config{
		APIBaseURL:    "https://api.example.test",
		SitePublicURL: "https://sites.example.test",
		TempDir:       filepath.Clean(os.TempDir()),
	})
	service.now = func() time.Time { return time.Unix(1_800_000_000, 0).UTC() }
	return service
}

func TestPrepareUsesUserOwnershipAcrossCallingClients(t *testing.T) {
	store := &memoryStore{}
	service := newTestService(store, &memoryObjectStore{})
	first, err := service.Prepare(context.Background(), PrepareInput{
		OwnerUserID: "11111111-1111-1111-1111-111111111111",
		WorkspaceID: "22222222-2222-2222-2222-222222222222",
		ExpectedSHA256: strings.Repeat("a", 64), ExpectedLength: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Prepare(context.Background(), PrepareInput{
		OwnerUserID: "11111111-1111-1111-1111-111111111111",
		WorkspaceID: "22222222-2222-2222-2222-222222222222",
		SiteID: first.SiteID, ExpectedSHA256: strings.Repeat("b", 64), ExpectedLength: 1,
	}); err != nil {
		t.Fatalf("same user through another client cannot update Site: %v", err)
	}
	if _, err := service.Prepare(context.Background(), PrepareInput{
		OwnerUserID: "44444444-4444-4444-4444-444444444444",
		WorkspaceID: "22222222-2222-2222-2222-222222222222",
		SiteID: first.SiteID, ExpectedSHA256: strings.Repeat("c", 64), ExpectedLength: 1,
	}); !errors.Is(err, ErrSiteForbidden) {
		t.Fatalf("different user update error=%v", err)
	}
}

func TestPrepareRequiresWorkspaceAuthority(t *testing.T) {
	service := newTestService(&memoryStore{}, &memoryObjectStore{})
	_, err := service.Prepare(context.Background(), PrepareInput{
		OwnerUserID: "11111111-1111-1111-1111-111111111111",
		ExpectedSHA256: strings.Repeat("a", 64), ExpectedLength: 1,
	})
	if !errors.Is(err, ErrSiteForbidden) {
		t.Fatalf("missing workspace authority error=%v", err)
	}
}

func TestListAndDeleteSitesUseAuthenticatedUser(t *testing.T) {
	ownerUserID := "11111111-1111-1111-1111-111111111111"
	otherUserID := "22222222-2222-2222-2222-222222222222"
	workspaceID := "33333333-3333-3333-3333-333333333333"
	otherWorkspaceID := "44444444-4444-4444-4444-444444444444"
	store := &memoryStore{sites: map[string]SiteStatus{
		"site-a": {
			SiteID: "site-a", PublicSiteID: "public-a", OwnerUserID: ownerUserID,
			WorkspaceID: workspaceID,
			Status: "active", LatestRevisionID: "revision-a", LatestStatus: "active",
		},
		"site-b": {
			SiteID: "site-b", PublicSiteID: "public-b", OwnerUserID: ownerUserID,
			WorkspaceID: otherWorkspaceID,
			Status: "active", LatestRevisionID: "revision-b", LatestStatus: "active",
		},
	}}
	service := newTestService(store, &memoryObjectStore{})

	sites, err := service.ListSites(context.Background(), ownerUserID, workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(sites) != 1 || sites[0].SiteID != "site-a" ||
		sites[0].SiteURL != "https://sites.example.test/sites/public-a/" {
		t.Fatalf("sites=%#v", sites)
	}
	if err := service.DeleteSite(context.Background(), "site-a", otherUserID, workspaceID); !errors.Is(err, ErrSiteForbidden) {
		t.Fatalf("cross-user delete error=%v", err)
	}
	if err := service.DeleteSite(context.Background(), "site-a", ownerUserID, otherWorkspaceID); !errors.Is(err, ErrSiteForbidden) {
		t.Fatalf("cross-workspace delete error=%v", err)
	}
	if err := service.DeleteSite(context.Background(), "site-a", ownerUserID, workspaceID); err != nil {
		t.Fatal(err)
	}
	sites, err = service.ListSites(context.Background(), ownerUserID, workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(sites) != 0 {
		t.Fatalf("deleted site still listed: %#v", sites)
	}
}

func TestPrepareRequiresConfiguredSitePublicURL(t *testing.T) {
	service := NewService(&memoryStore{}, &memoryObjectStore{}, Config{
		APIBaseURL: "https://api.example.test",
		Limits:     DefaultLimits(),
	})
	_, err := service.Prepare(context.Background(), PrepareInput{
		OwnerUserID: "u", ExpectedSHA256: strings.Repeat("a", 64), ExpectedLength: 1,
	})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing Site public URL error=%v", err)
	}
}

func TestPrepareDoesNotExposeAuthorityInURL(t *testing.T) {
	store := &memoryStore{}
	service := newTestService(store, &memoryObjectStore{})
	body := zipBytes(t, map[string]string{"index.html": "hello"})
	sum := sha256.Sum256(body)
	prepared, err := service.Prepare(context.Background(), PrepareInput{
		OwnerUserID: "11111111-1111-1111-1111-111111111111",
		WorkspaceID: "22222222-2222-2222-2222-222222222222",
		ExpectedSHA256: hex.EncodeToString(sum[:]), ExpectedLength: int64(len(body)),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{prepared.SiteID, prepared.RevisionID, "11111111-1111-1111-1111-111111111111"} {
		if strings.Contains(prepared.SiteURL, forbidden) {
			t.Fatalf("site URL leaks %q: %s", forbidden, prepared.SiteURL)
		}
	}
	if strings.Contains(prepared.UploadURL, prepared.UploadToken) {
		t.Fatal("upload token leaked into upload URL")
	}
	if prepared.UploadPath != "/api/sitehosting/uploads/"+prepared.UploadID ||
		prepared.UploadTokenHeader != "X-Multica-Site-Upload-Token" {
		t.Fatalf("upload protocol path=%q header=%q", prepared.UploadPath, prepared.UploadTokenHeader)
	}
}

func TestUploadPublishesMultipleFilesAndRejectsTokenReuse(t *testing.T) {
	store := &memoryStore{}
	objects := &memoryObjectStore{}
	service := newTestService(store, objects)
	body := zipBytes(t, map[string]string{
		"index.html": "<script src=assets/app.js></script>",
		"assets/app.js": strings.Repeat("x", 192*1024),
		"assets/site.css": "body{margin:0}",
	})
	sum := sha256.Sum256(body)
	prepared, err := service.Prepare(context.Background(), PrepareInput{
		OwnerUserID: "u", WorkspaceID: "w", ExpectedSHA256: hex.EncodeToString(sum[:]), ExpectedLength: int64(len(body)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Upload(context.Background(), prepared.UploadID, prepared.UploadToken, bytes.NewReader(body), int64(len(body))); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if len(objects.objects) != 3 {
		t.Fatalf("objects=%d", len(objects.objects))
	}
	if err := service.Upload(context.Background(), prepared.UploadID, prepared.UploadToken, bytes.NewReader(body), int64(len(body))); !errors.Is(err, ErrUploadCapabilityInvalid) {
		t.Fatalf("reused token error=%v", err)
	}
}

func TestUploadRejectsExpiredCapability(t *testing.T) {
	store := &memoryStore{}
	service := newTestService(store, &memoryObjectStore{})
	body := zipBytes(t, map[string]string{"index.html": "hello"})
	sum := sha256.Sum256(body)
	prepared, err := service.Prepare(context.Background(), PrepareInput{
		OwnerUserID: "u", WorkspaceID: "w", ExpectedSHA256: hex.EncodeToString(sum[:]), ExpectedLength: int64(len(body)),
	})
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return prepared.ExpiresAt.Add(time.Second) }
	err = service.Upload(context.Background(), prepared.UploadID, prepared.UploadToken, bytes.NewReader(body), int64(len(body)))
	if !errors.Is(err, ErrUploadCapabilityInvalid) {
		t.Fatalf("expired token error=%v", err)
	}
}

func TestUploadRejectsLengthAndSHAWithoutActivation(t *testing.T) {
	tests := []struct {
		name   string
		body   []byte
		length int64
		sha    string
	}{
		{name: "length", body: zipBytes(t, map[string]string{"index.html": "ok"}), length: 1},
		{name: "sha", body: zipBytes(t, map[string]string{"index.html": "ok"}), sha: strings.Repeat("0", 64)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := &memoryStore{}
			service := newTestService(store, &memoryObjectStore{})
			length := tc.length
			if length == 0 {
				length = int64(len(tc.body))
			}
			sha := tc.sha
			if sha == "" {
				sum := sha256.Sum256(tc.body)
				sha = hex.EncodeToString(sum[:])
			}
			prepared, err := service.Prepare(context.Background(), PrepareInput{OwnerUserID: "u", WorkspaceID: "w", ExpectedSHA256: sha, ExpectedLength: length})
			if err != nil {
				t.Fatal(err)
			}
			err = service.Upload(context.Background(), prepared.UploadID, prepared.UploadToken, bytes.NewReader(tc.body), int64(len(tc.body)))
			if err == nil {
				t.Fatal("mismatched upload accepted")
			}
			if len(store.active) != 0 {
				t.Fatal("failed upload activated a revision")
			}
		})
	}
}

func TestUploadObjectFailureLeavesPreviousRevisionActive(t *testing.T) {
	store := &memoryStore{}
	objects := &memoryObjectStore{}
	service := newTestService(store, objects)
	oldBody := zipBytes(t, map[string]string{"index.html": "old"})
	oldSum := sha256.Sum256(oldBody)
	oldPrepared, err := service.Prepare(context.Background(), PrepareInput{OwnerUserID: "u", WorkspaceID: "w", ExpectedSHA256: hex.EncodeToString(oldSum[:]), ExpectedLength: int64(len(oldBody))})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Upload(context.Background(), oldPrepared.UploadID, oldPrepared.UploadToken, bytes.NewReader(oldBody), int64(len(oldBody))); err != nil {
		t.Fatal(err)
	}
	body := zipBytes(t, map[string]string{"index.html": "new", "app.js": "broken"})
	sum := sha256.Sum256(body)
	prepared, err := service.Prepare(context.Background(), PrepareInput{OwnerUserID: "u", WorkspaceID: "w", SiteID: oldPrepared.SiteID, ExpectedSHA256: hex.EncodeToString(sum[:]), ExpectedLength: int64(len(body))})
	if err != nil {
		t.Fatal(err)
	}
	objects.failKey = objectKey(prepared.SiteID, prepared.RevisionID, "app.js")
	if err := service.Upload(context.Background(), prepared.UploadID, prepared.UploadToken, bytes.NewReader(body), int64(len(body))); err == nil {
		t.Fatal("object failure was ignored")
	}
	publicID := oldPrepared.PublicSiteIDForTest()
	if store.active[publicID].RevisionID != oldPrepared.RevisionID {
		t.Fatal("failed revision replaced the previous active revision")
	}
}
