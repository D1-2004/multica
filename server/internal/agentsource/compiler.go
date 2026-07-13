package agentsource

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/multica-ai/multica/server/internal/githubapp"
	"github.com/multica-ai/multica/server/internal/skill"
	"gopkg.in/yaml.v3"
)

const (
	ManifestPath       = "multica-agent.yaml"
	APIVersion         = "multica.ai/v1alpha1"
	Kind               = "Agent"
	MaxSkills          = 20
	MaxFileSize        = 1 << 20
	MaxSkillFiles      = 200
	MaxSkillSize       = 8 << 20
	MaxBundleSize      = 32 << 20
	MaxAgentNameLength = 100
	MaxDescriptionSize = 4000
)

var knownProviders = map[string]struct{}{
	"antigravity": {}, "claude": {}, "codebuddy": {}, "codex": {},
	"copilot": {}, "cursor": {}, "hermes": {}, "kimi": {},
	"kiro": {}, "openclaw": {}, "opencode": {}, "pi": {},
	"qoder": {}, "traecli": {},
}

type Manifest struct {
	APIVersion string           `yaml:"apiVersion" json:"api_version"`
	Kind       string           `yaml:"kind" json:"kind"`
	Metadata   ManifestMetadata `yaml:"metadata" json:"metadata"`
	Spec       ManifestSpec     `yaml:"spec" json:"spec"`
}

type ManifestMetadata struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description" json:"description"`
}

type ManifestSpec struct {
	Instructions  string                `yaml:"instructions" json:"instructions"`
	Skills        []ManifestSkill       `yaml:"skills" json:"skills"`
	Compatibility ManifestCompatibility `yaml:"compatibility" json:"compatibility"`
}

type ManifestSkill struct {
	Path string `yaml:"path" json:"path"`
}

type ManifestCompatibility struct {
	Providers []string `yaml:"providers" json:"providers"`
}

type Source struct {
	InstallationID int64
	Owner          string
	Repository     string
	CommitSHA      string
}

