package agentsource

import (
	"context"
	"sort"

	"github.com/multica-ai/multica/server/internal/githubapp"
)

// RepositorySnapshot is an internal record of a pinned Git read. It is never
// accepted as an upload or a public creation request.
type RepositorySnapshot struct {
	Definition Bundle `json:"definition"`
	Files map[string]string `json:"files"`
	Tree []githubapp.TreeEntry `json:"tree"`
}

type FileChange struct {
	Path string `json:"path"`
	Status string `json:"status"`
	Before *string `json:"before"`
	After *string `json:"after"`
	BeforeSHA string `json:"before_sha,omitempty"`
	AfterSHA string `json:"after_sha,omitempty"`
	BeforeMode string `json:"before_mode,omitempty"`
	AfterMode string `json:"after_mode,omitempty"`
}

type recordingRepository struct {
	RepositoryClient
	tree githubapp.Tree
	blobs map[string]string
}

func (r *recordingRepository) GetTree(ctx context.Context, installationID int64, owner, repo, sha string) (githubapp.Tree, error) {
	tree, err := r.RepositoryClient.GetTree(ctx, installationID, owner, repo, sha)
	if err == nil { r.tree = tree }
	return tree, err
}

func (r *recordingRepository) GetBlob(ctx context.Context, installationID int64, owner, repo, sha string) ([]byte, error) {
	content, err := r.RepositoryClient.GetBlob(ctx, installationID, owner, repo, sha)
	if err == nil && isText(content) && !isLFSPointer(content) { r.blobs[sha] = string(content) }
	return content, err
}

func ReadDTARepository(ctx context.Context, client RepositoryClient, source Source) (RepositorySnapshot, error) {
	recorder := &recordingRepository{RepositoryClient:client, blobs:map[string]string{}}
	definition, err := CompileDTAProject(ctx, recorder, source)
	if err != nil { return RepositorySnapshot{}, err }
	files := map[string]string{}
	for _, entry := range recorder.tree.Entries {
		if content, ok := recorder.blobs[entry.SHA]; ok { files[entry.Path] = content }
	}
	return RepositorySnapshot{Definition:definition, Files:files, Tree:recorder.tree.Entries}, nil
}

// DiffRepository compares the two complete trees, rather than their merge base,
// so switching to an older or diverged branch includes reverted/deleted files.
// Text is included for files read by the importer; other files still have Git
// object/mode changes, without pretending a missing text patch is an empty file.
func DiffRepository(before, after RepositorySnapshot) []FileChange {
	oldEntries := map[string]githubapp.TreeEntry{}
	newEntries := map[string]githubapp.TreeEntry{}
	paths := map[string]struct{}{}
	for _, entry := range before.Tree {
		if entry.Type != "tree" { oldEntries[entry.Path] = entry; paths[entry.Path] = struct{}{} }
	}
	for _, entry := range after.Tree {
		if entry.Type != "tree" { newEntries[entry.Path] = entry; paths[entry.Path] = struct{}{} }
	}
	changes := []FileChange{}
	for path := range paths {
		old, had := oldEntries[path]
		next, has := newEntries[path]
		if had && has && old.SHA == next.SHA && old.Mode == next.Mode { continue }
		change := FileChange{Path:path, Status:changeStatus(had, has), BeforeSHA:old.SHA, AfterSHA:next.SHA, BeforeMode:old.Mode, AfterMode:next.Mode}
		if content, ok := before.Files[path]; ok { change.Before = &content }
		if content, ok := after.Files[path]; ok { change.After = &content }
		changes = append(changes, change)
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	return changes
}

func DiffFiles(before, after map[string]string) []FileChange {
	paths := map[string]struct{}{}
	for path := range before { paths[path] = struct{}{} }
	for path := range after { paths[path] = struct{}{} }
	changes := []FileChange{}
	for path := range paths {
		old, had := before[path]
		next, has := after[path]
		if had && has && old == next { continue }
		change := FileChange{Path:path, Status:changeStatus(had, has)}
		if had { change.Before = &old }
		if has { change.After = &next }
		changes = append(changes, change)
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	return changes
}

func changeStatus(had, has bool) string {
	if !had { return "added" }
	if !has { return "deleted" }
	return "modified"
}
