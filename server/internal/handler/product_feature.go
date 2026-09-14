package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const (
	productFeatureDefaultPageSize = 30
	productFeatureMaxPageSize     = 100
	productFeatureMaxQueryRunes   = 200
	productFeatureMaxSearchTerms  = 10
)

var productFeatureSlugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

const productFeatureReleaseSelectColumns = `
	r.id,
	r.feature_id,
	f.slug,
	r.release_type,
	r.title,
	r.description,
	r.use_cases,
	r.usage_guide,
	r.version_label,
	r.image_requirement,
	r.required_image_version,
	r.previous_release_id,
	r.published_at,
	r.created_at`

const productFeatureSearchText = `LOWER(CONCAT_WS(
	' ',
	r.title,
	r.description,
	r.use_cases,
	r.usage_guide,
	r.version_label,
	COALESCE(r.required_image_version, '')
))`

type PublishProductFeatureReleaseRequest struct {
	FeatureSlug          string     `json:"feature_slug"`
	ReleaseType          string     `json:"release_type"`
	Title                string     `json:"title"`
	Description          string     `json:"description"`
	UseCases             string     `json:"use_cases"`
	UsageGuide           string     `json:"usage_guide"`
	VersionLabel         string     `json:"version_label"`
	ImageRequirement     string     `json:"image_requirement"`
	RequiredImageVersion *string    `json:"required_image_version"`
	PublishedAt          *time.Time `json:"published_at"`
}

type ProductFeatureReleaseSummaryResponse struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	VersionLabel string `json:"version_label"`
	PublishedAt  string `json:"published_at"`
}

type ProductFeatureReleaseResponse struct {
	ID                   string                                `json:"id"`
	FeatureID            string                                `json:"feature_id"`
	FeatureSlug          string                                `json:"feature_slug"`
	ReleaseType          string                                `json:"release_type"`
	Title                string                                `json:"title"`
	Description          string                                `json:"description"`
	UseCases             string                                `json:"use_cases"`
	UsageGuide           string                                `json:"usage_guide"`
	VersionLabel         string                                `json:"version_label"`
	ImageRequirement     string                                `json:"image_requirement"`
	RequiredImageVersion *string                               `json:"required_image_version"`
	RequiresImageUpgrade bool                                  `json:"requires_image_upgrade"`
	PreviousReleaseID    *string                               `json:"previous_release_id"`
	PreviousRelease      *ProductFeatureReleaseSummaryResponse `json:"previous_release,omitempty"`
	PublishedAt          string                                `json:"published_at"`
	CreatedAt            string                                `json:"created_at"`
}

type productFeatureReleaseRow struct {
	ID                   pgtype.UUID
	FeatureID            pgtype.UUID
	FeatureSlug          string
	ReleaseType          string
	Title                string
	Description          string
	UseCases             string
	UsageGuide           string
	VersionLabel         string
	ImageRequirement     string
	RequiredImageVersion pgtype.Text
	PreviousReleaseID    pgtype.UUID
	PublishedAt          pgtype.Timestamptz
	CreatedAt            pgtype.Timestamptz
}

func (row *productFeatureReleaseRow) scanTargets() []any {
	return []any{
		&row.ID,
		&row.FeatureID,
		&row.FeatureSlug,
		&row.ReleaseType,
		&row.Title,
		&row.Description,
		&row.UseCases,
		&row.UsageGuide,
		&row.VersionLabel,
		&row.ImageRequirement,
		&row.RequiredImageVersion,
		&row.PreviousReleaseID,
		&row.PublishedAt,
		&row.CreatedAt,
	}
}

func productFeatureReleaseToResponse(row productFeatureReleaseRow) ProductFeatureReleaseResponse {
	return ProductFeatureReleaseResponse{
		ID:                   uuidToString(row.ID),
		FeatureID:            uuidToString(row.FeatureID),
		FeatureSlug:          row.FeatureSlug,
		ReleaseType:          row.ReleaseType,
		Title:                row.Title,
		Description:          row.Description,
		UseCases:             row.UseCases,
		UsageGuide:           row.UsageGuide,
		VersionLabel:         row.VersionLabel,
		ImageRequirement:     row.ImageRequirement,
		RequiredImageVersion: textToPtr(row.RequiredImageVersion),
		RequiresImageUpgrade: row.ImageRequirement != "none",
		PreviousReleaseID:    uuidToPtr(row.PreviousReleaseID),
		PublishedAt:          timestampToString(row.PublishedAt),
		CreatedAt:            timestampToString(row.CreatedAt),
	}
}

