package dshplugin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/storage"
)

type recoveredArchiveStore struct {
	storage.Storage
	key     string
	data    []byte
	uploads int
}

func (s *recoveredArchiveStore) GetReader(context.Context, string) (io.ReadCloser, error) {
	if len(s.data) == 0 {
		return nil, errors.New("missing archive")
	}
	return io.NopCloser(bytes.NewReader(s.data)), nil
}
func (s *recoveredArchiveStore) Upload(_ context.Context, key string, data []byte, _, _ string) (string, error) {
	s.key, s.data = key, append([]byte{}, data...)
	s.uploads++
	return "stored", nil
}

func TestRecoverPinnedArchiveRequiresOriginalBytesAndReusesStoredPackage(t *testing.T) {
	data := tarball(t, goodPackage())
	registry := serveTarball(t, "demo-plugin", "1.0.0", data, "")
	resolver := testResolver(registry.URL)
	integrity := fmt.Sprintf("sha256-%x", sha256.Sum256(data))
	key, err := StoredArchiveKey("workspace", "demo-plugin", integrity)
	if err != nil {
		t.Fatal(err)
	}
	objects := &recoveredArchiveStore{}
	for range 2 {
		if err := EnsureStoredArchive(context.Background(), objects, resolver, key, "demo-plugin", "1.0.0", integrity, "npm:demo-plugin@1.0.0"); err != nil {
			t.Fatal(err)
		}
	}
	if objects.uploads != 1 || objects.key != key || !bytes.Equal(objects.data, data) {
		t.Fatal("missing package was not recovered exactly once")
	}
	objects = &recoveredArchiveStore{}
	if err := EnsureStoredArchive(context.Background(), objects, resolver, key, "demo-plugin", "1.0.0", "sha256-"+strings.Repeat("0", 64), "npm:demo-plugin@1.0.0"); !errors.Is(err, ErrArchiveIdentity) || objects.uploads != 0 {
		t.Fatal("changed source archive was accepted", err)
	}
}
