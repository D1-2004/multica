package agentsource

import (
	"archive/zip"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"unicode/utf8"
)

const PortableManifestPath = "agent.json"
const PortableSchemaPath = "agent.schema.json"
const PortableVersion = "multica.agent/v1"

//go:embed agent.schema.json
var PortableSchema []byte

// PortableManifest contains only repository-owned configuration. Runtime and
// credentials remain workspace-owned and are never serialized here.
type PortableManifest struct {
	Schema string `json:"$schema"`
	Version string `json:"version"`
	Name string `json:"name"`
	Description string `json:"description"`
	Instructions string `json:"instructions"`
	Skills []PortableSkill `json:"skills"`
}

type PortableSkill struct {
	Path string `json:"path"`
	Name string `json:"name"`
	Description string `json:"description"`
	Enabled *bool `json:"enabled"`
}

func (m PortableManifest) internalManifest() Manifest {
	manifest := Manifest{APIVersion:APIVersion, Kind:Kind, Metadata:ManifestMetadata{Name:m.Name, Description:m.Description}, Spec:ManifestSpec{Instructions:m.Instructions, Skills:[]ManifestSkill{}, Compatibility:ManifestCompatibility{Providers:[]string{}}}}
	for _, skill := range m.Skills { manifest.Spec.Skills = append(manifest.Spec.Skills, ManifestSkill{Path:skill.Path}) }
	return manifest
}

func (m PortableManifest) validate() error {
	if m.Schema != PortableSchemaPath || m.Version != PortableVersion || m.Skills == nil { return errors.New("invalid agent.json schema, version or skills array") }
	if err := validateManifest(m.internalManifest()); err != nil { return err }
	for i, skill := range m.Skills {
		if strings.HasPrefix(m.Instructions, skill.Path + "/") { return errors.New("instructions cannot be inside a skill directory") }
		for j, other := range m.Skills {
			if i != j && strings.HasPrefix(skill.Path, other.Path + "/") { return errors.New("skill directories must not overlap") }
		}
		if skill.Enabled == nil || strings.TrimSpace(skill.Name) == "" || utf8.RuneCountInString(skill.Name) > MaxAgentNameLength || len(skill.Description) > MaxDescriptionSize { return fmt.Errorf("invalid metadata or enabled state for skill %q", skill.Path) }
	}
	return nil
}

func compilePortable(ctx context.Context, client RepositoryClient, source Source) (Bundle, error) {
	tree, err := client.GetTree(ctx, source.InstallationID, source.Owner, source.Repository, source.CommitSHA)
	if err != nil { return Bundle{}, err }
	entries := repositoryEntries(tree)
	content, err := loadRequiredText(ctx, client, source, entries, PortableManifestPath)
	if err != nil { return Bundle{}, err }
	decoder := json.NewDecoder(bytes.NewReader(content)); decoder.DisallowUnknownFields()
	var manifest PortableManifest
	if err := decoder.Decode(&manifest); err != nil { return Bundle{}, fmt.Errorf("decode agent.json: %w", err) }
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) { return Bundle{}, errors.New("agent.json must contain exactly one JSON object") }
	var raw struct { Description json.RawMessage `json:"description"`; Skills []struct { Description json.RawMessage `json:"description"` } `json:"skills"` }
	if err := json.Unmarshal(content, &raw); err != nil { return Bundle{}, err }
	if string(raw.Description) == "null" { return Bundle{}, errors.New("description must be a string") }
	for _, item := range raw.Skills { if string(item.Description) == "null" { return Bundle{}, errors.New("skill description must be a string") } }
	if err := manifest.validate(); err != nil { return Bundle{}, err }
	instructions, err := loadRequiredText(ctx, client, source, entries, manifest.Instructions)
	if err != nil { return Bundle{}, err }
	bundle := Bundle{Manifest:manifest.internalManifest(), PortableConfig:&manifest, Instructions:string(instructions), Skills:[]Skill{}, Warnings:[]string{}}
	total := len(content) + len(instructions)
	for _, ref := range manifest.Skills {
		compiled, warnings, size, err := compileSkill(ctx, client, source, entries, ref.Path)
		if err != nil { return Bundle{}, err }
		compiled.Name = ref.Name; compiled.Description = ref.Description; compiled.Disabled = !*ref.Enabled
		bundle.Skills = append(bundle.Skills, compiled); bundle.Warnings = append(bundle.Warnings, warnings...)
		total += size
		if total > MaxBundleSize { return Bundle{}, errors.New("agent source exceeds total size limit") }
	}
	sort.Slice(bundle.Skills, func(i, j int) bool { return bundle.Skills[i].SourcePath < bundle.Skills[j].SourcePath })
	bundle.Hash = hashBundle(bundle)
	if err := ValidateBundle(bundle); err != nil { return Bundle{}, err }
	return bundle, nil
}

