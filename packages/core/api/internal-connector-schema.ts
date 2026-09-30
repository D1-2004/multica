import { z } from "zod";

/** Authentication modes the UI knows. A newer backend may add more; those
 * parse as "unknown" so the connector still lists instead of emptying the
 * whole page. */
export type InternalConnectorAuthMode = "none" | "bearer" | "oauth" | "unknown";

export type InternalConnectorCredentialSource =
  | "none"
  | "environment"
  | "workspace"
  | "unavailable"
  | "unknown";

export function internalConnectorAuthModeOf(value: unknown): InternalConnectorAuthMode {
  if (value === "none" || value === "bearer" || value === "oauth") return value;
  return "unknown";
}

// Older backends omit auth_mode; every connector they return is a Bearer
// connector (the only mode that existed).
const authMode = z
  .string()
  .nullish()
  .transform((value): InternalConnectorAuthMode =>
    value == null || value === "" ? "bearer" : internalConnectorAuthModeOf(value),
  );

const credentialSource = z
  .string()
  .nullish()
  .transform((value): InternalConnectorCredentialSource => {
    if (value == null || value === "") return "none";
    if (value === "none" || value === "environment" || value === "workspace" || value === "unavailable") {
      return value;
    }
    return "unknown";
  });

const text = z
  .string()
  .nullish()
  .transform((value) => value ?? "");

const stringList = z
  .array(z.string())
  .nullish()
  .transform((value) => value ?? []);

const count = z
  .number()
  .int()
  .nonnegative()
  .nullish()
  .catch(0)
  .transform((value) => value ?? 0);

const strictTrue = z.unknown().transform((value) => value === true);

export const InternalConnectorSchema = z.object({
  id: z.string().uuid(),
  workspace_id: z.string().uuid(),
  name: z.string(),
  upstream_url: z.string().url(),
  credential_ref: z.string(),
  credential_ready: z.boolean(),
  auth_mode: authMode,
  credential_source: credentialSource,
  credential_optional: z.boolean().optional().default(false),
  allowed_tools: stringList,
  agent_ids: stringList,
  enabled: z.boolean(),
  // Official app catalog (connectorcatalog). "" for custom connectors.
  catalog_slug: text,
  write_enabled: strictTrue,
  discovered_tool_count: count,
  // Display hint of the workspace credential ("@octocat", "OAuth",
  // "••••abcd"), never token material.
  credential_account: text,
}).transform((v) => ({
  id: v.id,
  workspaceId: v.workspace_id,
  name: v.name,
  upstreamUrl: v.upstream_url,
  credentialRef: v.credential_ref,
  credentialReady: v.credential_ready,
  authMode: v.auth_mode,
  credentialSource: v.credential_source,
  credentialOptional: v.credential_optional === true,
  allowedTools: v.allowed_tools,
  agentIds: v.agent_ids,
  enabled: v.enabled,
  catalogSlug: v.catalog_slug,
  writeEnabled: v.write_enabled,
  discoveredToolCount: v.discovered_tool_count,
  credentialAccount: v.credential_account,
}));

export const InternalConnectorListSchema = z.array(InternalConnectorSchema);
export type InternalConnector = z.infer<typeof InternalConnectorSchema>;

export const AvailableInternalConnectorSchema = z.object({
  id: z.string().uuid(),
  name: z.string(),
  server_name: z.string().optional(),
  agent_id: z.string().uuid(),
  agent_name: z.string(),
  tools: z.array(z.string()),
}).transform((v) => ({
  id: v.id,
  name: v.name,
  serverName: v.server_name ?? `internal-${v.id}`,
  agentId: v.agent_id,
  agentName: v.agent_name,
  tools: v.tools,
}));
export const AvailableInternalConnectorListSchema = z.array(AvailableInternalConnectorSchema);
export type AvailableInternalConnector = z.infer<typeof AvailableInternalConnectorSchema>;

export const SavedInternalConnectorSchema = z.object({
  id: z.string().uuid(),
  credential_ref: z.string(),
});

export type InternalConnectorInput = {
  name: string;
  upstream_url: string;
  allowed_tools: string[];
  agent_ids: string[];
  enabled: boolean;
  auth_mode?: "none" | "bearer" | "oauth" | "";
  bearer_token?: string;
  auto_discover?: boolean;
  /** Catalog connectors only: expose write tools, not just readOnlyHint ones. */
  write_enabled?: boolean;
};

/**
 * PATCH body that keeps every stored field of `connector` except `patch`.
 * The update endpoint replaces name, tools, grants and state wholesale, so
 * every write starts from the full current connector. Catalog connectors
 * always carry `write_enabled` so a save never silently drops write access;
 * custom connectors never send it. An unknown auth mode is left out ("") so
 * the server keeps the stored one.
 */
