package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/dshplugin"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// The DSH plugin browse surface.
//
// DeepSeek publishes no plugin catalog — the harness ships no registry, index,
// or search endpoint, and `dsh plugin search` simply forwards to pnpm, which
// hits whatever npm registry is configured. So browsing has exactly two honest
// sources, and both are read rather than built here:
//
//   - the community `awesome-dsh-plugin` index, published as a CC0-licensed
//     npm package, cached in Postgres so search and paging are one query;
//   - the npm registry's own search endpoint, which is the same path the DSH
//     CLI reaches through pnpm.
//
// Nothing is crawled, and neither source is presented as official.

const (
	dshCatalogDefaultPageSize = 30
	dshCatalogMaxPageSize     = 100
	dshCatalogRefreshTimeout  = 3 * time.Minute
)

// DshPluginCatalogEntryResponse is one row of the community index.
type DshPluginCatalogEntryResponse struct {
	Name          string `json:"name"`
	Owner         string `json:"owner"`
	URL           string `json:"url"`
	Page          string `json:"page"`
	Category      string `json:"category"`
	DescriptionEN string `json:"description_en"`
	DescriptionZH string `json:"description_zh"`
	NpmPackage    string `json:"npm_package"`
	NpmVersion    string `json:"npm_version"`
	Stars         int32  `json:"stars"`
	Downloads     int64  `json:"downloads"`
	AddedOn       string `json:"added_on"`
	// SourceSpec is what to POST back to import this entry. Empty when the
	// entry names no installable npm release.
	SourceSpec string `json:"source_spec"`
}

// DshPluginCatalogStateResponse describes the cache and, importantly, says
// plainly where the data comes from.
type DshPluginCatalogStateResponse struct {
	Catalog        string `json:"catalog"`
	CatalogVersion string `json:"catalog_version"`
	EntryCount     int64  `json:"entry_count"`
	RefreshedAt    string `json:"refreshed_at"`
	SourcePackage  string `json:"source_package"`
	SourceRepo     string `json:"source_repo"`
	SourceSite     string `json:"source_site"`
	License        string `json:"license"`
	// Official is always false: this is a community index, not a DeepSeek one.
	Official bool `json:"official"`
}

func dshCatalogState(row db.GetDshPluginCatalogStateRow) DshPluginCatalogStateResponse {
	return DshPluginCatalogStateResponse{
		Catalog:        dshplugin.CatalogName,
		CatalogVersion: row.CatalogVersion,
		EntryCount:     row.EntryCount,
		RefreshedAt:    timestampToString(row.RefreshedAt),
		SourcePackage:  dshplugin.CatalogPackage,
		SourceRepo:     dshplugin.CatalogRepoURL,
		SourceSite:     dshplugin.CatalogSiteURL,
		License:        dshplugin.CatalogLicense,
		Official:       false,
	}
}

func catalogEntryToResponse(row db.DshPluginCatalogEntry) DshPluginCatalogEntryResponse {
	resp := DshPluginCatalogEntryResponse{
		Name:          row.Name,
		Owner:         row.Owner,
		URL:           row.Url,
		Page:          row.Page,
		Category:      row.Category,
		DescriptionEN: row.DescriptionEn,
		DescriptionZH: row.DescriptionZh,
		NpmPackage:    row.NpmPackage,
		NpmVersion:    row.NpmVersion,
		Stars:         row.Stars,
		Downloads:     row.Downloads,
	}
	if row.AddedOn.Valid {
		resp.AddedOn = row.AddedOn.Time.Format("2006-01-02")
	}
	if row.NpmPackage != "" && row.NpmVersion != "" {
		resp.SourceSpec = "npm:" + row.NpmPackage + "@" + row.NpmVersion
	}
	return resp
}

