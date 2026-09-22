package userdecision

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/storage"
)

type snapshotFixtureStorage struct {
	storage.Storage
	body      []byte
	key       string
	uploadErr error
	readErr   error
}

func (s *snapshotFixtureStorage) Upload(_ context.Context, key string, body []byte, _, _ string) (string, error) {
	if s.uploadErr != nil {
		return "", s.uploadErr
	}
	s.key, s.body = key, bytes.Clone(body)
	return key, nil
}
func (s *snapshotFixtureStorage) GetReader(_ context.Context, key string) (io.ReadCloser, error) {
	if s.readErr != nil {
		return nil, s.readErr
	}
	if key != s.key {
		return nil, errors.New("unexpected object")
	}
	return io.NopCloser(bytes.NewReader(s.body)), nil
}

func TestSnapshotUnavailableOrCorruptNeverReturnsPartialContext(t *testing.T) {
	ctx := context.Background()
	body, _ := json.Marshal(map[string]string{"context": strings.Repeat("test ", 16000)})
	r := Request{ID: "decision", WorkspaceID: "workspace", Snapshot: body}
	blobs := &snapshotFixtureStorage{}
	ref, err := freezeSnapshot(ctx, blobs, r)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(ref, body) {
		t.Fatal("large context was not externalized")
	}
	r.Snapshot = ref
	store := &Store{Blobs: blobs}
	got, err := store.Snapshot(ctx, r)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("snapshot round trip failed: %v", err)
	}
	blobs.readErr = errors.New("injected read failure")
	if got, err = store.Snapshot(ctx, r); err == nil || got != nil {
		t.Fatal("read failure returned context")
	}
	blobs.readErr = nil
	blobs.body[10] ^= 1
	if got, err = store.Snapshot(ctx, r); err == nil || got != nil {
		t.Fatal("corrupt object returned context")
	}
	blobs.body = bytes.Repeat([]byte{'x'}, maxSnapshotBytes+1)
	if got, err = store.Snapshot(ctx, r); err == nil || got != nil {
		t.Fatal("oversized object returned context")
	}
	store.Blobs = nil
	if got, err = store.Snapshot(ctx, r); err == nil || got != nil {
		t.Fatal("missing object store returned context")
	}
	// A later successful read uses the original immutable reference.
	store.Blobs, blobs.body = blobs, body
	got, err = store.Snapshot(ctx, r)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("read retry failed: %v", err)
	}
}

func TestSnapshotUploadFailureDoesNotProduceReference(t *testing.T) {
	body, _ := json.Marshal(map[string]string{"context": strings.Repeat("test ", 16000)})
	r := Request{ID: "decision", WorkspaceID: "workspace", Snapshot: body}
	blobs := &snapshotFixtureStorage{uploadErr: errors.New("injected upload failure")}
	for _, target := range []storage.Storage{nil, blobs} {
		if ref, err := freezeSnapshot(context.Background(), target, r); err == nil || ref != nil {
			t.Fatal("failed upload produced reference")
		}
	}
}
