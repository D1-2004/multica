package handler

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/gitrepo"
	skillpkg "github.com/multica-ai/multica/server/internal/skill"
)

func (h *Handler) fetchRepositorySkill(ctx context.Context, workspace pgtype.UUID, request ImportSkillRequest, source importSource, rawURL string) (*importedSkill,error) {
	repositoryURL, requestedName := rawURL, ""
	if source == sourceSkillsSh {
		owner, repository, name, err := parseSkillsShParts(rawURL)
		if err != nil { return nil,err }
		repositoryURL, requestedName = "https://github.com/"+owner+"/"+repository, name
	}
	access, err := h.gitRepositories().Open(ctx,workspace,repositoryURL,request.ConnectionID)
	if err != nil { return nil,err }
	return readRepositorySkill(ctx,access.Remote,access.Address,request.Ref,request.Path,requestedName)
}

func readRepositorySkill(ctx context.Context, remote gitrepo.Remote, address gitrepo.Address, ref, directory, requestedName string) (*importedSkill,error) {
	if address.LinkKind != "" {
		if ref != "" || directory != "" { return nil,fmt.Errorf("use either a repository URL with ref/path or a file/tree URL") }
		first, rest, _ := strings.Cut(address.LinkPath,"/")
		if gitrepo.IsCommitSHA(first) { ref,directory = first,rest } else {
			branches, err := remote.ListBranches(ctx)
			if err != nil { return nil,err }
			tags, err := remote.ListTags(ctx)
			if err != nil { return nil,err }
			var matches []string
			for _, branch := range branches { if address.LinkPath == branch.Name || strings.HasPrefix(address.LinkPath,branch.Name+"/") { matches = append(matches,branch.Name) } }
			for _, tag := range tags { if address.LinkPath == tag.Name || strings.HasPrefix(address.LinkPath,tag.Name+"/") { matches = append(matches,tag.Name) } }
			sort.Slice(matches,func(i,j int) bool { return len(matches[i]) > len(matches[j]) })
			if len(matches) == 0 { return nil,fmt.Errorf("repository URL revision was not found") }
			ref = matches[0]
			if len(matches)>1 && len(matches[1])==len(ref) { return nil,fmt.Errorf("branch and tag share this name; provide repository URL and an explicit refs/heads/ or refs/tags/ ref") }
			directory = strings.TrimPrefix(strings.TrimPrefix(address.LinkPath,ref),"/")
		}
		if address.LinkKind == "blob" && path.Base(directory) != "SKILL.md" { return nil,fmt.Errorf("skill file URL must point to SKILL.md") }
	}
	if ref == "" { ref = "refs/heads/"+remote.Info().DefaultBranch }
	if !gitrepo.ValidRef(ref) { return nil,fmt.Errorf("invalid Git ref") }
	sha, err := remote.ResolveCommit(ctx,ref)
	if err != nil { return nil,err }
	if !gitrepo.IsCommitSHA(sha) { return nil,fmt.Errorf("repository did not return an immutable commit") }
	tree, err := remote.GetTree(ctx,sha)
	if err != nil { return nil,err }
	if tree.Truncated { return nil,fmt.Errorf("repository tree is incomplete") }
	directory = strings.TrimSuffix(directory,"/SKILL.md")
	if directory == "SKILL.md" || directory == "." { directory = "" }
	if directory != "" && (path.Clean(directory)!=directory || strings.HasPrefix(directory,"/") || strings.Contains(directory,"\\") || strings.Contains(directory,"../")) { return nil,fmt.Errorf("invalid skill directory") }
	var candidates []gitrepo.TreeEntry
	for _, entry := range tree.Entries {
		if entry.Type == "blob" && path.Base(entry.Path)=="SKILL.md" {
			if directory != "" && entry.Path != directory+"/SKILL.md" { continue }
			if requestedName != "" && path.Base(path.Dir(entry.Path))!=requestedName { continue }
			candidates=append(candidates,entry)
		}
	}
	if len(candidates) != 1 { return nil,fmt.Errorf("expected one SKILL.md, found %d; specify the skill directory",len(candidates)) }
	manifest := candidates[0]
	if manifest.Mode != "100644" && manifest.Mode != "100755" { return nil,fmt.Errorf("SKILL.md must be a regular file") }
	body, err := remote.GetBlob(ctx,manifest.SHA)
	if err != nil { return nil,err }
	if len(body)>maxImportFileSize { return nil,fmt.Errorf("%w: SKILL.md exceeds file limit",errImportCapExceeded) }
	if !utf8.Valid(body) || strings.ContainsRune(string(body),0) { return nil,fmt.Errorf("SKILL.md must contain UTF-8 text") }
	name, description := skillpkg.ParseSkillFrontmatter(string(body))
	if name == "" { name = path.Base(path.Dir(manifest.Path)); if name == "." { name = path.Base(address.Repository) } }
	if requestedName != "" && name != requestedName { return nil,fmt.Errorf("SKILL.md name does not match requested skill %s",requestedName) }
	result := &importedSkill{name:name,description:description,content:string(body),origin:map[string]any{"type":"git","repository":address.URL,"source_url":address.URL,"ref":ref,"commit_sha":sha,"path":manifest.Path}}
	prefix := strings.TrimSuffix(manifest.Path,"SKILL.md")
	for _, entry := range tree.Entries {
		if !strings.HasPrefix(entry.Path,prefix) || entry.Path==manifest.Path || entry.Type=="tree" { continue }
		relative := strings.TrimPrefix(entry.Path,prefix)
		if path.Clean(relative)!=relative || strings.HasPrefix(relative,"/") || strings.HasPrefix(relative,"../") || strings.Contains(relative,"\\") { return nil,fmt.Errorf("unsafe skill path") }
		if entry.Type!="blob" || (entry.Mode!="100644" && entry.Mode!="100755") { return nil,fmt.Errorf("skill contains a symlink or submodule: %s",relative) }
		if isLikelyBinaryFilePath(relative) { continue }
		if entry.Size>maxImportFileSize || len(result.files)>=maxImportFileCount { return nil,fmt.Errorf("%w: skill exceeds import limits",errImportCapExceeded) }
		content, err := remote.GetBlob(ctx,entry.SHA)
		if err != nil { return nil,fmt.Errorf("read skill file %s: %w",relative,err) }
		if len(content)>maxImportFileSize { return nil,fmt.Errorf("%w: file %s exceeds import limit",errImportCapExceeded,relative) }
		if !utf8.Valid(content) || strings.ContainsRune(string(content),0) { return nil,fmt.Errorf("skill file %s must contain UTF-8 text",relative) }
		if err := result.addFile(relative,string(content)); err != nil { return nil,err }
	}
	return result,nil
}
