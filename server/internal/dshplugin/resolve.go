package dshplugin

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	// MaxTarballBytes bounds a single package download. The largest entries in
	// the community catalog are a few megabytes; this leaves generous headroom
	// while keeping one import off the heap budget.
	MaxTarballBytes = 32 << 20
	// maxMemberBytes bounds any single file read out of the archive.
	maxMemberBytes = 8 << 20
	// maxMembers stops a tarball with an absurd file count from spinning here.
	maxMembers = 20000
	// maxArchiveBytes bounds the DECOMPRESSED total. Without it a small,
	// highly compressible archive expands without limit in memory.
	maxArchiveBytes = 96 << 20
	// maxMemberNameBytes caps a single entry name. PAX stores a long name as
	// member data, so an unbounded name is an unbounded allocation.
	maxMemberNameBytes = 4096
	// DefaultRegistry is the mirror the runtime adapter also defaults to.
	DefaultRegistry = "https://registry.npmmirror.com"
)

// Resolved is everything an import needs to record, all of it read out of the
// package itself rather than supplied by the caller.
type Resolved struct {
	PackageName string
	Version     string
	Description string
	Homepage    string
	// Integrity is the sha256 of the exact bytes fetched, in the
	// `sha256-<hex>` shape the runtime adapter verifies.
	Integrity string
	// BundleRows are the loader row ids the package's own patch file inserts.
	// A row id is chosen by the plugin author and routinely differs from the
	// package name, so config must be addressed by row, not by package.
	BundleRows []string
	// Entry is the prebuilt file main/exports resolves to, proving the package
	// ships built output rather than only sources.
	Entry string
	// WebOnly is true when the package declares a browser client surface and
	// nothing else. Multica runs headless, so such a plugin would load but do
	// nothing useful.
	WebOnly bool
	// Warnings are non-fatal observations worth showing at import time.
	Warnings []string
	// SizeBytes is the fetched archive size.
	SizeBytes int64
	// Archive is the exact bytes that were validated, so the caller can store
	// them without fetching the package a second time.
	Archive []byte
}

type packageManifest struct {
	Name        string            `json:"name"`
	Version     string            `json:"version"`
	Description string            `json:"description"`
	Homepage    string            `json:"homepage"`
	Main        string            `json:"main"`
	Exports     json.RawMessage   `json:"exports"`
	Scripts     map[string]string `json:"scripts"`
	DSH         *struct {
		Bundle *struct {
			Patch string `json:"patch"`
		} `json:"bundle"`
		Client json.RawMessage `json:"client"`
	} `json:"dsh"`
	Dependencies map[string]string `json:"dependencies"`
}

type registryVersion struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
	Homepage    string `json:"homepage"`
	Dist        struct {
		Tarball   string `json:"tarball"`
		Integrity string `json:"integrity"`
		Shasum    string `json:"shasum"`
	} `json:"dist"`
}

// Resolver fetches and validates packages. Its HTTP client is supplied so the
// caller owns the timeout and any proxy policy.
type Resolver struct {
	HTTP     *http.Client
	Registry string
}

// NewResolver builds a resolver with sane defaults.
func NewResolver(registry string) *Resolver {
	if strings.TrimSpace(registry) == "" {
		registry = DefaultRegistry
	}
	return &Resolver{
		HTTP:     newSafeHTTPClient(60 * time.Second),
		Registry: strings.TrimRight(registry, "/"),
	}
}

