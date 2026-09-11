import { z } from "zod";
import { parseWithFallback } from "../api/schema";

const PackageErrorSchema = z.object({
  code: z.string().optional(),
  issues: z.array(z.unknown()).optional(),
  validation: z.unknown().optional(),
  schema_url: z.string().optional(),
}).loose();

export function agentPackageErrorDetails(error: unknown): string {
  const message = error instanceof Error ? error.message : String(error);
  if (!(error instanceof Error) || !("body" in error) || !error.body) return message;
  const body = parseWithFallback<z.infer<typeof PackageErrorSchema> | null>(error.body, PackageErrorSchema, null, { endpoint: "Agent package error", includeReceived: false });
  if (!body) return message;
  const details = body.validation ?? body.issues;
  return details ? `${message}\n\n${JSON.stringify(details, null, 2)}` : message;
}