func decodeProductFeatureReleaseRequest(w http.ResponseWriter, r *http.Request) (PublishProductFeatureReleaseRequest, bool) {
	var request PublishProductFeatureReleaseRequest
	r.Body = http.MaxBytesReader(w, r.Body, 512<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
		} else {
			writeError(w, http.StatusBadRequest, "invalid request body")
		}
		return PublishProductFeatureReleaseRequest{}, false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return PublishProductFeatureReleaseRequest{}, false
	}
	return request, true
}

func normalizeProductFeatureReleaseRequest(
	w http.ResponseWriter,
	request PublishProductFeatureReleaseRequest,
) (PublishProductFeatureReleaseRequest, bool) {
	request.FeatureSlug = strings.ToLower(strings.TrimSpace(request.FeatureSlug))
	request.ReleaseType = strings.TrimSpace(request.ReleaseType)
	request.Title = strings.TrimSpace(request.Title)
	request.Description = strings.TrimSpace(request.Description)
	request.UseCases = strings.TrimSpace(request.UseCases)
	request.UsageGuide = strings.TrimSpace(request.UsageGuide)
	request.VersionLabel = strings.TrimSpace(request.VersionLabel)
	request.ImageRequirement = strings.TrimSpace(request.ImageRequirement)
	if request.RequiredImageVersion != nil {
		trimmed := strings.TrimSpace(*request.RequiredImageVersion)
		request.RequiredImageVersion = &trimmed
	}

	if !productFeatureSlugPattern.MatchString(request.FeatureSlug) || len(request.FeatureSlug) > 100 {
		writeError(w, http.StatusBadRequest, "feature_slug must contain lowercase letters, numbers, and single hyphens")
		return PublishProductFeatureReleaseRequest{}, false
	}
	if request.ReleaseType != "new" && request.ReleaseType != "improvement" {
		writeError(w, http.StatusBadRequest, "release_type must be new or improvement")
		return PublishProductFeatureReleaseRequest{}, false
	}
	if request.Title == "" || request.Description == "" || request.UseCases == "" || request.UsageGuide == "" {
		writeError(w, http.StatusBadRequest, "title, description, use_cases, and usage_guide are required")
		return PublishProductFeatureReleaseRequest{}, false
	}
	if request.ImageRequirement == "" {
		request.ImageRequirement = "none"
	}
	switch request.ImageRequirement {
	case "none":
		if request.RequiredImageVersion != nil && *request.RequiredImageVersion != "" {
			writeError(w, http.StatusBadRequest, "required_image_version must be empty when image_requirement is none")
			return PublishProductFeatureReleaseRequest{}, false
		}
		request.RequiredImageVersion = nil
	case "latest_at_publish", "min_version":
		if request.RequiredImageVersion == nil || *request.RequiredImageVersion == "" {
			writeError(w, http.StatusBadRequest, "required_image_version is required for image upgrades")
			return PublishProductFeatureReleaseRequest{}, false
		}
	default:
		writeError(w, http.StatusBadRequest, "image_requirement must be none, latest_at_publish, or min_version")
		return PublishProductFeatureReleaseRequest{}, false
	}
	if request.PublishedAt == nil {
		now := time.Now().UTC()
		request.PublishedAt = &now
	} else {
		publishedAt := request.PublishedAt.UTC()
		request.PublishedAt = &publishedAt
	}
	return request, true
}

func scanProductFeatureRelease(scanner interface{ Scan(...any) error }) (productFeatureReleaseRow, error) {
	var row productFeatureReleaseRow
	err := scanner.Scan(row.scanTargets()...)
	return row, err
}