// Resolve downloads the package the source names and validates that it is a
// DSH plugin Multica can actually boot.
func (r *Resolver) Resolve(ctx context.Context, src Source) (*Resolved, error) {
	tarballURL := ""
	var meta *registryVersion
	switch src.Kind {
	case SourceNPM:
		var err error
		meta, err = r.fetchRegistryVersion(ctx, src)
		if err != nil {
			return nil, err
		}
		tarballURL = meta.Dist.Tarball
		if tarballURL == "" {
			return nil, fmt.Errorf("the registry published no tarball for %s", src.Name)
		}
	default:
		var err error
		tarballURL, err = src.TarballURL(r.Registry)
		if err != nil {
			return nil, err
		}
	}

	data, err := r.fetch(ctx, src, tarballURL)
	if err != nil {
		return nil, err
	}
	// The digest computed below only detects later drift. Checking the bytes
	// against what the registry published is what detects a swapped tarball on
	// the very first fetch.
	if meta != nil {
		if err := verifyPublishedDigest(data, meta.Dist.Integrity, meta.Dist.Shasum); err != nil {
			return nil, err
		}
	}
	digest := sha256.Sum256(data)

	resolved, err := inspectArchive(data, src)
	if err != nil {
		return nil, err
	}
	resolved.Integrity = "sha256-" + hex.EncodeToString(digest[:])
	resolved.SizeBytes = int64(len(data))
	resolved.Archive = data
	if meta != nil {
		if resolved.Version == "" {
			resolved.Version = meta.Version
		}
		if resolved.Description == "" {
			resolved.Description = meta.Description
		}
		if resolved.Homepage == "" {
			resolved.Homepage = meta.Homepage
		}
	}
	return resolved, nil
}

func (r *Resolver) fetchRegistryVersion(ctx context.Context, src Source) (*registryVersion, error) {
	version := src.Version
	if version == "" {
		version = "latest"
	}
	endpoint := fmt.Sprintf("%s/%s/%s", r.Registry, escapePackageName(src.Name), url.PathEscape(version))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to reach the npm registry: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("%s@%s was not found on the registry", src.Name, version)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the npm registry returned status %d for %s", resp.StatusCode, src.Name)
	}
	var out registryVersion
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("the npm registry returned a response this server could not parse")
	}
	return &out, nil
}

// escapePackageName encodes a scoped name the way the registry expects: the
// slash in @scope/name is percent-encoded, the @ is not.
func escapePackageName(name string) string {
	return strings.ReplaceAll(url.PathEscape(name), "%40", "@")
}

func (r *Resolver) fetch(ctx context.Context, src Source, tarballURL string) ([]byte, error) {
	if src.Kind == SourceFile {
		info, err := os.Stat(src.URL)
		if err != nil {
			return nil, fmt.Errorf("cannot read %s", src.URL)
		}
		if info.Size() > MaxTarballBytes {
			return nil, fmt.Errorf("the package is %d bytes, over the %d byte limit", info.Size(), int64(MaxTarballBytes))
		}
		return os.ReadFile(src.URL)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tarballURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to download the package: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloading the package returned status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxTarballBytes+1))
	if err != nil {
		return nil, fmt.Errorf("failed to read the package: %w", err)
	}
	if len(data) > MaxTarballBytes {
		return nil, fmt.Errorf("the package is larger than the %d byte limit", int64(MaxTarballBytes))
	}
	if len(data) == 0 {
		return nil, errors.New("the package is empty")
	}
	return data, nil
}

