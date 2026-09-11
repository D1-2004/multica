"use client";

import { agentPackageErrorDetails } from "@multica/core/agents";
import { DownloadAgentSchema } from "./download-agent-schema";

export function PackageError({ error }: { error: unknown }) {
  if (!error) return null;
  return <div role="alert" className="space-y-3 rounded-md border border-destructive p-3">
    <pre className="max-h-96 overflow-auto whitespace-pre-wrap break-words text-caption text-destructive">{agentPackageErrorDetails(error)}</pre>
    <DownloadAgentSchema asLink />
  </div>;
}
