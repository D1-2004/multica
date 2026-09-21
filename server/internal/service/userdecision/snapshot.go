package userdecision

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/multica-ai/multica/server/internal/storage"
	"io"
)

const maxSnapshotBytes = 8 << 20

type snapshotRef struct {
	Key    string `json:"blob_key"`
	SHA256 string `json:"sha256"`
}

func freezeSnapshot(ctx context.Context, blobs storage.Storage, r Request) (json.RawMessage, error) {
	if len(r.Snapshot) > maxSnapshotBytes {
		return nil, errors.New("decision snapshot too large")
	}
	if len(r.Snapshot) <= 64<<10 {
		return r.Snapshot, nil
	}
	if blobs == nil {
		return nil, errors.New("decision snapshot storage unavailable")
	}
	hash := sha256.Sum256(r.Snapshot)
	digest := hex.EncodeToString(hash[:])
	key := "coordinator-decisions/" + r.WorkspaceID + "/" + r.ID + "/" + digest + ".json"
	if _, err := blobs.Upload(ctx, key, r.Snapshot, "application/json", "snapshot.json"); err != nil {
		return nil, err
	}
	return json.Marshal(snapshotRef{Key: key, SHA256: digest})
}
func (s *Store) Snapshot(ctx context.Context, r Request) (json.RawMessage, error) {
	var ref snapshotRef
	if json.Unmarshal(r.Snapshot, &ref) != nil {
		return nil, errors.New("invalid snapshot")
	}
	if ref.Key == "" {
		return r.Snapshot, nil
	}
	if s.Blobs == nil {
		return nil, errors.New("snapshot store unavailable")
	}
	reader, err := s.Blobs.GetReader(ctx, ref.Key)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	body, err := io.ReadAll(io.LimitReader(reader, maxSnapshotBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxSnapshotBytes {
		return nil, errors.New("snapshot exceeds limit")
	}
	hash := sha256.Sum256(body)
	if hex.EncodeToString(hash[:]) != ref.SHA256 {
		return nil, errors.New("snapshot digest mismatch")
	}
	return body, nil
}
