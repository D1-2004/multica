"use client";

import { Suspense } from "react";
import { useSearchParams } from "next/navigation";
import { RunnerAuthorizePage } from "@multica/views/runner";

function RunnerAuthorizePageContent() {
  const searchParams = useSearchParams();
  return <RunnerAuthorizePage code={searchParams.get("code")} />;
}

export default function Page() {
  return (
    <Suspense fallback={null}>
      <RunnerAuthorizePageContent />
    </Suspense>
  );
}
