package dshplugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Where the community index comes from.
//
// The project publishes the same document two ways, and both are used here for
// a reason. The site is the origin and is always the freshest, but it is served
// from GitHub Pages, which is unreliable from inside China — which is exactly
// why the npm package exists in the first place. So: try the origin, fall back
// to the registry, and say which one answered.
const (
	// CatalogOriginURL is the published document itself. It sends an ETag and
	// Last-Modified and allows cross-origin reads.
	CatalogOriginURL = CatalogSiteURL + "/plugins.json"
	// maxCatalogBytes bounds either path. The document is around 3 MB.
	maxCatalogBytes = 24 << 20
)

// CatalogFetch is one refresh result, including which source answered so an
// operator can tell a stale mirror from a stale cache.
type CatalogFetch struct {
	Document *CatalogDocument
	// Origin is "site" or "npm".
	Origin string
	// Version identifies the snapshot: the npm package version on the registry
	// path, or the origin's ETag on the site path.
	Version string
	// NotModified is true when the caller's ETag still matches and Document is
	// nil — nothing needs rewriting.
	NotModified bool
}

// FetchCatalogFrom retrieves the community index, preferring the origin.
//
// knownETag, when non-empty, turns the origin request into a conditional GET,
// so an unchanged catalog costs a 304 rather than three megabytes.
func (r *Resolver) FetchCatalogFrom(ctx context.Context, knownETag string) (*CatalogFetch, error) {
	fetch, siteErr := r.fetchCatalogFromSite(ctx, knownETag)
	if siteErr == nil {
		return fetch, nil
	}
	doc, npmErr := r.FetchCatalog(ctx)
	if npmErr != nil {
		return nil, fmt.Errorf("the catalog origin failed (%v) and the npm mirror failed (%v)", siteErr, npmErr)
	}
	return &CatalogFetch{Document: doc, Origin: "npm", Version: doc.Version}, nil
}

func (r *Resolver) fetchCatalogFromSite(ctx context.Context, knownETag string) (*CatalogFetch, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, CatalogOriginURL, nil)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(knownETag) != "" {
		req.Header.Set("If-None-Match", knownETag)
	}
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to reach the catalog origin: %w", err)
	}
	defer resp.Body.Close()

	etag := strings.TrimSpace(resp.Header.Get("ETag"))
	if resp.StatusCode == http.StatusNotModified {
		return &CatalogFetch{Origin: "site", Version: etag, NotModified: true}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the catalog origin returned status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCatalogBytes+1))
	if err != nil {
		return nil, fmt.Errorf("failed to read the catalog: %w", err)
	}
	if len(body) > maxCatalogBytes {
		return nil, fmt.Errorf("the catalog is larger than the %d byte limit", int64(maxCatalogBytes))
	}
	doc, err := parseCatalogDocument(body)
	if err != nil {
		return nil, err
	}
	if etag == "" {
		// No validator: fall back to the payload digest so a refresh is still
		// keyed by content rather than by wall-clock time.
		sum := sha256.Sum256(body)
		etag = "sha256-" + hex.EncodeToString(sum[:])
	}
	doc.Version = etag
	return &CatalogFetch{Document: doc, Origin: "site", Version: etag}, nil
}

func parseCatalogDocument(body []byte) (*CatalogDocument, error) {
	var doc CatalogDocument
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("the catalog document could not be parsed")
	}
	if len(doc.Plugins) == 0 {
		return nil, fmt.Errorf("the catalog document lists no plugins")
	}
	if len(doc.Plugins) > maxCatalogEntries {
		return nil, fmt.Errorf("the catalog lists %d plugins, over the %d entry limit",
			len(doc.Plugins), maxCatalogEntries)
	}
	return &doc, nil
}

// TrustedTarball returns the entry's release tarball only when the URL actually
// belongs to the repository the entry names.
//
// The catalog is community-maintained and its rows are user-submitted, so an
// entry can name a well-known repository while pointing `tarball` at somebody
// else's release. Binding the URL to the entry's own owner/repo closes that,
// and costs nothing for a legitimate row.
func (e CatalogEntry) TrustedTarball() string {
	raw := strings.TrimSpace(e.Tarball)
	if raw == "" || e.Owner == "" || e.Name == "" {
		return ""
	}
	if !strings.HasPrefix(raw, "https://github.com/") {
		return ""
	}
	rest := strings.TrimPrefix(raw, "https://github.com/")
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) < 3 {
		return ""
	}
	// The entry's own `url` is the authority on which repository it is, so
	// BOTH segments must match it. Checking only the repo would let an entry
	// declare a trusted url while pointing the tarball at its own owner's fork.
	ownerFromURL, repoFromURL := "", ""
	if strings.HasPrefix(e.URL, "https://github.com/") {
		urlParts := strings.SplitN(strings.TrimPrefix(e.URL, "https://github.com/"), "/", 3)
		if len(urlParts) >= 2 {
			ownerFromURL, repoFromURL = urlParts[0], urlParts[1]
		}
	}
	if ownerFromURL != "" {
		if !strings.EqualFold(parts[0], ownerFromURL) || !strings.EqualFold(parts[1], repoFromURL) {
			return ""
		}
		// The declared owner field must agree with the url it claims.
		if !strings.EqualFold(ownerFromURL, e.Owner) {
			return ""
		}
		return raw
	}
	// No usable url to bind against: fall back to the declared owner/name pair.
	if !strings.EqualFold(parts[0], e.Owner) || !strings.EqualFold(parts[1], e.Name) {
		return ""
	}
	return raw
}
