"use client";

import { use } from "react";
import { LabelUsagePage } from "@multica/views/labels";
import { ErrorBoundary } from "@multica/ui/components/common/error-boundary";

export default function LabelUsageRoute({
  params,
}: {
  params: Promise<{ id: string }>;
}) {
  const { id } = use(params);
  return (
    <ErrorBoundary resetKeys={[id]}>
      <LabelUsagePage labelId={id} />
    </ErrorBoundary>
  );
}
