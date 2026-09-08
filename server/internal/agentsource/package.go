package agentsource

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

const MaxAgentPackageSize = 40 << 20
const MaxAgentPackageEntries = 8192

// ParsedAgentPackage preserves every schema-validated field, including those
// that require deployment bindings. Parsing never applies a partial Agent.
type ParsedAgentPackage struct {
	Manifest map[string]json.RawMessage `json:"manifest"`
	Instructions string `json:"instructions"`
	Skills []Skill `json:"skills"`
	Warnings []string `json:"warnings"`
	Hash string `json:"hash"`
	header PortableManifest
}

func ParseAgentPackage(ctx context.Context, content []byte) (ParsedAgentPackage, error) {
	if len(content) == 0 || len(content) > MaxAgentPackageSize { return ParsedAgentPackage{}, errors.New("Agent package exceeds the upload size limit or is empty") }
	archive, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil { return ParsedAgentPackage{}, errors.New("Agent package must be a ZIP archive") }
	if len(archive.File) > MaxAgentPackageEntries { return ParsedAgentPackage{}, errors.New("Agent package contains too many entries") }
	seen := map[string]bool{}
	var total uint64
	for _, file := range archive.File {
		filePath := strings.TrimSuffix(file.Name, "/")
		if err := validateRepositoryPath(filePath, "package path"); err != nil { return ParsedAgentPackage{}, err }
		if seen[filePath] { return ParsedAgentPackage{}, fmt.Errorf("duplicate Agent package path %q", filePath) }
		seen[filePath] = true
		if file.Mode()&fs.ModeSymlink != 0 || (!file.Mode().IsRegular() && !file.Mode().IsDir()) { return ParsedAgentPackage{}, errors.New("Agent package contains an unsupported file type") }
		if file.UncompressedSize64 > MaxBundleSize || total > MaxBundleSize - file.UncompressedSize64 { return ParsedAgentPackage{}, errors.New("Agent package exceeds the uncompressed size limit") }
		total += file.UncompressedSize64
	}
	return ParseAgentPackageFS(ctx, archive)
}

func ParseAgentPackageFS(ctx context.Context, files fs.FS) (ParsedAgentPackage, error) {
	client, err := newFSRepositoryClient(files)
	if err != nil { return ParsedAgentPackage{}, err }
	return parseAgentPackageRepository(ctx, client, Source{})
}

func parseAgentPackageRepository(ctx context.Context, client RepositoryClient, source Source) (ParsedAgentPackage, error) {
	if err := ctx.Err(); err != nil { return ParsedAgentPackage{}, err }
	tree, err := client.GetTree(ctx, source.InstallationID, source.Owner, source.Repository, source.CommitSHA)
	if err != nil { return ParsedAgentPackage{}, err }
	entries := repositoryEntries(tree)
	if _, exists := entries[DTAProjectPath]; exists { return ParsedAgentPackage{}, errors.New("Agent package must contain only agent.json as its manifest") }
	content, err := loadRequiredText(ctx, client, source, entries, PortableManifestPath)
	if err != nil { return ParsedAgentPackage{}, err }
	fields, err := ValidateManifestJSON(content)
	if err != nil { return ParsedAgentPackage{}, err }
	var header PortableManifest
	if err := json.Unmarshal(content, &header); err != nil { return ParsedAgentPackage{}, errors.New("failed to decode validated manifest") }
	if err := validatePortableLayout(header); err != nil { return ParsedAgentPackage{}, err }
	instructions, err := loadRequiredText(ctx, client, source, entries, header.Instructions)
	if err != nil { return ParsedAgentPackage{}, err }
	parsed := ParsedAgentPackage{Manifest:fields, Instructions:string(instructions), Skills:[]Skill{}, Warnings:[]string{}, header:header}
	total := len(content) + len(instructions)
	for _, item := range header.Skills {
		if err := ctx.Err(); err != nil { return ParsedAgentPackage{}, err }
		compiled, warnings, size, err := compileSkill(ctx, client, source, entries, item.Path)
		if err != nil { return ParsedAgentPackage{}, err }
		compiled.Name, compiled.Description, compiled.Disabled = item.Name, item.Description, !*item.Enabled
		parsed.Skills = append(parsed.Skills, compiled)
		parsed.Warnings = append(parsed.Warnings, warnings...)
		total += size
		if total > MaxBundleSize { return ParsedAgentPackage{}, errors.New("Agent source exceeds total size limit") }
	}
	sort.Slice(parsed.Skills, func(i, j int) bool { return parsed.Skills[i].SourcePath < parsed.Skills[j].SourcePath })
	sort.Strings(parsed.Warnings)
	encoded, err := json.Marshal(struct { Manifest map[string]json.RawMessage; Instructions string; Skills []Skill }{fields, parsed.Instructions, parsed.Skills})
	if err != nil { return ParsedAgentPackage{}, err }
	hash := sha256.Sum256(encoded)
	parsed.Hash = hex.EncodeToString(hash[:])
	return parsed, nil
}

func validatePortableLayout(manifest PortableManifest) error {
	if len(manifest.Description) > MaxDescriptionSize { return errors.New("manifest description exceeds the UTF-8 byte limit") }
	paths, names := map[string]bool{}, map[string]bool{}
	for _, skill := range manifest.Skills {
		if paths[skill.Path] || names[skill.Name] { return errors.New("duplicate skill path or name") }
		paths[skill.Path], names[skill.Name] = true, true
		if len(skill.Description) > MaxDescriptionSize { return fmt.Errorf("skill %q description exceeds the UTF-8 byte limit", skill.Path) }
		if strings.HasPrefix(manifest.Instructions, skill.Path + "/") { return errors.New("instructions cannot be inside a skill directory") }
		for _, other := range manifest.Skills {
			if skill.Path != other.Path && strings.HasPrefix(skill.Path, other.Path + "/") { return errors.New("skill directories must not overlap") }
		}
	}
	return nil
}