// archiveFiles reads a gzipped tar into a map of cleaned relative path to
// bytes, with the leading wrapper directory (npm's "package/", GitHub's
// "<repo>-<ref>/") stripped.
func archiveFiles(data []byte) (map[string][]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("the package is not a gzipped tarball")
	}
	defer gz.Close()

	// Bound the DECOMPRESSED stream itself, not just the bytes retained. Tar
	// headers carry their own payload — a PAX long-name record is file data —
	// so counting only regular-file contents leaves a gap wide enough to blow
	// the heap with a few kilobytes of archive.
	limited := &countingReader{inner: io.LimitReader(gz, maxArchiveBytes+1)}

	files := map[string][]byte{}
	reader := tar.NewReader(limited)
	root := ""
	for count := 0; ; count++ {
		if count >= maxMembers {
			return nil, fmt.Errorf("the package archive holds more than %d files", maxMembers)
		}
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("the package archive is malformed")
		}
		if limited.read > maxArchiveBytes {
			return nil, fmt.Errorf("the package archive expands past the %d byte limit", int64(maxArchiveBytes))
		}
		// A PAX global header sets defaults for every following member. Go
		// ignores it; Python's tarfile applies it. The runtime adapter unpacks
		// with Python, so honouring the difference would let one tarball
		// present a different package.json to the validator than to the
		// sandbox — with a matching digest, because it is the same bytes.
		if header.Typeflag == tar.TypeXGlobalHeader {
			return nil, fmt.Errorf("the package archive carries a global header, which is not accepted")
		}
		if len(header.Name) > maxMemberNameBytes {
			return nil, fmt.Errorf("the package archive contains an over-long file name")
		}
		name := path.Clean(strings.TrimPrefix(header.Name, "./"))
		// Check the path before deciding whether to read the member. Skipping
		// non-regular entries first would let a symlink or a second root
		// directory pass here and then fail in the sandbox, where no operator
		// can see why.
		if strings.HasPrefix(name, "/") || name == ".." || strings.HasPrefix(name, "../") {
			return nil, fmt.Errorf("the package archive contains an unsafe path: %s", header.Name)
		}
		if header.Typeflag == tar.TypeSymlink || header.Typeflag == tar.TypeLink {
			return nil, fmt.Errorf("the package archive contains a link, which is not accepted: %s", header.Name)
		}
		if name == "." || name == "" {
			continue
		}
		top := name
		if slash := strings.Index(name, "/"); slash >= 0 {
			top = name[:slash]
		}
		if root == "" {
			root = top
		}
		// Applies to the bare wrapper entry too: a second empty top-level
		// directory is still a second root, and the adapter refuses those.
		if top != root {
			return nil, fmt.Errorf("the package archive has more than one root directory")
		}
		if top == name {
			continue
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		if header.Size > maxMemberBytes {
			return nil, fmt.Errorf("the package archive contains a file over the %d byte limit: %s",
				int64(maxMemberBytes), name)
		}
		body, err := io.ReadAll(io.LimitReader(reader, maxMemberBytes+1))
		if err != nil {
			return nil, fmt.Errorf("the package archive is malformed")
		}
		if int64(len(body)) > maxMemberBytes {
			return nil, fmt.Errorf("the package archive contains a file over the %d byte limit: %s",
				int64(maxMemberBytes), name)
		}
		files[path.Clean(name[len(top)+1:])] = body
	}
	if limited.read > maxArchiveBytes {
		return nil, fmt.Errorf("the package archive expands past the %d byte limit", int64(maxArchiveBytes))
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("the package archive holds no files")
	}
	return files, nil
}

// countingReader tracks how many decompressed bytes the tar reader has
// consumed, including the bytes of headers and of members that are skipped.
type countingReader struct {
	inner io.Reader
	read  int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.inner.Read(p)
	c.read += int64(n)
	return n, err
}

