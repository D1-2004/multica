import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

export const productFeatureReleaseKeys = {
  all: ["product-feature-releases"] as const,
  list: (query: string) =>
    [...productFeatureReleaseKeys.all, "list", query] as const,
  detail: (id: string) =>
    [...productFeatureReleaseKeys.all, "detail", id] as const,
};

export function productFeatureReleaseListOptions(query: string) {
  return queryOptions({
    queryKey: productFeatureReleaseKeys.list(query),
    queryFn: ({ signal }) =>
      api.listProductFeatureReleases({ query, signal }),
  });
}

export function productFeatureReleaseDetailOptions(id: string) {
  return queryOptions({
    queryKey: productFeatureReleaseKeys.detail(id),
    queryFn: () => api.getProductFeatureRelease(id),
    enabled: !!id,
  });
}
