package agentsource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path"
	"sort"

	"github.com/multica-ai/multica/server/internal/githubapp"
)

// CompileFS compiles an Agent bundle from a local or embedded filesystem using
// the same parser, limits, validation, and stable hash as GitHub sources.
func CompileFS(ctx context.Context, sourceFS fs.FS) (Bundle, error) {
	client := &fsRepositoryClient{sourceFS: sourceFS, blobs: make(map[string][]byte)}
	if err := fs.WalkDir(sourceFS, ".", func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if filePath == "." {
			return nil
		}
		cleaned := path.Clean(filePath)
		if cleaned != filePath || entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("unsupported local bundle path %q", filePath)
		}
		if entry.IsDir() {
			client.entries = append(client.entries, githubapp.TreeEntry{Path: filePath, Type: "tree", Mode: "040000"})
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("unsupported local bundle file %q", filePath)
		}
		content, err := fs.ReadFile(sourceFS, filePath)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(append([]byte(filePath+"\x00"), content...))
		blobID := hex.EncodeToString(sum[:])
		client.blobs[blobID] = content
		client.entries = append(client.entries, githubapp.TreeEntry{
			Path: filePath, Type: "blob", Mode: "100644", SHA: blobID, Size: int64(len(content)),
		})
		return nil
	}); err != nil {
		return Bundle{}, fmt.Errorf("scan local agent bundle: %w", err)
	}
	sort.Slice(client.entries, func(i, j int) bool { return client.entries[i].Path < client.entries[j].Path })
	return Compile(ctx, client, Source{})
}

type fsRepositoryClient struct {
	sourceFS fs.FS
	entries  []githubapp.TreeEntry
	blobs    map[string][]byte
}

func (c *fsRepositoryClient) GetTree(context.Context, int64, string, string, string) (githubapp.Tree, error) {
	return githubapp.Tree{Entries: c.entries}, nil
}

func (c *fsRepositoryClient) GetBlob(_ context.Context, _ int64, _, _, blobID string) ([]byte, error) {
	content, ok := c.blobs[blobID]
	if !ok {
		return nil, fmt.Errorf("local bundle blob %q not found", blobID)
	}
	return append([]byte(nil), content...), nil
}