func getLatestProductFeatureRelease(
	ctx context.Context,
	tx pgx.Tx,
	featureID pgtype.UUID,
) (productFeatureReleaseRow, error) {
	query := `SELECT ` + productFeatureReleaseSelectColumns + `
		FROM product_feature_release r
		JOIN product_feature f ON f.id = r.feature_id
		WHERE r.feature_id = $1
		ORDER BY r.published_at DESC, r.id DESC
		LIMIT 1`
	return scanProductFeatureRelease(tx.QueryRow(ctx, query, featureID))
}

// PublishProductFeatureRelease appends one immutable public release. A new
// feature may have only one first release; every improvement locks the stable
// feature row and links to the latest release in the same transaction.
func (h *Handler) PublishProductFeatureRelease(w http.ResponseWriter, r *http.Request) {
	request, ok := decodeProductFeatureReleaseRequest(w, r)
	if !ok {
		return
	}
	request, ok = normalizeProductFeatureReleaseRequest(w, request)
	if !ok {
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start feature release publication")
		return
	}
	defer tx.Rollback(r.Context())

	var featureID pgtype.UUID
	var featureStatus string
	err = tx.QueryRow(
		r.Context(),
		`SELECT id, status FROM product_feature WHERE slug = $1 FOR UPDATE`,
		request.FeatureSlug,
	).Scan(&featureID, &featureStatus)
	featureExists := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "failed to load product feature")
		return
	}
	if !featureExists {
		if request.ReleaseType != "new" {
			writeError(w, http.StatusConflict, "an improvement requires an existing feature release")
			return
		}
		if err := tx.QueryRow(
			r.Context(),
			`INSERT INTO product_feature (slug) VALUES ($1) RETURNING id, status`,
			request.FeatureSlug,
		).Scan(&featureID, &featureStatus); err != nil {
			if isUniqueViolation(err) {
				writeError(w, http.StatusConflict, "feature slug already exists")
				return
			}
			writeError(w, http.StatusInternalServerError, "failed to create product feature")
			return
		}
	}
	if featureStatus != "active" {
		writeError(w, http.StatusConflict, "archived features cannot receive releases")
		return
	}

	latest, latestErr := getLatestProductFeatureRelease(r.Context(), tx, featureID)
	hasLatest := latestErr == nil
	if latestErr != nil && !errors.Is(latestErr, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "failed to load feature release history")
		return
	}
	if request.ReleaseType == "new" && hasLatest {
		writeError(w, http.StatusConflict, "feature already has a first release; publish an improvement")
		return
	}
	if request.ReleaseType == "improvement" && !hasLatest {
		writeError(w, http.StatusConflict, "an improvement requires an existing feature release")
		return
	}

	var previousReleaseID pgtype.UUID
	if hasLatest {
		if !request.PublishedAt.After(latest.PublishedAt.Time) {
			writeError(w, http.StatusConflict, "published_at must be later than the previous feature release")
			return
		}
		previousReleaseID = latest.ID
	}

	insertQuery := `WITH inserted AS (
		INSERT INTO product_feature_release (
			feature_id, release_type, title, description, use_cases, usage_guide,
			version_label, image_requirement, required_image_version,
			previous_release_id, published_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING *
	)
	SELECT
		i.id, i.feature_id, $12::text, i.release_type, i.title, i.description,
		i.use_cases, i.usage_guide, i.version_label, i.image_requirement,
		i.required_image_version, i.previous_release_id, i.published_at, i.created_at
	FROM inserted i`
	row, err := scanProductFeatureRelease(tx.QueryRow(
		r.Context(),
		insertQuery,
		featureID,
		request.ReleaseType,
		request.Title,
		request.Description,
		request.UseCases,
		request.UsageGuide,
		request.VersionLabel,
		request.ImageRequirement,
		request.RequiredImageVersion,
		previousReleaseID,
		request.PublishedAt,
		request.FeatureSlug,
	))
	if err != nil {
		if isCheckViolation(err) {
			writeError(w, http.StatusBadRequest, "invalid feature release")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to publish feature release")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to publish feature release")
		return
	}
	writeJSON(w, http.StatusCreated, productFeatureReleaseToResponse(row))
}

