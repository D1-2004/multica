export type ProductFeatureReleaseType = "new" | "improvement";

export type ProductFeatureImageRequirement =
  | "none"
  | "latest_at_publish"
  | "min_version";

export interface ProductFeatureReleaseSummary {
  id: string;
  title: string;
  versionLabel: string;
  publishedAt: string;
}

export interface ProductFeatureRelease {
  id: string;
  featureId: string;
  featureSlug: string;
  releaseType: ProductFeatureReleaseType;
  title: string;
  description: string;
  useCases: string;
  usageGuide: string;
  versionLabel: string;
  imageRequirement: ProductFeatureImageRequirement;
  requiredImageVersion: string | null;
  requiresImageUpgrade: boolean;
  previousReleaseId: string | null;
  previousRelease: ProductFeatureReleaseSummary | null;
  publishedAt: string;
  createdAt: string;
}

export interface ProductFeatureReleasePage {
  releases: ProductFeatureRelease[];
  total: number;
  limit: number;
  offset: number;
}

export interface ListProductFeatureReleasesParams {
  query?: string;
  limit?: number;
  offset?: number;
  signal?: AbortSignal;
}