// inspectArchive applies the same gates the runtime adapter applies before it
// will boot a profile, so an import that succeeds here runs in a task.
func inspectArchive(data []byte, src Source) (*Resolved, error) {
	files, err := archiveFiles(data)
	if err != nil {
		return nil, err
	}
	raw, ok := files["package.json"]
	if !ok {
		return nil, fmt.Errorf("the package has no package.json at its root")
	}
	var manifest packageManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, fmt.Errorf("the package.json could not be parsed")
	}
	if !ValidPackageName(manifest.Name) {
		return nil, fmt.Errorf("the package declares an invalid name: %q", manifest.Name)
	}
	if src.Kind == SourceNPM && src.Name != manifest.Name {
		return nil, fmt.Errorf("the registry served %q when %q was requested", manifest.Name, src.Name)
	}

	// Install scripts are never run, so a package that needs one would be
	// installed half-configured. Refuse instead.
	for _, hook := range []string{"preinstall", "install", "postinstall"} {
		if strings.TrimSpace(manifest.Scripts[hook]) != "" {
			return nil, fmt.Errorf("the package declares a %s script, which is never run; publish a prebuilt package instead", hook)
		}
	}

	if manifest.DSH == nil || manifest.DSH.Bundle == nil || strings.TrimSpace(manifest.DSH.Bundle.Patch) == "" {
		return nil, fmt.Errorf("the package declares no dsh.bundle.patch, so DeepSeek Harness would refuse to load it as a profile bundle")
	}

	entry, err := declaredEntry(manifest)
	if err != nil {
		return nil, err
	}
	if entry == "" {
		return nil, fmt.Errorf("the package declares neither main nor exports, so nothing would load")
	}
	if _, ok := files[entry]; !ok {
		hint := "the package ships no built output at " + entry
		if strings.TrimSpace(manifest.Scripts["prepare"]) != "" {
			hint += "; it builds in a prepare script, so use its published npm release rather than a source snapshot"
		}
		return nil, errors.New(hint)
	}

	patchPath := path.Clean(strings.TrimPrefix(manifest.DSH.Bundle.Patch, "./"))
	patchBody, ok := files[patchPath]
	if !ok {
		return nil, fmt.Errorf("the package points dsh.bundle.patch at %s, which is not in the package", patchPath)
	}
	rows, err := BundleRowIDs(patchBody)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("the package's bundle patch inserts no loader row, so there would be nothing to configure or run")
	}

	resolved := &Resolved{
		PackageName: manifest.Name,
		Version:     manifest.Version,
		Description: manifest.Description,
		Homepage:    manifest.Homepage,
		BundleRows:  rows,
		Entry:       entry,
	}
	if len(manifest.DSH.Client) > 0 && string(manifest.DSH.Client) != "null" {
		resolved.WebOnly = true
		resolved.Warnings = append(resolved.Warnings,
			"This plugin declares a browser client surface. Multica runs DeepSeek Harness headless, so any UI it contributes will not appear.")
	}
	if len(manifest.Dependencies) > 0 {
		resolved.Warnings = append(resolved.Warnings, fmt.Sprintf(
			"Runtime dependencies (%s) must already be present in the sandbox image; the sandbox installs nothing at task time.",
			strings.Join(sortedKeys(manifest.Dependencies), ", ")))
	}
	return resolved, nil
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// declaredEntry returns the file Node would load for the package root.
//
// exports wins over main when both are present, because that is Node's own
// precedence. Two details matter and are easy to get wrong: an exports map that
// declares subpaths but no "." does not export the root at all, so falling back
// to main would validate a file nothing can import; and conditions are matched
// in the order the package DECLARES them, not in a fixed order of our choosing.
func declaredEntry(manifest packageManifest) (string, error) {
	if len(manifest.Exports) > 0 {
		var asString string
		if err := json.Unmarshal(manifest.Exports, &asString); err == nil && asString != "" {
			return path.Clean(strings.TrimPrefix(asString, "./")), nil
		}
		var asObject map[string]json.RawMessage
		if err := json.Unmarshal(manifest.Exports, &asObject); err == nil {
			if entry, ok := asObject["."]; ok {
				found := firstStringLeaf(entry)
				if found == "" {
					return "", errors.New("the package exports its root as something this server cannot resolve")
				}
				return found, nil
			}
			if hasSubpathKeys(asObject) {
				// Subpaths only: the package root is deliberately not exported.
				return "", errors.New("the package declares subpath exports but no root export, so importing it by name would fail")
			}
			if found := firstStringLeaf(manifest.Exports); found != "" {
				return found, nil
			}
		}
	}
	if main := strings.TrimPrefix(strings.TrimSpace(manifest.Main), "./"); main != "" {
		return path.Clean(main), nil
	}
	return "", nil
}

// hasSubpathKeys reports whether an exports object maps subpaths ("./x")
// rather than conditions ("import", "require").
func hasSubpathKeys(exports map[string]json.RawMessage) bool {
	for key := range exports {
		if strings.HasPrefix(key, ".") {
			return true
		}
	}
	return false
}

// firstStringLeaf walks a conditional-exports subtree and returns the path a
// resolver would pick. Conditions are tried in declaration order, which is what
// Node does — a fixed preference list picks the wrong branch whenever a package
// lists a fallback before the condition we happen to rank higher. An array is a
// fallback list, so its first usable entry wins.
func firstStringLeaf(raw json.RawMessage) string {
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		if asString == "" {
			return ""
		}
		return path.Clean(strings.TrimPrefix(asString, "./"))
	}
	var asArray []json.RawMessage
	if err := json.Unmarshal(raw, &asArray); err == nil {
		for _, child := range asArray {
			if found := firstStringLeaf(child); found != "" {
				return found
			}
		}
		return ""
	}
	for _, key := range objectKeysInOrder(raw) {
		// "types" names a declaration file, never something a runtime loads.
		if key == "types" {
			continue
		}
		var asObject map[string]json.RawMessage
		if err := json.Unmarshal(raw, &asObject); err != nil {
			return ""
		}
		if found := firstStringLeaf(asObject[key]); found != "" {
			return found
		}
	}
	return ""
}