// BrowseDshPluginCatalog searches the cached community index.
func (h *Handler) BrowseDshPluginCatalog(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	limit := parseBoundedInt(query.Get("limit"), dshCatalogDefaultPageSize, dshCatalogMaxPageSize)
	offset := parseBoundedInt(query.Get("offset"), 1, 1_000_000) - 1
	if strings.TrimSpace(query.Get("offset")) == "" {
		offset = 0
	}
	search := strings.TrimSpace(query.Get("q"))
	category := strings.TrimSpace(query.Get("category"))
	// Default to hiding entries with no installable npm release: they cannot be
	// imported, so listing them by default is a dead end.
	installableOnly := query.Get("installable_only") != "false"

	entries, err := h.Queries.SearchDshPluginCatalog(r.Context(), db.SearchDshPluginCatalogParams{
		Catalog:         dshplugin.CatalogName,
		Category:        category,
		Query:           search,
		InstallableOnly: installableOnly,
		RowLimit:        int32(limit),
		RowOffset:       int32(offset),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to search the DSH plugin catalog")
		return
	}
	total, err := h.Queries.CountDshPluginCatalog(r.Context(), db.CountDshPluginCatalogParams{
		Catalog:         dshplugin.CatalogName,
		Category:        category,
		Query:           search,
		InstallableOnly: installableOnly,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to count the DSH plugin catalog")
		return
	}
	state, err := h.Queries.GetDshPluginCatalogState(r.Context(), dshplugin.CatalogName)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read the DSH plugin catalog state")
		return
	}

	resp := make([]DshPluginCatalogEntryResponse, len(entries))
	for i, row := range entries {
		resp[i] = catalogEntryToResponse(row)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"entries": resp,
		"total":   total,
		"limit":   limit,
		"offset":  offset,
		"state":   dshCatalogState(state),
	})
}

// ListDshPluginCatalogCategories returns the category facets with counts.
func (h *Handler) ListDshPluginCatalogCategories(w http.ResponseWriter, r *http.Request) {
	rows, err := h.Queries.ListDshPluginCatalogCategories(r.Context(), dshplugin.CatalogName)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list DSH plugin categories")
		return
	}
	type categoryResponse struct {
		Category   string `json:"category"`
		EntryCount int64  `json:"entry_count"`
	}
	resp := make([]categoryResponse, len(rows))
	for i, row := range rows {
		resp[i] = categoryResponse{Category: row.Category, EntryCount: row.EntryCount}
	}
	writeJSON(w, http.StatusOK, resp)
}

