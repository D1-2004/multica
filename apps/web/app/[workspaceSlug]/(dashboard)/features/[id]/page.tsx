"use client";

import { use } from "react";
import { ProductFeatureDetailPage } from "@multica/views/product-features";

export default function Page({ params }: { params: Promise<{ id: string }> }) {
  const { id } = use(params);
  return <ProductFeatureDetailPage releaseId={id} />;
}