// objectKeysInOrder returns a JSON object's keys in the order the document
// declares them, which encoding/json's map decoding discards.
func objectKeysInOrder(raw json.RawMessage) []string {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return nil
	}
	if delim, ok := token.(json.Delim); !ok || delim != '{' {
		return nil
	}
	var keys []string
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return keys
		}
		key, ok := keyToken.(string)
		if !ok {
			return keys
		}
		keys = append(keys, key)
		var skip json.RawMessage
		if err := decoder.Decode(&skip); err != nil {
			return keys
		}
	}
	return keys
}

// BundleRowIDs reads the loader rows a bundle's cordis.patch.yml leaves behind.
//
// The file is YAML with DSH's own `!!js` tag, whose value is an expression
// evaluated at compose time. A strict decode rejects the unknown tag outright,
// so the document is walked as nodes and tagged scalars are left alone — the
// row ids are what matter here, not the expressions.
//
// Patches are applied in order, the way the loader applies them, because the
// list is a program and not a set: a later patch can replace a group's children,
// and reporting the ids an earlier insert mentioned would offer configuration
// targets that no longer exist while hiding the ones that do.
func BundleRowIDs(body []byte) ([]string, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("the package's bundle patch is not valid YAML")
	}

	var top []*patchEntry
	// Only rows introduced by an insert are addressable, mirroring the
	// loader's own index; a patch naming anything else is a no-op there too.
	index := map[string]*patchEntry{}

	var register func(entry *patchEntry)
	register = func(entry *patchEntry) {
		if entry.id != "" {
			index[entry.id] = entry
		}
		if entry.isGroup {
			for _, child := range entry.children {
				register(child)
			}
		}
	}

	for _, patch := range patchList(&doc) {
		id := mappingValue(patch, "id")
		insert := mappingNode(patch, "insert")
		if insert != nil && insert.Kind == yaml.SequenceNode {
			inserted := make([]*patchEntry, 0, len(insert.Content))
			for _, node := range insert.Content {
				if entry := parsePatchEntry(node); entry != nil {
					inserted = append(inserted, entry)
				}
			}
			if id == "" {
				top = append(top, inserted...)
			} else {
				target, ok := index[id]
				if !ok || !target.isGroup {
					// The loader warns and skips; so do we.
					continue
				}
				target.children = append(target.children, inserted...)
			}
			for _, entry := range inserted {
				register(entry)
			}
			continue
		}
		if id == "" {
			continue
		}
		target, ok := index[id]
		if !ok {
			continue
		}
		// `name` is an assertion guard: a mismatch makes the loader skip the
		// whole patch rather than apply it to the wrong row.
		if asserted := mappingValue(patch, "name"); asserted != "" && asserted != target.name {
			continue
		}
		if group := mappingNode(patch, "group"); group != nil && group.Kind == yaml.ScalarNode {
			target.isGroup = group.Value == "true"
		}
		if config := mappingNode(patch, "config"); config != nil {
			// An override replaces the value wholesale. For a group that means
			// its children become exactly this list; for anything else the row
			// simply has no children.
			target.children = nil
			if target.isGroup && config.Kind == yaml.SequenceNode {
				for _, node := range config.Content {
					if entry := parsePatchEntry(node); entry != nil {
						target.children = append(target.children, entry)
					}
				}
			}
		}
	}

	seen := map[string]bool{}
	var rows []string
	var flatten func(entries []*patchEntry)
	flatten = func(entries []*patchEntry) {
		for _, entry := range entries {
			if entry.id != "" && !seen[entry.id] {
				seen[entry.id] = true
				rows = append(rows, entry.id)
			}
			if entry.isGroup {
				flatten(entry.children)
			}
		}
	}
	flatten(top)
	return rows, nil
}

// patchEntry is one row in the composed tree.
type patchEntry struct {
	id       string
	name     string
	isGroup  bool
	children []*patchEntry
}

