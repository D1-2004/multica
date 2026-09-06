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

	files := map[string][]byte{}
	reader := tar.NewReader(gz)
	root := ""
	// A 32 MiB archive of highly compressible members can expand to gigabytes,
	// so the running total is what actually bounds memory — the per-member and
	// per-archive caps alone do not.
	var total int64
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
		slash := strings.Index(name, "/")
		if slash < 0 {
			// A bare top-level entry is the wrapper directory itself.
			if root == "" && header.Typeflag == tar.TypeDir {
				root = name
			}
			continue
		}
		if root == "" {
			root = name[:slash]
		}
		if name[:slash] != root {
			return nil, fmt.Errorf("the package archive has more than one root directory")
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		if header.Size > maxMemberBytes {
			return nil, fmt.Errorf("the package archive contains a file over the %d byte limit: %s",
				int64(maxMemberBytes), name)
		}
		remaining := maxArchiveBytes - total
		if remaining <= 0 {
			return nil, fmt.Errorf("the package archive expands past the %d byte limit", int64(maxArchiveBytes))
		}
		body, err := io.ReadAll(io.LimitReader(reader, remaining+1))
		if err != nil {
			return nil, fmt.Errorf("the package archive is malformed")
		}
		total += int64(len(body))
		if total > maxArchiveBytes {
			return nil, fmt.Errorf("the package archive expands past the %d byte limit", int64(maxArchiveBytes))
		}
		files[path.Clean(name[slash+1:])] = body
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("the package archive holds no files")
	}
	return files, nil
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

	entry := declaredEntry(manifest)
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
// precedence: a package whose main points at a shipped file while its "."
// export points at a missing one does not load, and validating main would pass
// it. Only the "." key counts — a subpath export is not the package entry.
func declaredEntry(manifest packageManifest) string {
	if len(manifest.Exports) > 0 {
		var asString string
		if err := json.Unmarshal(manifest.Exports, &asString); err == nil && asString != "" {
			return path.Clean(strings.TrimPrefix(asString, "./"))
		}
		var asObject map[string]json.RawMessage
		if err := json.Unmarshal(manifest.Exports, &asObject); err == nil {
			// An exports object with no "." and no leading-dot keys is the
			// shorthand for the root's own conditions.
			if entry, ok := asObject["."]; ok {
				if found := firstStringLeaf(entry); found != "" {
					return found
				}
			} else if !hasSubpathKeys(asObject) {
				if found := firstStringLeaf(manifest.Exports); found != "" {
					return found
				}
			}
		}
	}
	if main := strings.TrimPrefix(strings.TrimSpace(manifest.Main), "./"); main != "" {
		return path.Clean(main)
	}
	return ""
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

// firstStringLeaf walks a conditional-exports subtree and returns the first
// path it finds, preferring the conditions Node resolves first. An array is a
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
	var asObject map[string]json.RawMessage
	if err := json.Unmarshal(raw, &asObject); err != nil {
		return ""
	}
	// "types" is deliberately absent: it names a declaration file, never
	// something Node loads.
	for _, condition := range []string{"node", "import", "require", "default"} {
		if child, ok := asObject[condition]; ok {
			if found := firstStringLeaf(child); found != "" {
				return found
			}
		}
	}
	return ""
}

// BundleRowIDs reads the loader row ids a bundle's cordis.patch.yml inserts.
//
// The file is YAML with DSH's own `!!js` tag, whose value is an expression
// evaluated at compose time. A strict decode rejects the unknown tag outright,
// so the document is walked as nodes and tagged scalars are left alone — the
// row ids are what matter here, not the expressions.
func BundleRowIDs(body []byte) ([]string, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("the package's bundle patch is not valid YAML")
	}
	seen := map[string]bool{}
	var rows []string

	// collectRow reads one entry of an insert list. A group row carries its
	// children in `config` and marks itself with `group: true`; a plain row's
	// `config` is opaque plugin settings that may hold an unrelated `id`, so
	// the two cases must be told apart rather than both walked.
	var collectRow func(node *yaml.Node)
	collectRow = func(node *yaml.Node) {
		if node == nil || node.Kind != yaml.MappingNode {
			return
		}
		isGroup := false
		var id string
		var config *yaml.Node
		for i := 0; i+1 < len(node.Content); i += 2 {
			key, value := node.Content[i], node.Content[i+1]
			switch key.Value {
			case "id":
				if value.Kind == yaml.ScalarNode {
					id = value.Value
				}
			case "group":
				if value.Kind == yaml.ScalarNode && value.Value == "true" {
					isGroup = true
				}
				// Some bundles nest children directly under `group`.
				if value.Kind == yaml.SequenceNode {
					isGroup = true
					config = value
				}
			case "config":
				config = value
			}
		}
		if id != "" && !seen[id] {
			seen[id] = true
			rows = append(rows, id)
		}
		if isGroup && config != nil && config.Kind == yaml.SequenceNode {
			for _, child := range config.Content {
				collectRow(child)
			}
		}
	}

	// Only rows an `insert` actually adds are this bundle's rows. An `id` on a
	// patch that modifies somebody else's row is a reference, not a row this
	// package declares, and treating it as one produces a config target that
	// does not belong to the plugin.
	var walkPatches func(node *yaml.Node)
	walkPatches = func(node *yaml.Node) {
		if node == nil {
			return
		}
		switch node.Kind {
		case yaml.DocumentNode, yaml.SequenceNode:
			for _, child := range node.Content {
				walkPatches(child)
			}
		case yaml.MappingNode:
			for i := 0; i+1 < len(node.Content); i += 2 {
				key, value := node.Content[i], node.Content[i+1]
				if key.Value != "insert" || value.Kind != yaml.SequenceNode {
					continue
				}
				for _, entry := range value.Content {
					collectRow(entry)
				}
			}
		}
	}
	walkPatches(&doc)
	return rows, nil
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