// RefreshDshPluginCatalog pulls the published index and replaces the cache.
//
// The whole replacement is one transaction under one advisory lock, and both
// parts are load-bearing:
//
//   - Without the lock, two refreshes of different snapshots interleave and
//     each prunes the other's rows by version, which can empty the catalog
//     while both calls report success.
//   - Without the transaction, a refresh that fails halfway leaves rows stamped
//     with the new version, so max(catalog_version) advances, the next
//     conditional request gets a 304, and the missing rows are never written.
//     Rolling back keeps the recorded version describing a complete snapshot.
func (h *Handler) RefreshDshPluginCatalog(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireUserID(w, r); !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), dshCatalogRefreshTimeout)
	defer cancel()

	// The origin is freshest but is served from GitHub Pages, which is
	// unreliable from inside China; the npm mirror exists for exactly that
	// reason. Try the origin, fall back, and report which one answered.
	knownVersion := ""
	if current, err := h.Queries.GetDshPluginCatalogState(ctx, dshplugin.CatalogName); err == nil {
		knownVersion = current.CatalogVersion
	}
	fetched, err := dshPluginResolver().FetchCatalogFrom(ctx, knownVersion)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	if fetched.NotModified {
		state, err := h.Queries.GetDshPluginCatalogState(ctx, dshplugin.CatalogName)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to read the DSH plugin catalog state")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"written": 0,
			"pruned":  0,
			"origin":  fetched.Origin,
			"state":   dshCatalogState(state),
		})
		return
	}
	doc := fetched.Document

	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start the catalog refresh")
		return
	}
	defer tx.Rollback(ctx)
	// Transaction-scoped, so it releases on commit or rollback and a crashed
	// replica cannot wedge future refreshes.
	if _, err := tx.Exec(ctx,
		"SELECT pg_advisory_xact_lock(hashtextextended($1, 0))",
		"dsh_plugin_catalog:"+dshplugin.CatalogName); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to lock the catalog for refresh")
		return
	}
	qtx := h.Queries.WithTx(tx)

	// ON CONFLICT cannot touch the same row twice in one statement, so the
	// snapshot is de-duplicated by the key the unique index uses. A community
	// index can carry a repeated owner/name pair; last one wins, as it would
	// have with row-at-a-time upserts.
	type catalogRow struct {
		Name          string  `json:"name"`
		Owner         string  `json:"owner"`
		URL           string  `json:"url"`
		Page          string  `json:"page"`
		Category      string  `json:"category"`
		DescriptionEN string  `json:"description_en"`
		DescriptionZH string  `json:"description_zh"`
		NpmPackage    string  `json:"npm_package"`
		NpmVersion    string  `json:"npm_version"`
		TarballURL    string  `json:"tarball_url"`
		Stars         int32   `json:"stars"`
		Downloads     int64   `json:"downloads"`
		InstallHint   string  `json:"install_hint"`
		AddedOn       *string `json:"added_on"`
	}
	seen := make(map[string]int, len(doc.Plugins))
	rows := make([]catalogRow, 0, len(doc.Plugins))
	for _, entry := range doc.Plugins {
		if strings.TrimSpace(entry.Name) == "" {
			continue
		}
		var added *string
		if parsed, err := time.Parse("2006-01-02", strings.TrimSpace(entry.Added)); err == nil {
			formatted := parsed.Format("2006-01-02")
			added = &formatted
		}
		row := catalogRow{
			Name:          entry.Name,
			Owner:         entry.Owner,
			URL:           entry.URL,
			Page:          entry.Page,
			Category:      entry.Category.String(),
			DescriptionEN: entry.Description.EN,
			DescriptionZH: entry.Description.ZH,
			NpmPackage:    entry.NPM,
			NpmVersion:    entry.Version,
			// Community-submitted rows can name a trusted repository while
			// pointing the tarball somewhere else; only a URL that belongs to
			// the entry's own repo is recorded.
			TarballURL:  entry.TrustedTarball(),
			Stars:       entry.Stars,
			Downloads:   entry.Downloads,
			InstallHint: entry.Install,
			AddedOn:     added,
		}
		key := strings.ToLower(entry.Owner) + "/" + strings.ToLower(entry.Name)
		if index, ok := seen[key]; ok {
			rows[index] = row
			continue
		}
		seen[key] = len(rows)
		rows = append(rows, row)
	}
	written := len(rows)

	payload, err := json.Marshal(rows)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode the catalog snapshot")
		return
	}
	if err := qtx.BulkUpsertDshPluginCatalogEntries(ctx, db.BulkUpsertDshPluginCatalogEntriesParams{
		Catalog:        dshplugin.CatalogName,
		CatalogVersion: doc.Version,
		Entries:        payload,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to write the catalog snapshot")
		return
	}

	pruned, err := qtx.DeleteStaleDshPluginCatalogEntries(ctx,
		db.DeleteStaleDshPluginCatalogEntriesParams{
			Catalog:        dshplugin.CatalogName,
			CatalogVersion: doc.Version,
		})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to prune stale catalog entries")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit the catalog refresh")
		return
	}
	state, err := h.Queries.GetDshPluginCatalogState(ctx, dshplugin.CatalogName)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read the DSH plugin catalog state")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"written": written,
		"pruned":  pruned,
		"origin":  fetched.Origin,
		"state":   dshCatalogState(state),
	})
}

// SearchDshPluginRegistry proxies the npm registry's own search endpoint.
//
// This is the closest thing to an official discovery route: it is the same
// endpoint `dsh plugin --profile <p> search <text>` reaches through pnpm.
func (h *Handler) SearchDshPluginRegistry(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		writeError(w, http.StatusBadRequest, "a search query is required")
		return
	}
	limit := parseBoundedInt(r.URL.Query().Get("limit"), 25, 100)
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	results, err := dshPluginResolver().SearchRegistry(ctx, query, limit)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"code":  "upstream_unavailable",
			"error": err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"results": results,
		"source":  "npm-registry-search",
	})
}