func buildProductFeatureReleaseListQuery(search string, limit, offset int) (string, []any) {
	terms := splitSearchTerms(search)
	if len(terms) > productFeatureMaxSearchTerms {
		terms = terms[:productFeatureMaxSearchTerms]
	}
	args := make([]any, 0, len(terms)+2)
	conditions := make([]string, 0, len(terms))
	for _, term := range terms {
		args = append(args, "%"+escapeLike(strings.ToLower(term))+"%")
		conditions = append(conditions, fmt.Sprintf("%s LIKE $%d", productFeatureSearchText, len(args)))
	}
	whereSearch := ""
	if len(conditions) > 0 {
		whereSearch = " AND " + strings.Join(conditions, " AND ")
	}
	args = append(args, limit, offset)
	limitParam := len(args) - 1
	offsetParam := len(args)
	query := fmt.Sprintf(`SELECT %s, COUNT(*) OVER() AS total_count
		FROM product_feature_release r
		JOIN product_feature f ON f.id = r.feature_id
		WHERE f.status = 'active'
		  AND r.published_at <= now()%s
		ORDER BY r.published_at DESC, r.id DESC
		LIMIT $%d OFFSET $%d`, productFeatureReleaseSelectColumns, whereSearch, limitParam, offsetParam)
	return query, args
}

// ListProductFeatureReleases returns the global product release feed. Search
// is AND-based across whitespace-separated terms and never changes the
// required newest-first ordering.
func (h *Handler) ListProductFeatureReleases(w http.ResponseWriter, r *http.Request) {
	search := strings.TrimSpace(r.URL.Query().Get("q"))
	if utf8.RuneCountInString(search) > productFeatureMaxQueryRunes {
		writeError(w, http.StatusBadRequest, "q is too long")
		return
	}
	limit := parseBoundedInt(r.URL.Query().Get("limit"), productFeatureDefaultPageSize, productFeatureMaxPageSize)
	offset := parseBoundedInt(r.URL.Query().Get("offset"), 0, 1_000_000)
	query, args := buildProductFeatureReleaseListQuery(search, limit, offset)

	releases := make([]ProductFeatureReleaseResponse, 0)
	var total int64
	err := runSearchQuery(r.Context(), h.TxStarter, query, args, func(rows pgx.Rows) error {
		for rows.Next() {
			var row productFeatureReleaseRow
			var rowTotal int64
			targets := append(row.scanTargets(), &rowTotal)
			if err := rows.Scan(targets...); err != nil {
				return err
			}
			total = rowTotal
			releases = append(releases, productFeatureReleaseToResponse(row))
		}
		return rows.Err()
	})
	if err != nil {
		writeSearchQueryFailure(w, err, "feature releases", "global", search)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"releases": releases,
		"total":    total,
		"limit":    limit,
		"offset":   offset,
	})
}

func (h *Handler) loadPublishedProductFeatureRelease(
	request *http.Request,
	id pgtype.UUID,
) (productFeatureReleaseRow, error) {
	query := `SELECT ` + productFeatureReleaseSelectColumns + `
		FROM product_feature_release r
		JOIN product_feature f ON f.id = r.feature_id
		WHERE r.id = $1
		  AND f.status = 'active'
		  AND r.published_at <= now()`
	return scanProductFeatureRelease(h.DB.QueryRow(request.Context(), query, id))
}

// GetProductFeatureRelease returns one published release and the compact
// previous-version link recorded at publication time.
func (h *Handler) GetProductFeatureRelease(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "feature release id")
	if !ok {
		return
	}
	row, err := h.loadPublishedProductFeatureRelease(r, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "feature release not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load feature release")
		return
	}
	response := productFeatureReleaseToResponse(row)
	if row.PreviousReleaseID.Valid {
		previous, previousErr := h.loadPublishedProductFeatureRelease(r, row.PreviousReleaseID)
		if previousErr == nil && previous.FeatureID == row.FeatureID {
			response.PreviousRelease = &ProductFeatureReleaseSummaryResponse{
				ID:           uuidToString(previous.ID),
				Title:        previous.Title,
				VersionLabel: previous.VersionLabel,
				PublishedAt:  timestampToString(previous.PublishedAt),
			}
		} else if previousErr != nil && !errors.Is(previousErr, pgx.ErrNoRows) {
			writeError(w, http.StatusInternalServerError, "failed to load previous feature release")
			return
		}
	}
	writeJSON(w, http.StatusOK, response)
}
