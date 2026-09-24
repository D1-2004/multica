/**
 * Shared-disk agent work needs this FC/E2B image. Older PI images return 401
 * from the model request and exit before they can read the disk.
 */
export const SHARED_DISK_REQUIRED_RUNTIME_IMAGE =
  "multica-m7-va2eb67817f146ef4-r1-fdf8b8";

export type RuntimeImageMatch = "not_required" | "match" | "mismatch" | "unknown";

function clean(value: string | null | undefined): string {
  return typeof value === "string" ? value.trim() : "";
}

/**
 * Compare the image a capability requires with the image on the bound runtime.
 * An empty bound image is unknown, not a match. A bound image satisfies the
 * requirement when it equals the required image or ends with that image as a
 * "-" or "/" delimited token. A shorter suffix does not match.
 */
export function compareRuntimeImage(
  required: string | null | undefined,
  bound: string | null | undefined,
): RuntimeImageMatch {
  const need = clean(required);
  if (!need) return "not_required";
  const have = clean(bound);
  if (!have) return "unknown";
  if (have === need || have.endsWith(`-${need}`) || have.endsWith(`/${need}`)) return "match";
  return "mismatch";
}

export function boundRuntimeImage(
  runtime: { metadata?: Record<string, unknown> | null } | null | undefined,
): string | null {
  const metadata = runtime?.metadata;
  if (!metadata || typeof metadata !== "object") return null;
  for (const key of ["template_alias", "template", "template_id", "template_name"] as const) {
    const value = metadata[key];
    if (typeof value === "string" && value.trim()) return value.trim();
  }
  return null;
}
