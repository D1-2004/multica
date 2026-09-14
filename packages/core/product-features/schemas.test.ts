import { describe, expect, it } from "vitest";
import { parseWithFallback } from "../api/schema";
import {
  EMPTY_PRODUCT_FEATURE_RELEASE,
  EMPTY_PRODUCT_FEATURE_RELEASE_PAGE,
  ProductFeatureReleasePageSchema,
  ProductFeatureReleaseSchema,
} from "../api/schemas";

const rawRelease = {
  id: "release-2",
  feature_id: "feature-1",
  feature_slug: "agent-events",
  release_type: "improvement",
  title: "Agent events support filters",
  description: "Filter event subscriptions.",
  use_cases: "Reduce irrelevant triggers.",
  usage_guide: "Open Events and add a filter.",
  version_label: "2026.09.14",
  image_requirement: "min_version",
  required_image_version: "multica-runtime:2026.09.14",
  requires_image_upgrade: true,
  previous_release_id: "release-1",
  previous_release: {
    id: "release-1",
    title: "Agent events",
    version_label: "2026.09.01",
    published_at: "2026-09-01T08:00:00Z",
  },
  published_at: "2026-09-14T08:00:00Z",
  created_at: "2026-09-14T08:00:00Z",
};

describe("product feature release schemas", () => {
  it("parses snake_case release history into camelCase values", () => {
    const parsed = ProductFeatureReleaseSchema.parse(rawRelease);

    expect(parsed).toMatchObject({
      featureId: "feature-1",
      featureSlug: "agent-events",
      releaseType: "improvement",
      imageRequirement: "min_version",
      requiredImageVersion: "multica-runtime:2026.09.14",
      requiresImageUpgrade: true,
      previousReleaseId: "release-1",
      previousRelease: {
        id: "release-1",
        versionLabel: "2026.09.01",
      },
    });
  });

  it("falls back when a page contains a malformed release", () => {
    const parsed = parseWithFallback(
      { releases: [{ ...rawRelease, published_at: 42 }], total: 1, limit: 30, offset: 0 },
      ProductFeatureReleasePageSchema,
      EMPTY_PRODUCT_FEATURE_RELEASE_PAGE,
      { endpoint: "GET /api/features", includeReceived: false },
    );

    expect(parsed).toEqual(EMPTY_PRODUCT_FEATURE_RELEASE_PAGE);
  });

  it("falls back when detail omits required content", () => {
    const { usage_guide: _usageGuide, ...malformed } = rawRelease;
    const parsed = parseWithFallback(
      malformed,
      ProductFeatureReleaseSchema,
      EMPTY_PRODUCT_FEATURE_RELEASE,
      { endpoint: "GET /api/features/:id", includeReceived: false },
    );

    expect(parsed).toEqual(EMPTY_PRODUCT_FEATURE_RELEASE);
  });

  it("keeps a future enum value displayable with safe defaults", () => {
    const parsed = ProductFeatureReleaseSchema.parse({
      ...rawRelease,
      release_type: "redesign",
      image_requirement: "runtime_family",
      requires_image_upgrade: undefined,
    });

    expect(parsed.releaseType).toBe("improvement");
    expect(parsed.imageRequirement).toBe("none");
    expect(parsed.requiresImageUpgrade).toBe(false);
  });
});
