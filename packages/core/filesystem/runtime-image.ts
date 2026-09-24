/** Runtime metadata capability. An image name is not a capability. */
export const SHARED_DISK_CAPABILITY = "workspace_shared_disk";

export type SharedDiskSupport = "capable" | "incapable" | "unknown";

export function sharedDiskSupport(
  runtime: { metadata?: Record<string, unknown> | null } | null | undefined,
): SharedDiskSupport {
  const metadata = runtime?.metadata;
  if (!metadata || typeof metadata !== "object") return "unknown";
  const capabilities = metadata.capabilities;
  if (!Array.isArray(capabilities)) return "incapable";
  return capabilities.some((item) => item === SHARED_DISK_CAPABILITY) ? "capable" : "incapable";
}

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