// parsePatchEntry reads a single row, including a group's nested children.
func parsePatchEntry(node *yaml.Node) *patchEntry {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	entry := &patchEntry{
		id:   mappingValue(node, "id"),
		name: mappingValue(node, "name"),
	}
	if group := mappingNode(node, "group"); group != nil {
		// Both shapes appear in the wild: `group: true` with children under
		// `config`, and children directly under `group`.
		if group.Kind == yaml.ScalarNode {
			entry.isGroup = group.Value == "true"
		} else if group.Kind == yaml.SequenceNode {
			entry.isGroup = true
			for _, child := range group.Content {
				if parsed := parsePatchEntry(child); parsed != nil {
					entry.children = append(entry.children, parsed)
				}
			}
		}
	}
	if entry.isGroup && entry.children == nil {
		if config := mappingNode(node, "config"); config != nil && config.Kind == yaml.SequenceNode {
			for _, child := range config.Content {
				if parsed := parsePatchEntry(child); parsed != nil {
					entry.children = append(entry.children, parsed)
				}
			}
		}
	}
	return entry
}

// patchList returns the top-level patch mappings of a document.
func patchList(doc *yaml.Node) []*yaml.Node {
	node := doc
	for node != nil && node.Kind == yaml.DocumentNode {
		if len(node.Content) == 0 {
			return nil
		}
		node = node.Content[0]
	}
	if node == nil || node.Kind != yaml.SequenceNode {
		return nil
	}
	out := make([]*yaml.Node, 0, len(node.Content))
	for _, child := range node.Content {
		if child.Kind == yaml.MappingNode {
			out = append(out, child)
		}
	}
	return out
}

// mappingNode returns the value node for a key of a mapping, or nil.
func mappingNode(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

// mappingValue returns a scalar value for a key of a mapping, or "".
func mappingValue(node *yaml.Node, key string) string {
	value := mappingNode(node, key)
	if value == nil || value.Kind != yaml.ScalarNode {
		return ""
	}
	return value.Value
}

// getJSON performs a bounded GET and returns the body.
func (r *Resolver) getJSON(ctx context.Context, endpoint string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to reach the npm registry: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the npm registry returned status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

// urlQueryEscape is a local alias so callers do not have to import net/url.
func urlQueryEscape(value string) string { return url.QueryEscape(value) }

// verifyPublishedDigest checks downloaded bytes against the digest the registry
// published for that version.
//
// Recomputing a digest over whatever arrived proves only that the bytes do not
// change later. Comparing against the published value is what catches a tarball
// swapped at the mirror — and a mirror is exactly what this fetches from.
func verifyPublishedDigest(data []byte, integrity, shasum string) error {
	integrity = strings.TrimSpace(integrity)
	shasum = strings.TrimSpace(shasum)
	if integrity != "" {
		algorithm, encoded, found := strings.Cut(integrity, "-")
		if !found {
			return fmt.Errorf("the registry published an integrity value this server cannot read")
		}
		expected, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return fmt.Errorf("the registry published an integrity value this server cannot read")
		}
		var actual []byte
		switch algorithm {
		case "sha512":
			sum := sha512.Sum512(data)
			actual = sum[:]
		case "sha256":
			sum := sha256.Sum256(data)
			actual = sum[:]
		case "sha1":
			sum := sha1.Sum(data)
			actual = sum[:]
		default:
			// An algorithm this build does not know is not a reason to accept
			// unverified bytes when a shasum is also published.
			if shasum == "" {
				return fmt.Errorf("the registry published an unsupported integrity algorithm %q", algorithm)
			}
			actual = nil
		}
		if actual != nil {
			if !hmac.Equal(actual, expected) {
				return fmt.Errorf("the downloaded package does not match the digest the registry published")
			}
			return nil
		}
	}
	if shasum != "" {
		sum := sha1.Sum(data)
		if !hmac.Equal([]byte(hex.EncodeToString(sum[:])), []byte(strings.ToLower(shasum))) {
			return fmt.Errorf("the downloaded package does not match the checksum the registry published")
		}
		return nil
	}
	return fmt.Errorf("the registry published no digest for this version, so the download cannot be verified")
}

// LatestVersion asks the registry what the newest published version of a
// package is, so an operator can be told an update exists rather than having to
// go and look.
func (r *Resolver) LatestVersion(ctx context.Context, name string) (string, error) {
	if !ValidPackageName(name) {
		return "", fmt.Errorf("%q is not a valid npm package name", name)
	}
	meta, err := r.fetchRegistryVersion(ctx, Source{Kind: SourceNPM, Name: name})
	if err != nil {
		return "", err
	}
	return meta.Version, nil
}
