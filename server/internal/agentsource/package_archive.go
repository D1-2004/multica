package agentsource

import (
	"fmt"
	"path"
	"strings"
)

// normalizeAgentPackageArchive removes packaging-only directories and desktop
// metadata after every original ZIP entry has passed the safety/size checks.
// Blob paths still point into the original archive; no content is read until
// the logical root manifest has been validated by the shared package parser.
func normalizeAgentPackageArchive(client *fsRepositoryClient) error {
	entries := client.entries[:0]
	root := ""
	manifests := []string{}
	hasRootManifest := false
	for _, entry := range client.entries {
		if isAgentPackageArchiveMetadata(entry.Path) { continue }
		entries = append(entries, entry)
		if entry.Type != "blob" { continue }
		if root == "" { root = path.Dir(entry.Path) }
		for root != "." && !strings.HasPrefix(entry.Path, root + "/") { root = path.Dir(root) }
		if path.Base(entry.Path) == PortableManifestPath {
			manifests = append(manifests, entry.Path)
			if entry.Path == PortableManifestPath { hasRootManifest = true }
		}
	}
	client.entries = entries
	if hasRootManifest { return nil }
	if root == "" { root = "." }
	manifestPath := path.Join(root, PortableManifestPath)
	for _, candidate := range manifests {
		if candidate != manifestPath { continue }
		// All non-metadata files share this directory. Rebase logical paths
		// while retaining original blob lookups and skill identity fields.
		entries = client.entries[:0]
		prefix := root + "/"
		for _, entry := range client.entries {
			if !strings.HasPrefix(entry.Path, prefix) { continue }
			entry.Path = strings.TrimPrefix(entry.Path, prefix)
			entries = append(entries, entry)
		}
		client.entries = entries
		return nil
	}
	if len(manifests) > 1 {
		return fmt.Errorf("Agent ZIP contains multiple agent.json candidates %q but no manifest at the package root %q; upload one Agent directory containing agent.json and its referenced files", manifests, root)
	}
	if len(manifests) == 1 {
		return fmt.Errorf("Agent ZIP contains %q, but other files are outside its directory; place agent.json and all referenced files together at the package root", manifests[0])
	}
	return fmt.Errorf("required file %q was not found in Agent package root %q; include agent.json alongside its referenced files, either at the ZIP root or inside one enclosing Agent directory", PortableManifestPath, root)
}

func isAgentPackageArchiveMetadata(filePath string) bool {
	for _, part := range strings.Split(filePath, "/") {
		if part == "__MACOSX" || part == ".DS_Store" || strings.HasPrefix(part, "._") { return true }
	}
	return false
}
