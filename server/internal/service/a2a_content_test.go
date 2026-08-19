package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/multica-ai/multica/server/internal/storage"
)

type a2aContentTestStorage struct {
	objects       map[string][]byte
	deleted       []string
	presignCalls  int
	presignTTLs   []time.Duration
	uploadedNames []string
}

func newA2AContentTestStorage() *a2aContentTestStorage {
	return &a2aContentTestStorage{objects: map[string][]byte{}}
}

func (store *a2aContentTestStorage) Upload(_ context.Context, key string, data []byte, _, filename string) (string, error) {
	store.objects[key] = append([]byte(nil), data...)
	store.uploadedNames = append(store.uploadedNames, filename)
	return "https://storage.example/" + key, nil
}

func (store *a2aContentTestStorage) Delete(_ context.Context, key string) {
	delete(store.objects, key)
	store.deleted = append(store.deleted, key)
}

func (store *a2aContentTestStorage) DeleteObject(ctx context.Context, key string) error {
	store.Delete(ctx, key)
	return nil
}

func (store *a2aContentTestStorage) DeleteKeys(ctx context.Context, keys []string) {
	for _, key := range keys {
		store.Delete(ctx, key)
	}
}

func (*a2aContentTestStorage) KeyFromURL(rawURL string) string {
	return strings.TrimPrefix(rawURL, "https://storage.example/")
}

func (*a2aContentTestStorage) ObjectURL(key string) string {
	return "https://storage.example/" + key
}

func (*a2aContentTestStorage) CdnDomain() string { return "storage.example" }

