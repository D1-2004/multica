import { useParams } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { productFeatureReleaseDetailOptions } from "@multica/core/product-features";
import { ProductFeatureDetailPage as SharedProductFeatureDetailPage } from "@multica/views/product-features";
import { useDocumentTitle } from "@/hooks/use-document-title";

export function ProductFeatureDetailPage() {
  const { id } = useParams<{ id: string }>();
  const { data: release } = useQuery(productFeatureReleaseDetailOptions(id ?? ""));

  useDocumentTitle(release?.title || "Feature update");

  if (!id) return null;
  return <SharedProductFeatureDetailPage releaseId={id} />;
}
