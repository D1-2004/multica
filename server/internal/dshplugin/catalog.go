package dshplugin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// CatalogPackage is the npm package that publishes the community index.
//
// DeepSeek publishes no plugin catalog: `dsh plugin` is a pnpm passthrough and
// the harness ships no registry, index, or search endpoint of its own. The
// ecosystem's de-facto index is the community `awesome-dsh-plugin` project,
// which publishes its data as this CC0-licensed, zero-dependency npm package.
// Reading it is therefore both the least work and the closest thing to an
// upstream source — but it is a community list, and must never be presented as
// official.
const (
	CatalogPackage = "dsh-plugin-catalog"
	CatalogName    = "awesome-dsh-plugin"
	CatalogLicense = "CC0-1.0"
	CatalogSiteURL = "https://awesome-dsh-plugin.com"
	CatalogRepoURL = "https://github.com/awesome-dsh-plugin/awesome-dsh-plugin"
	// catalogDataFile is the single JSON document the package ships.
	catalogDataFile = "plugins.json"
	// maxCatalogEntries bounds one refresh. The published index is ~3200
	// entries; this leaves room to grow without letting a bad publish flood
	// the table.
	maxCatalogEntries = 50000
)

// CatalogEntry is one row of the published index.
type CatalogEntry struct {
	Name        string         `json:"name"`
	Owner       string         `json:"owner"`
	URL         string         `json:"url"`
	Page        string         `json:"page"`
	Category    FlexibleString `json:"category"`
	Description struct {
		EN string `json:"en"`
		ZH string `json:"zh"`
	} `json:"description"`
	NPM       string `json:"npm"`
	Tarball   string `json:"tarball"`
	Version   string `json:"version"`
	Stars     int32  `json:"stars"`
	Downloads int64  `json:"downloads"`
	Install   string `json:"install"`
	Added     string `json:"added"`
}

// Installable reports whether this entry carries enough to install without a
// human resolving anything by hand.
func (e CatalogEntry) Installable() bool {
	return strings.TrimSpace(e.NPM) != "" && strings.TrimSpace(e.Version) != ""
}

// SourceSpec is the spec to hand the resolver, in npm's own vocabulary.
func (e CatalogEntry) SourceSpec() string {
	if !e.Installable() {
		return ""
	}
	return "npm:" + e.NPM + "@" + e.Version
}

// FlexibleString accepts either a JSON string or an array of strings, keeping
// the first entry. The published catalog types `category` as a plain string
// today, but the project's own consumers allow a list, so a schema change must
// not break a refresh.
type FlexibleString string

// UnmarshalJSON implements json.Unmarshaler.
func (f *FlexibleString) UnmarshalJSON(data []byte) error {
	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		*f = FlexibleString(single)
		return nil
	}
	var many []string
	if err := json.Unmarshal(data, &many); err != nil {
		*f = ""
		return nil
	}
	if len(many) > 0 {
		*f = FlexibleString(many[0])
	}
	return nil
}

// String returns the plain value.
func (f FlexibleString) String() string { return string(f) }

// CatalogDocument is the whole published index.
type CatalogDocument struct {
	Name       string                       `json:"name"`
	URL        string                       `json:"url"`
	Source     string                       `json:"source"`
	Updated    string                       `json:"updated"`
	Count      int                          `json:"count"`
	Categories map[string]map[string]string `json:"categories"`
	Plugins    []CatalogEntry               `json:"plugins"`
	// Version is the npm version of the package the document came from. It is
	// not part of the document itself; the refresh stamps it so stale rows can
	// be pruned by version.
	Version string `json:"-"`
}

// FetchCatalog downloads the published community index and parses it. Nothing
// is crawled and no site is scraped: the index is an ordinary npm package, so
// the same registry path an install uses fetches it.
func (r *Resolver) FetchCatalog(ctx context.Context) (*CatalogDocument, error) {
	src, err := ParseSource("npm:" + CatalogPackage)
	if err != nil {
		return nil, err
	}
	meta, err := r.fetchRegistryVersion(ctx, src)
	if err != nil {
		return nil, err
	}
	if meta.Dist.Tarball == "" {
		return nil, fmt.Errorf("the registry published no tarball for %s", CatalogPackage)
	}
	data, err := r.fetch(ctx, src, meta.Dist.Tarball)
	if err != nil {
		return nil, err
	}
	files, err := archiveFiles(data)
	if err != nil {
		return nil, err
	}
	body, ok := files[catalogDataFile]
	if !ok {
		return nil, fmt.Errorf("%s@%s ships no %s", CatalogPackage, meta.Version, catalogDataFile)
	}
	doc, err := parseCatalogDocument(body)
	if err != nil {
		return nil, err
	}
	doc.Version = meta.Version
	return doc, nil
}

// RegistrySearchResult is one hit from the npm registry's own search endpoint.
//
// This is the same path `dsh plugin --profile <p> search <text>` reaches
// through pnpm, so it is the closest thing to an official discovery route:
// whatever registry is configured is the plugin source.
type RegistrySearchResult struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
	Publisher   string `json:"publisher"`
	Links       string `json:"links"`
}

type registrySearchResponse struct {
	Objects []struct {
		Package struct {
			Name        string `json:"name"`
			Version     string `json:"version"`
			Description string `json:"description"`
			Publisher   struct {
				Username string `json:"username"`
			} `json:"publisher"`
			Links map[string]string `json:"links"`
		} `json:"package"`
	} `json:"objects"`
}

// SearchRegistry queries the configured npm registry.
func (r *Resolver) SearchRegistry(ctx context.Context, text string, limit int) ([]RegistrySearchResult, error) {
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("a search query is required")
	}
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	endpoint := fmt.Sprintf("%s/-/v1/search?text=%s&size=%d",
		r.Registry, urlQueryEscape(text), limit)
	body, err := r.getJSON(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	var parsed registrySearchResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("the registry returned a search response this server could not parse")
	}
	out := make([]RegistrySearchResult, 0, len(parsed.Objects))
	for _, object := range parsed.Objects {
		pkg := object.Package
		if pkg.Name == "" {
			continue
		}
		out = append(out, RegistrySearchResult{
			Name:        pkg.Name,
			Version:     pkg.Version,
			Description: pkg.Description,
			Publisher:   pkg.Publisher.Username,
			Links:       pkg.Links["npm"],
		})
	}
	return out, nil
}