type File struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type Skill struct {
	SourcePath  string `json:"source_path"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Content     string `json:"content"`
	Files       []File `json:"files"`
}

type Bundle struct {
	Manifest     Manifest `json:"manifest"`
	Instructions string   `json:"instructions"`
	Skills       []Skill  `json:"skills"`
	Warnings     []string `json:"warnings"`
	Hash         string   `json:"hash"`
}

type RepositoryClient interface {
	GetTree(ctx context.Context, installationID int64, owner, repo, sha string) (githubapp.Tree, error)
	GetBlob(ctx context.Context, installationID int64, owner, repo, sha string) ([]byte, error)
}

func ParseManifest(content []byte) (Manifest, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	decoder.KnownFields(true)
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode %s: %w", ManifestPath, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return Manifest{}, errors.New("manifest must contain exactly one YAML document")
		}
		return Manifest{}, fmt.Errorf("decode trailing manifest content: %w", err)
	}
	if err := validateManifest(manifest); err != nil {
		return Manifest{}, err
	}
	if manifest.Spec.Skills == nil {
		manifest.Spec.Skills = []ManifestSkill{}
	}
	if manifest.Spec.Compatibility.Providers == nil {
		manifest.Spec.Compatibility.Providers = []string{}
	}
	return manifest, nil
}

func validateManifest(manifest Manifest) error {
	if manifest.APIVersion != APIVersion {
		return fmt.Errorf("apiVersion must be %q", APIVersion)
	}
	if manifest.Kind != Kind {
		return fmt.Errorf("kind must be %q", Kind)
	}
	name := strings.TrimSpace(manifest.Metadata.Name)
	if name == "" || utf8.RuneCountInString(name) > MaxAgentNameLength {
		return fmt.Errorf("metadata.name must be between 1 and %d characters", MaxAgentNameLength)
	}
	if len(manifest.Metadata.Description) > MaxDescriptionSize {
		return fmt.Errorf("metadata.description exceeds %d bytes", MaxDescriptionSize)
	}
	if err := validateRepositoryPath(manifest.Spec.Instructions, "spec.instructions"); err != nil {
		return err
	}
	if len(manifest.Spec.Skills) > MaxSkills {
		return fmt.Errorf("spec.skills exceeds the %d skill limit", MaxSkills)
	}
	seenPaths := make(map[string]struct{}, len(manifest.Spec.Skills))
	for _, skillRef := range manifest.Spec.Skills {
		if err := validateRepositoryPath(skillRef.Path, "spec.skills.path"); err != nil {
			return err
		}
		if _, exists := seenPaths[skillRef.Path]; exists {
			return fmt.Errorf("duplicate skill path %q", skillRef.Path)
		}
		seenPaths[skillRef.Path] = struct{}{}
	}
	seenProviders := make(map[string]struct{}, len(manifest.Spec.Compatibility.Providers))
	for _, provider := range manifest.Spec.Compatibility.Providers {
		if _, ok := knownProviders[provider]; !ok {
			return fmt.Errorf("unknown compatibility provider %q", provider)
		}
		if _, exists := seenProviders[provider]; exists {
			return fmt.Errorf("duplicate compatibility provider %q", provider)
		}
		seenProviders[provider] = struct{}{}
	}
	return nil
}

func validateRepositoryPath(value, field string) error {
	if value == "" || strings.HasPrefix(value, "/") || strings.Contains(value, "\\") {
		return fmt.Errorf("%s must be a non-empty relative repository path", field)
	}
	cleaned := path.Clean(value)
	if cleaned != value || cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return fmt.Errorf("%s contains an unsafe path", field)
	}
	return nil
}

func Compile(ctx context.Context, client RepositoryClient, source Source) (Bundle, error) {
	tree, err := client.GetTree(ctx, source.InstallationID, source.Owner, source.Repository, source.CommitSHA)
	if err != nil {
		return Bundle{}, err
	}
	entries := make(map[string]githubapp.TreeEntry, len(tree.Entries))
	for _, entry := range tree.Entries {
		entries[entry.Path] = entry
	}
	manifestBytes, err := loadRequiredText(ctx, client, source, entries, ManifestPath)
	if err != nil {
		return Bundle{}, err
	}
	manifest, err := ParseManifest(manifestBytes)
	if err != nil {
		return Bundle{}, err
	}
	instructionsBytes, err := loadRequiredText(ctx, client, source, entries, manifest.Spec.Instructions)
	if err != nil {
		return Bundle{}, err
	}

	bundle := Bundle{
		Manifest:     manifest,
		Instructions: string(instructionsBytes),
		Skills:       make([]Skill, 0, len(manifest.Spec.Skills)),
		Warnings:     []string{},
	}
	totalSize := len(manifestBytes) + len(instructionsBytes)
	seenSkillNames := make(map[string]string, len(manifest.Spec.Skills))
	for _, skillRef := range manifest.Spec.Skills {
		compiled, warnings, size, err := compileSkill(ctx, client, source, entries, skillRef.Path)
		if err != nil {
			return Bundle{}, err
		}
		if previousPath, exists := seenSkillNames[compiled.Name]; exists {
			return Bundle{}, fmt.Errorf("skills %q and %q use the same name %q", previousPath, compiled.SourcePath, compiled.Name)
		}
		seenSkillNames[compiled.Name] = compiled.SourcePath
		bundle.Skills = append(bundle.Skills, compiled)
		bundle.Warnings = append(bundle.Warnings, warnings...)
		totalSize += size
		if totalSize > MaxBundleSize {
			return Bundle{}, fmt.Errorf("agent bundle exceeds the %d byte limit", MaxBundleSize)
		}
	}
	sort.Slice(bundle.Skills, func(i, j int) bool { return bundle.Skills[i].SourcePath < bundle.Skills[j].SourcePath })
	sort.Strings(bundle.Warnings)
	bundle.Hash = hashBundle(bundle)
	return bundle, nil
}

func loadRequiredText(ctx context.Context, client RepositoryClient, source Source, entries map[string]githubapp.TreeEntry, filePath string) ([]byte, error) {
	entry, ok := entries[filePath]
	if !ok || entry.Type != "blob" {
		return nil, fmt.Errorf("required file %q was not found", filePath)
	}
	if err := validateGitObject(entry); err != nil {
		return nil, fmt.Errorf("%s: %w", filePath, err)
	}
	if entry.Size > MaxFileSize {
		return nil, fmt.Errorf("%s exceeds the %d byte file limit", filePath, MaxFileSize)
	}
	content, err := client.GetBlob(ctx, source.InstallationID, source.Owner, source.Repository, entry.SHA)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", filePath, err)
	}
	if len(content) > MaxFileSize {
		return nil, fmt.Errorf("%s exceeds the %d byte file limit", filePath, MaxFileSize)
	}
	if !isText(content) {
		return nil, fmt.Errorf("%s must be UTF-8 text", filePath)
	}
	if isLFSPointer(content) {
		return nil, fmt.Errorf("%s is a Git LFS pointer", filePath)
	}
	return content, nil
}

func compileSkill(ctx context.Context, client RepositoryClient, source Source, entries map[string]githubapp.TreeEntry, sourcePath string) (Skill, []string, int, error) {
	prefix := sourcePath + "/"
	skillMDPath := prefix + "SKILL.md"
	nestedSkillMD := make([]string, 0)
	files := make([]githubapp.TreeEntry, 0)
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Path, prefix) {
			continue
		}
		if entry.Type == "commit" || entry.Mode == "160000" || entry.Mode == "120000" {
			return Skill{}, nil, 0, fmt.Errorf("skill %q contains unsupported git object %q", sourcePath, entry.Path)
		}
		if entry.Type != "blob" {
			continue
		}
		if strings.HasSuffix(entry.Path, "/SKILL.md") && entry.Path != skillMDPath {
			nestedSkillMD = append(nestedSkillMD, entry.Path)
		}
		files = append(files, entry)
	}
	if len(nestedSkillMD) > 0 {
		return Skill{}, nil, 0, fmt.Errorf("skill %q must contain exactly one SKILL.md", sourcePath)
	}
	skillMD, err := loadRequiredText(ctx, client, source, entries, skillMDPath)
	if err != nil {
		return Skill{}, nil, 0, err
	}
	name, description := skill.ParseSkillFrontmatter(string(skillMD))
	if name == "" {
		name = path.Base(sourcePath)
	}
	if strings.TrimSpace(name) == "" || utf8.RuneCountInString(name) > MaxAgentNameLength {
		return Skill{}, nil, 0, fmt.Errorf("skill %q has an invalid name", sourcePath)
	}

	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	if len(files)-1 > MaxSkillFiles {
		return Skill{}, nil, 0, fmt.Errorf("skill %q exceeds the %d supporting file limit", sourcePath, MaxSkillFiles)
	}
	result := Skill{SourcePath: sourcePath, Name: name, Description: description, Content: string(skillMD)}
	warnings := make([]string, 0)
	totalSize := len(skillMD)
	for _, entry := range files {
		if entry.Path == skillMDPath {
			continue
		}
		if err := validateGitObject(entry); err != nil {
			return Skill{}, nil, 0, fmt.Errorf("%s: %w", entry.Path, err)
		}
		if entry.Size > MaxFileSize {
			return Skill{}, nil, 0, fmt.Errorf("%s exceeds the %d byte file limit", entry.Path, MaxFileSize)
		}
		content, err := client.GetBlob(ctx, source.InstallationID, source.Owner, source.Repository, entry.SHA)
		if err != nil {
			return Skill{}, nil, 0, fmt.Errorf("read %s: %w", entry.Path, err)
		}
		if len(content) > MaxFileSize {
			return Skill{}, nil, 0, fmt.Errorf("%s exceeds the %d byte file limit", entry.Path, MaxFileSize)
		}
		if isLFSPointer(content) {
			return Skill{}, nil, 0, fmt.Errorf("%s is a Git LFS pointer", entry.Path)
		}
		if !isText(content) {
			warnings = append(warnings, fmt.Sprintf("skipped binary file %s", entry.Path))
			continue
		}
		totalSize += len(content)
		if totalSize > MaxSkillSize {
			return Skill{}, nil, 0, fmt.Errorf("skill %q exceeds the %d byte bundle limit", sourcePath, MaxSkillSize)
		}
		result.Files = append(result.Files, File{Path: strings.TrimPrefix(entry.Path, prefix), Content: string(content)})
	}
	return result, warnings, totalSize, nil
}

func validateGitObject(entry githubapp.TreeEntry) error {
	if entry.Type == "commit" || entry.Mode == "160000" {
		return errors.New("git submodules are not supported")
	}
	if entry.Mode == "120000" {
		return errors.New("symbolic links are not supported")
	}
	if entry.Type != "blob" {
		return fmt.Errorf("unsupported git object type %q", entry.Type)
	}
	return nil
}

func isText(content []byte) bool {
	return utf8.Valid(content) && !bytes.ContainsRune(content, '\x00')
}

func isLFSPointer(content []byte) bool {
	return bytes.HasPrefix(content, []byte("version https://git-lfs.github.com/spec/v1"))
}

func hashBundle(bundle Bundle) string {
	hash := sha256.New()
	hash.Write([]byte(bundle.Manifest.APIVersion))
	hash.Write([]byte{0})
	hash.Write([]byte(bundle.Manifest.Kind))
	hash.Write([]byte{0})
	hash.Write([]byte(bundle.Manifest.Metadata.Name))
	hash.Write([]byte{0})
	hash.Write([]byte(bundle.Manifest.Metadata.Description))
	hash.Write([]byte{0})
	hash.Write([]byte(bundle.Manifest.Spec.Instructions))
	for _, provider := range bundle.Manifest.Spec.Compatibility.Providers {
		hash.Write([]byte{0})
		hash.Write([]byte(provider))
	}
	hash.Write([]byte{0})
	hash.Write([]byte(bundle.Instructions))
	for _, compiledSkill := range bundle.Skills {
		hash.Write([]byte{0})
		hash.Write([]byte(compiledSkill.SourcePath))
		hash.Write([]byte{0})
		hash.Write([]byte(compiledSkill.Content))
		for _, file := range compiledSkill.Files {
			hash.Write([]byte{0})
			hash.Write([]byte(file.Path))
			hash.Write([]byte{0})
			hash.Write([]byte(file.Content))
		}
	}
	return hex.EncodeToString(hash.Sum(nil))
}