// ExportSource creates an editable source directory archive, and reimports it
// through the real repository compiler before allowing the download.
func ExportSource(ctx context.Context, name, description, instructions string, skills []Skill, instructionPaths ...string) ([]byte, error) {
	manifest := PortableManifest{Schema:PortableSchemaPath, Version:PortableVersion, Name:name, Description:description, Instructions:"AGENTS.md", Skills:[]PortableSkill{}}
	if len(instructionPaths) > 0 { manifest.Instructions = instructionPaths[0] }
	if err := validateRepositoryPath(manifest.Instructions, "instructions"); err != nil { return nil, err }
	if manifest.Instructions == PortableSchemaPath || manifest.Instructions == PortableManifestPath { return nil, errors.New("instructions conflict with the manifest") }
	files := map[string][]byte{PortableSchemaPath:PortableSchema, manifest.Instructions:[]byte(instructions)}
	total := len(instructions) + len(PortableSchema)
	add := func(filePath, content string) error {
		if err := validateRepositoryPath(filePath, "export file"); err != nil { return err }
		if _, exists := files[filePath]; exists { return fmt.Errorf("duplicate export path %q", filePath) }
		if !isText([]byte(content)) || isLFSPointer([]byte(content)) || len(content) > MaxFileSize { return fmt.Errorf("file %q must be UTF-8 text within the size limit", filePath) }
		total += len(content)
		if total > MaxBundleSize { return errors.New("agent source exceeds total size limit") }
		files[filePath] = []byte(content)
		return nil
	}
	for _, skill := range skills {
		if err := validateRepositoryPath(skill.SourcePath, "skill path"); err != nil { return nil, err }
		enabled := !skill.Disabled
		manifest.Skills = append(manifest.Skills, PortableSkill{Path:skill.SourcePath, Name:skill.Name, Description:skill.Description, Enabled:&enabled})
		if err := add(skill.SourcePath + "/SKILL.md", skill.Content); err != nil { return nil, err }
		for _, file := range skill.Files {
			if err := validateRepositoryPath(file.Path, "skill file"); err != nil { return nil, err }
			if strings.EqualFold(path.Base(file.Path), "SKILL.md") { return nil, fmt.Errorf("nested or duplicate SKILL.md in %q", file.Path) }
			if err := add(skill.SourcePath + "/" + file.Path, file.Content); err != nil { return nil, err }
		}
	}
	content, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil { return nil, err }
	files[PortableManifestPath] = append(content, '\n')
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	paths := make([]string, 0, len(files)); for p := range files { paths = append(paths, p) }; sort.Strings(paths)
	for _, p := range paths {
		writer, err := archive.Create(p); if err != nil { return nil, err }
		if _, err := writer.Write(files[p]); err != nil { return nil, err }
	}
	if err := archive.Close(); err != nil { return nil, err }
	reader, err := zip.NewReader(bytes.NewReader(buffer.Bytes()), int64(buffer.Len()))
	if err != nil { return nil, err }
	client, err := newFSRepositoryClient(reader)
	if err != nil { return nil, err }
	compiled, err := compilePortable(ctx, client, Source{})
	if err != nil { return nil, err }
	if len(compiled.Warnings) > 0 { return nil, fmt.Errorf("source export would lose files: %s", strings.Join(compiled.Warnings, "; ")) }
	return buffer.Bytes(), nil
}

func SourceManifestPath(bundle Bundle) string {
	if bundle.PortableConfig != nil { return PortableManifestPath }
	return DTAProjectPath
}