func (store *a2aContentTestStorage) GetReader(_ context.Context, key string) (io.ReadCloser, error) {
	data, ok := store.objects[key]
	if !ok {
		return nil, fmt.Errorf("not found")
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (store *a2aContentTestStorage) PresignGet(_ context.Context, key string, ttl time.Duration) (string, error) {
	store.presignCalls++
	store.presignTTLs = append(store.presignTTLs, ttl)
	return fmt.Sprintf("https://signed.example/%s?generation=%d", key, store.presignCalls), nil
}

type a2aRoundTripperFunc func(*http.Request) (*http.Response, error)

func (function a2aRoundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestMaterializeA2APartsPreservesOrderAndStoresFiles(t *testing.T) {
	store := newA2AContentTestStorage()
	fetches := 0
	client := &http.Client{Transport: a2aRoundTripperFunc(func(request *http.Request) (*http.Response, error) {
		fetches++
		if request.URL.String() != "https://files.example/report.bin" {
			t.Fatalf("unexpected URL fetch: %s", request.URL)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/octet-stream"}},
			Body:       io.NopCloser(strings.NewReader("remote-file")),
			Request:    request,
		}, nil
	})}
	raw := a2a.NewRawPart([]byte("raw-file"))
	raw.Filename = "input.bin"
	raw.MediaType = "application/octet-stream"
	urlPart := a2a.NewFileURLPart(a2a.URL("https://files.example/report.bin"), "")
	parts := a2a.ContentParts{
		a2a.NewTextPart("hello"),
		a2a.NewDataPart(map[string]any{"kind": "calendar", "count": float64(2)}),
		raw,
		urlPart,
	}
	materialized, err := materializeA2AParts(context.Background(), store, client, "a2a/inputs/ing_public", parts)
	if err != nil {
		t.Fatalf("materializeA2AParts() error = %v", err)
	}
	if fetches != 1 || len(materialized) != 4 || len(store.objects) != 2 {
		t.Fatalf("fetches=%d parts=%d objects=%d", fetches, len(materialized), len(store.objects))
	}
	wantKinds := []string{"text", "data", "object", "object"}
	for index, want := range wantKinds {
		if materialized[index].Stored.Kind != want {
			t.Fatalf("part %d kind = %q, want %q", index, materialized[index].Stored.Kind, want)
		}
	}
	for _, part := range materialized[2:] {
		if !strings.HasPrefix(part.Stored.ObjectKey, "a2a/inputs/ing_public/") || strings.Contains(part.Stored.ObjectKey, "workspace") {
			t.Fatalf("unsafe or unstable object key: %q", part.Stored.ObjectKey)
		}
	}
	if materialized[3].Stored.Filename != "report.bin" || materialized[3].Stored.MediaType != "application/octet-stream" {
		t.Fatalf("URL metadata = %#v", materialized[3].Stored)
	}
}

func TestMaterializeA2APartsRejectsFilesWithoutSignedURLSupport(t *testing.T) {
	t.Setenv("LOCAL_UPLOAD_DIR", t.TempDir())
	store := storage.NewLocalStorageFromEnv()
	if store == nil {
		t.Fatal("NewLocalStorageFromEnv returned nil")
	}
	raw := a2a.NewRawPart([]byte("private"))
	raw.Filename = "private.bin"
	_, err := materializeA2AParts(context.Background(), store, nil, "a2a/inputs/test", a2a.ContentParts{raw})
	if !errors.Is(err, a2a.ErrUnsupportedContentType) {
		t.Fatalf("materializeA2AParts() error = %v, want UnsupportedContentType", err)
	}
}

func TestDecodeStoredA2APartsRefreshesSignedObjectURLs(t *testing.T) {
	store := newA2AContentTestStorage()
	raw := []byte(`[{"kind":"object","objectKey":"a2a/artifacts/task/file.bin","filename":"file.bin","mediaType":"application/octet-stream","sizeBytes":4}]`)
	first, err := decodeStoredA2AParts(context.Background(), store, raw)
	if err != nil {
		t.Fatal(err)
	}
	second, err := decodeStoredA2AParts(context.Background(), store, raw)
	if err != nil {
		t.Fatal(err)
	}
	if first[0].URL() == second[0].URL() || store.presignCalls != 2 {
		t.Fatalf("signed URLs were not refreshed: first=%q second=%q calls=%d", first[0].URL(), second[0].URL(), store.presignCalls)
	}
	for _, ttl := range store.presignTTLs {
		if ttl != 15*time.Minute {
			t.Fatalf("signed URL TTL = %s, want 15m", ttl)
		}
	}
}

func TestValidateA2APartsLimitsAndUnsafeURLs(t *testing.T) {
	tooMany := make(a2a.ContentParts, a2aMaxMessageParts+1)
	for index := range tooMany {
		tooMany[index] = a2a.NewTextPart("x")
	}
	tests := []struct {
		name  string
		parts a2a.ContentParts
	}{
		{name: "empty", parts: nil},
		{name: "too many", parts: tooMany},
		{name: "raw total", parts: a2a.ContentParts{a2a.NewRawPart(make([]byte, a2aMaxRawBytes+1))}},
		{name: "data total", parts: a2a.ContentParts{a2a.NewDataPart(strings.Repeat("x", a2aMaxDataBytes+1))}},
		{name: "invalid data", parts: a2a.ContentParts{a2a.NewDataPart(math.Inf(1))}},
		{name: "traversal filename", parts: a2a.ContentParts{&a2a.Part{Content: a2a.Text("x"), Filename: "../secret"}}},
		{name: "loopback URL", parts: a2a.ContentParts{a2a.NewFileURLPart(a2a.URL("https://127.0.0.1/file"), "application/octet-stream")}},
		{name: "metadata URL", parts: a2a.ContentParts{a2a.NewFileURLPart(a2a.URL("https://169.254.169.254/latest/meta-data"), "application/octet-stream")}},
		{name: "private URL", parts: a2a.ContentParts{a2a.NewFileURLPart(a2a.URL("https://10.0.0.1/file"), "application/octet-stream")}},
		{name: "HTTP URL", parts: a2a.ContentParts{a2a.NewFileURLPart(a2a.URL("http://example.com/file"), "application/octet-stream")}},
		{name: "URL credentials", parts: a2a.ContentParts{a2a.NewFileURLPart(a2a.URL("https://user:password@example.com/file"), "application/octet-stream")}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateA2AParts(test.parts); err == nil {
				t.Fatal("invalid parts were accepted")
			}
		})
	}
}

func TestBlockedA2AAddressCoversNonPublicRanges(t *testing.T) {
	for _, raw := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "100.64.0.1", "0.0.0.1", "::1", "fe80::1", "2001:db8::1"} {
		address, err := netip.ParseAddr(raw)
		if err != nil {
			t.Fatal(err)
		}
		if !blockedA2AAddress(address) {
			t.Fatalf("address %s was not blocked", raw)
		}
	}
}
