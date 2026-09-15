package agentsource

import (
	"context"
	"errors"
	"sort"

	"github.com/multica-ai/multica/server/internal/gitrepo"
)

// RepositorySnapshot is an internal record of a pinned Git read. It is never
// accepted as an upload or a public creation request.
type RepositorySnapshot struct {
	Definition Bundle `json:"definition"`
	Files map[string]string `json:"files"`
	Tree []gitrepo.TreeEntry `json:"tree"`
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
	gitrepo.Reader
	tree gitrepo.Tree
	treeLoaded bool
	blobs map[string]string
}

func (r *recordingRepository) GetTree(ctx context.Context, sha string) (gitrepo.Tree, error) {
	if r.treeLoaded { return r.tree, nil }
	tree, err := r.Reader.GetTree(ctx, sha)
	if err == nil { r.tree = tree; r.treeLoaded = true }
	return tree, err
}

func (r *recordingRepository) GetBlob(ctx context.Context, sha string) ([]byte, error) {
	content, err := r.Reader.GetBlob(ctx, sha)
	if err == nil && isText(content) && !isLFSPointer(content) { r.blobs[sha] = string(content) }
	return content, err
}

// ReadAgentRepository uses the same agent.json protocol as uploaded ZIPs.
func ReadAgentRepository(ctx context.Context, client gitrepo.Reader, source Source) (RepositorySnapshot, error) {
	return readRepository(ctx, client, source, true)
}

// ReadDTARepository is retained only for pre-existing internal managed templates.
func ReadDTARepository(ctx context.Context, client gitrepo.Reader, source Source) (RepositorySnapshot, error) {
	return readRepository(ctx, client, source, false)
}

func readRepository(ctx context.Context, client gitrepo.Reader, source Source, requireAgentManifest bool) (RepositorySnapshot, error) {
	recorder := &recordingRepository{Reader:client, blobs:map[string]string{}}
	tree, err := recorder.GetTree(ctx, source.CommitSHA)
	if err != nil { return RepositorySnapshot{}, err }
	entries := repositoryEntries(tree)
	_, portable := entries[PortableManifestPath]
	_, dta := entries[DTAProjectPath]
	if portable && dta && !requireAgentManifest { return RepositorySnapshot{}, errors.New("repository must contain only one agent manifest: agent.json or dingtalk-agent.json") }
	if requireAgentManifest && !portable { return RepositorySnapshot{}, errors.New("required file agent.json is missing; use the Multica Agent package schema") }
	var definition Bundle
	if portable { definition, err = compilePortable(ctx, recorder, source) } else { definition, err = CompileDTAProject(ctx, recorder, source) }
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
	oldEntries := map[string]gitrepo.TreeEntry{}
	newEntries := map[string]gitrepo.TreeEntry{}
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
