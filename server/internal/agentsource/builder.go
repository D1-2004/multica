package agentsource

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
)

// BuildAgentPackage turns authored manifest/files into the same ZIP contract
// as a local upload. Validation is server-owned; no generated code is executed.
func BuildAgentPackage(ctx context.Context, manifest json.RawMessage, contents map[string]string) ([]byte, error) {
	if _, err := ValidateManifestJSON(manifest); err != nil { return nil, err }
	if len(contents) + 2 > MaxAgentPackageEntries { return nil, fmt.Errorf("Agent package contains too many entries") }
	files := map[string]string{PortableManifestPath:string(manifest), PortableSchemaPath:string(PortableSchema)}
	total := len(manifest) + len(PortableSchema)
	for path, content := range contents {
		if err := validateRepositoryPath(path, "package file"); err != nil { return nil, err }
		if path == PortableManifestPath || path == PortableSchemaPath { return nil, fmt.Errorf("files/%s is reserved; provide the manifest separately and use the server Schema", path) }
		if len(content) > MaxFileSize || !isText([]byte(content)) { return nil, fmt.Errorf("file %q must be UTF-8 text of at most %d bytes", path, MaxFileSize) }
		total += len(content)
		if total > MaxBundleSize { return nil, fmt.Errorf("Agent package exceeds the %d byte uncompressed size limit", MaxBundleSize) }
		files[path] = content
	}
	paths := make([]string, 0, len(files)); for path := range files { paths = append(paths, path) }; sort.Strings(paths)
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	for _, path := range paths {
		if err := ctx.Err(); err != nil { return nil, err }
		writer, err := archive.Create(path); if err != nil { return nil, err }
		if _, err := writer.Write([]byte(files[path])); err != nil { return nil, err }
	}
	if err := archive.Close(); err != nil { return nil, err }
	if _, err := ParseAgentPackage(ctx, buffer.Bytes()); err != nil { return nil, err }
	return buffer.Bytes(), nil
}
