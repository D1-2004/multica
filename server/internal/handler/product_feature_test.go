package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
)

type productFeatureReleaseTestResponse struct {
	ID                     string `json:"id"`
	FeatureID              string `json:"feature_id"`
	FeatureSlug            string `json:"feature_slug"`
	ReleaseType            string `json:"release_type"`
	Title                  string `json:"title"`
	Description            string `json:"description"`
	UseCases               string `json:"use_cases"`
	UsageGuide             string `json:"usage_guide"`
	VersionLabel           string `json:"version_label"`
	ImageRequirement       string `json:"image_requirement"`
	RequiredImageVersion   string `json:"required_image_version"`
	PreviousReleaseID      string `json:"previous_release_id"`
	PublishedAt            string `json:"published_at"`
	RequiresImageUpgrade   bool   `json:"requires_image_upgrade"`
	PreviousRelease        *struct {
		ID           string `json:"id"`
		Title        string `json:"title"`
		VersionLabel string `json:"version_label"`
		PublishedAt  string `json:"published_at"`
	} `json:"previous_release"`
}

type productFeatureReleaseListTestResponse struct {
	Releases []productFeatureReleaseTestResponse `json:"releases"`
	Total    int64                               `json:"total"`
	Limit    int                                 `json:"limit"`
	Offset   int                                 `json:"offset"`
}

func publishProductFeatureReleaseForTest(
	t *testing.T,
	payload map[string]any,
) productFeatureReleaseTestResponse {
	t.Helper()
	recorder := httptest.NewRecorder()
	testHandler.PublishProductFeatureRelease(
		recorder,
		newRequest(http.MethodPost, "/api/internal/features/releases", payload),
	)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("publish feature release status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response productFeatureReleaseTestResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode publish response: %v", err)
	}
	return response
}

func cleanupProductFeatureForTest(t *testing.T, slug string) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = testPool.Exec(ctx, `DELETE FROM product_feature_release WHERE feature_id IN (SELECT id FROM product_feature WHERE slug = $1)`, slug)
		_, _ = testPool.Exec(ctx, `DELETE FROM product_feature WHERE slug = $1`, slug)
	})
}

func TestProductFeatureReleaseHistoryAndOrdering(t *testing.T) {
	suffix := uuid.NewString()[:8]
	slug := "handler-feature-" + suffix
	marker := "ReleaseMarker" + suffix
	cleanupProductFeatureForTest(t, slug)

	firstPublishedAt := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	first := publishProductFeatureReleaseForTest(t, map[string]any{
		"feature_slug":          slug,
		"release_type":          "new",
		"title":                 marker + " launch",
		"description":           "First public release",
		"use_cases":             "Create a first release",
		"usage_guide":           "Open the feature page.",
		"version_label":         "1.0",
		"image_requirement":     "none",
		"required_image_version": nil,
		"published_at":          firstPublishedAt,
	})

	secondPublishedAt := firstPublishedAt.Add(time.Hour)
	second := publishProductFeatureReleaseForTest(t, map[string]any{
		"feature_slug":          slug,
		"release_type":          "improvement",
		"title":                 marker + " follow-up",
		"description":           "Adds a historical link",
		"use_cases":             "Review an earlier release",
		"usage_guide":           "Choose the previous version.",
		"version_label":         "1.1",
		"image_requirement":     "min_version",
		"required_image_version": "v0.18.3",
		"published_at":          secondPublishedAt,
	})

	if second.PreviousReleaseID != first.ID {
		t.Fatalf("previous_release_id=%q want %q", second.PreviousReleaseID, first.ID)
	}
	if !second.RequiresImageUpgrade {
		t.Fatal("min_version release must require an image upgrade")
	}

	listRecorder := httptest.NewRecorder()
	testHandler.ListProductFeatureReleases(
		listRecorder,
		newRequest(http.MethodGet, "/api/features?q="+marker, nil),
	)
	if listRecorder.Code != http.StatusOK {
		t.Fatalf("list feature releases status=%d body=%s", listRecorder.Code, listRecorder.Body.String())
	}
	var list productFeatureReleaseListTestResponse
	if err := json.Unmarshal(listRecorder.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if list.Total != 2 || len(list.Releases) != 2 {
		t.Fatalf("list=%#v want two releases", list)
	}
	if list.Releases[0].ID != second.ID || list.Releases[1].ID != first.ID {
		t.Fatalf("release order=%v want [%s %s]", []string{list.Releases[0].ID, list.Releases[1].ID}, second.ID, first.ID)
	}

	detailRecorder := httptest.NewRecorder()
	detailRequest := withURLParam(
		newRequest(http.MethodGet, "/api/features/"+second.ID, nil),
		"id",
		second.ID,
	)
	testHandler.GetProductFeatureRelease(detailRecorder, detailRequest)
	if detailRecorder.Code != http.StatusOK {
		t.Fatalf("get feature release status=%d body=%s", detailRecorder.Code, detailRecorder.Body.String())
	}
	var detail productFeatureReleaseTestResponse
	if err := json.Unmarshal(detailRecorder.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode detail response: %v", err)
	}
	if detail.PreviousRelease == nil || detail.PreviousRelease.ID != first.ID || detail.PreviousRelease.Title != first.Title {
		t.Fatalf("previous release=%#v want id=%s title=%q", detail.PreviousRelease, first.ID, first.Title)
	}
}

func TestProductFeatureReleaseSearchRequiresEveryTermAndHidesFutureReleases(t *testing.T) {
	suffix := uuid.NewString()[:8]
	slug := "handler-search-" + suffix
	marker := "SearchMarker" + suffix
	cleanupProductFeatureForTest(t, slug)

	publishProductFeatureReleaseForTest(t, map[string]any{
		"feature_slug":          slug,
		"release_type":          "new",
		"title":                 marker,
		"description":           "workspace automation",
		"use_cases":             "Review releases",
		"usage_guide":           "Search with several words.",
		"image_requirement":     "none",
		"required_image_version": nil,
		"published_at":          time.Now().UTC().Add(-time.Hour),
	})
	publishProductFeatureReleaseForTest(t, map[string]any{
		"feature_slug":          slug,
		"release_type":          "improvement",
		"title":                 marker + " scheduled",
		"description":           "workspace automation",
		"use_cases":             "Future release",
		"usage_guide":           "Wait until publication.",
		"image_requirement":     "none",
		"required_image_version": nil,
		"published_at":          time.Now().UTC().Add(time.Hour),
	})

	recorder := httptest.NewRecorder()
	testHandler.ListProductFeatureReleases(
		recorder,
		newRequest(http.MethodGet, "/api/features?q="+marker+"+automation", nil),
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("list feature releases status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var list productFeatureReleaseListTestResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if list.Total != 1 || len(list.Releases) != 1 || list.Releases[0].Title != marker {
		t.Fatalf("filtered releases=%#v want only the published multi-term match", list.Releases)
	}
}