export function internalConnectorUpdateInput(
  connector: InternalConnector,
  patch: Partial<Pick<InternalConnectorInput, "name" | "agent_ids" | "enabled" | "write_enabled">> = {},
): InternalConnectorInput {
  const input: InternalConnectorInput = {
    name: patch.name ?? connector.name,
    upstream_url: connector.upstreamUrl,
    allowed_tools: connector.allowedTools,
    agent_ids: patch.agent_ids ?? connector.agentIds,
    enabled: patch.enabled ?? connector.enabled,
    auth_mode: connector.authMode === "unknown" ? "" : connector.authMode,
  };
  if (connector.catalogSlug) {
    input.write_enabled = patch.write_enabled ?? connector.writeEnabled;
  }
  return input;
}

export const InternalConnectorTestSchema = z.object({
  reachable: z.boolean(),
  ready: z.boolean().optional().default(false),
  message: z.string().optional(),
  tools: z.array(z.string()).optional(),
  missing_tools: z.array(z.string()).optional().default([]),
  has_more: z.boolean().optional(),
  duration_ms: z.number().optional(),
});
export type InternalConnectorTest = z.infer<typeof InternalConnectorTestSchema>;

// ---------------------------------------------------------------------------
// Official app catalog (official remote MCP servers + OAuth)
// ---------------------------------------------------------------------------

/** How a catalog app signs users in. Unknown kinds from a newer backend keep
 * the app listed; the UI then offers no OAuth-specific affordance. */
export type ConnectorCatalogAuthKind = "oauth_dcr" | "oauth_github_app" | "unknown";

export interface ConnectorCatalogApp {
  slug: string;
  name: string;
  mcpUrl: string;
  authKind: ConnectorCatalogAuthKind;
  /** The app also accepts a Personal Access Token (GitHub). */
  allowsPat: boolean;
  /** The server can run the OAuth flow for this app (client configured). */
  oauthAvailable: boolean;
  /** The workspace connector created from this app, or null when not added. */
  connectorId: string | null;
  /** Optional provider page where users grant the app access to their
   * resources (GitHub App installation). "" when the server sends none. */
  installUrl: string;
}

/** Only absolute https URLs (http on loopback for local development) may be
 * handed to window navigation; anything else is dropped. */
export function safeExternalUrl(value: unknown): string {
  if (typeof value !== "string" || !value) return "";
  let url: URL;
  try {
    url = new URL(value);
  } catch {
    return "";
  }
  if (url.protocol === "https:") return url.toString();
  if (url.protocol === "http:" && (url.hostname === "localhost" || url.hostname === "127.0.0.1")) {
    return url.toString();
  }
  return "";
}

const slug = z.string().regex(/^[a-z0-9][a-z0-9_-]{0,63}$/);

const CatalogAppWireSchema = z
  .object({
    slug,
    name: text,
    mcp_url: text,
    auth_kind: text,
    allows_pat: strictTrue,
    oauth_available: strictTrue,
    connector_id: z.string().uuid().nullish().catch(null),
    install_url: z.unknown().optional(),
  })
  .transform(
    (value): ConnectorCatalogApp => ({
      slug: value.slug,
      name: value.name || value.slug,
      mcpUrl: value.mcp_url,
      authKind:
        value.auth_kind === "oauth_dcr" || value.auth_kind === "oauth_github_app"
          ? value.auth_kind
          : "unknown",
      allowsPat: value.allows_pat,
      oauthAvailable: value.oauth_available,
      connectorId: value.connector_id ?? null,
      installUrl: safeExternalUrl(value.install_url),
    }),
  );

/** Drops malformed items instead of failing the whole list, so one bad app
 * from a newer backend never empties the gallery. */
function tolerantList<T extends z.ZodType>(item: T) {
  return z
    .array(z.unknown())
    .nullish()
    .transform((values): z.output<T>[] =>
      (values ?? []).flatMap((value) => {
        const parsed = item.safeParse(value);
        return parsed.success ? [parsed.data as z.output<T>] : [];
      }),
    );
}

export const ConnectorCatalogSchema = z
  .object({ apps: tolerantList(CatalogAppWireSchema) })
  .transform((value) => value.apps);

/** `POST /connector-catalog/{slug}` echo. null when the connector JSON is
 * malformed; the caller refetches the list either way. */
export const AddedCatalogConnectorSchema = z
  .object({ connector: InternalConnectorSchema })
  .transform((value) => value.connector);

/** `{authorize_url}` from an OAuth start endpoint (admin or mobile). The URL
 * is navigated to, so it must be an absolute https URL. */
export const ConnectorAuthorizeUrlSchema = z
  .object({ authorize_url: z.string() })
  .transform((value) => safeExternalUrl(value.authorize_url))
  .pipe(z.string().min(1));

export interface InternalConnectorToolsRefresh {
  /** Tools the upstream server listed. */
  discovered: number;
  /** Tools now pinned for agents (read-only unless write is enabled). */
  allowedTools: string[];
}

export const InternalConnectorToolsRefreshSchema = z
  .object({
    discovered: z.number().int().nonnegative(),
    allowed_tools: stringList,
  })
  .transform(
    (value): InternalConnectorToolsRefresh => ({
      discovered: value.discovered,
      allowedTools: value.allowed_tools,
    }),
  );
