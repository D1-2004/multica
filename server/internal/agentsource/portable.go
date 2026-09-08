package agentsource

import (
	"archive/zip"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
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
	content, err := json.Marshal(m)
	if err != nil { return err }
	if _, err := ValidateManifestJSON(content); err != nil { return err }
	if m.Version != PortableVersion { return errors.New("this manifest version requires the v2 configuration materializer") }
	return validatePortableLayout(m)
}

func compilePortable(ctx context.Context, client RepositoryClient, source Source) (Bundle, error) {
	parsed, err := parseAgentPackageRepository(ctx, client, source)
	if err != nil { return Bundle{}, err }
	return parsed.Bundle()
}

// Bundle is the source-independent, validated input to preview and creation.
// Retain v2 fields in the immutable snapshot and its hash; never reconstruct
// the configuration from the limited legacy manifest header.
func (parsed ParsedAgentPackage) Bundle() (Bundle, error) {
	manifest := parsed.header
	bundle := Bundle{Manifest:manifest.internalManifest(), PortableConfig:&manifest, Instructions:parsed.Instructions, Skills:parsed.Skills, Warnings:parsed.Warnings}
	if manifest.Version == "multica.agent/v2" { bundle.Definition = parsed.Manifest }
	bundle.Hash = hashBundle(bundle)
	if err := ValidateBundle(bundle); err != nil { return Bundle{}, err }
	return bundle, nil
}

// ExportSource retains the v1 source format for repository compiler callers.
func ExportSource(ctx context.Context, name, description, instructions string, skills []Skill, instructionPaths ...string) ([]byte, error) {
	instructionsPath := "AGENTS.md"
	if len(instructionPaths) > 0 { instructionsPath = instructionPaths[0] }
	return ExportAgentPackage(ctx, map[string]any{"$schema":PortableSchemaPath, "version":PortableVersion, "name":name, "description":description, "instructions":instructionsPath}, instructions, skills, nil)
}

// ExportAgentPackage packages a platform snapshot and verifies it through the
// same schema and file parser used by uploaded packages before downloading.
func ExportAgentPackage(ctx context.Context, definition map[string]any, instructions string, skills []Skill, notes []byte) ([]byte, error) {
	content, err := json.Marshal(definition)
	if err != nil { return nil, err }
	var manifest PortableManifest
	if err := json.Unmarshal(content, &manifest); err != nil { return nil, err }
	manifest.Skills = []PortableSkill{}
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
	if notes != nil {
		if err := add("EXPORT-NOTES.json", string(notes)); err != nil { return nil, err }
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
	definition["skills"] = manifest.Skills
	content, err = json.MarshalIndent(definition, "", "  ")
	if err != nil { return nil, err }
	if _, err := ValidateManifestJSON(content); err != nil { return nil, err }
	files[PortableManifestPath] = append(content, '\n')
	if total + len(files[PortableManifestPath]) > MaxBundleSize { return nil, errors.New("agent package exceeds total size limit") }
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	paths := make([]string, 0, len(files)); for p := range files { paths = append(paths, p) }; sort.Strings(paths)
	for _, p := range paths {
		writer, err := archive.Create(p); if err != nil { return nil, err }
		if _, err := writer.Write(files[p]); err != nil { return nil, err }
	}
	if err := archive.Close(); err != nil { return nil, err }
	compiled, err := ParseAgentPackage(ctx, buffer.Bytes())
	if err != nil { return nil, err }
	if len(compiled.Warnings) > 0 { return nil, fmt.Errorf("source export would lose files: %s", strings.Join(compiled.Warnings, "; ")) }
	return buffer.Bytes(), nil
}

func SourceManifestPath(bundle Bundle) string {
	if bundle.PortableConfig != nil { return PortableManifestPath }
	return DTAProjectPath
}
