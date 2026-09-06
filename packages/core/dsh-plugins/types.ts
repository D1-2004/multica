/**
 * DSH plugins as a workspace asset.
 *
 * DeepSeek Harness has no plugin registry of its own: `dsh plugin add` forwards
 * to pnpm inside a profile directory, and a plugin is an npm package whose
 * manifest declares `dsh.bundle.patch`. So an imported plugin here is a pinned
 * package reference plus the loader rows that package declares.
 */

/** How a plugin's package is fetched. */
export type DshPluginSourceKind = "npm" | "github" | "url" | "file";

export interface DshPlugin {
  id: string;
  workspaceId: string;
  /** The npm package name — the identity DSH itself uses. */
  packageName: string;
  displayName: string;
  description: string;
  homepage: string;
  sourceKind: DshPluginSourceKind;
  /** Verbatim spec, e.g. `npm:dsh-mcp-lens@0.1.0-rc.9`. */
  sourceSpec: string;
  resolvedVersion: string;
  /** `sha256-<hex>`, or "" when the source is not pinned. */
  integrity: string;
  /**
   * Loader rows the package's own patch file inserts. A row id is chosen by the
   * plugin author and routinely differs from the package name.
   */
  bundleRows: string[];
  /** Which row `config` overrides. Empty when there is no configuration. */
  configRow: string;
  config: Record<string, unknown>;
  /** The community catalog this came from, or "" for a direct import. */
  catalog: string;
  validatedDshVersion: string;
  createdBy: string | null;
  createdAt: string;
  updatedAt: string;
}

/** An imported plugin as attached to one agent. */
export interface AgentDshPlugin extends DshPlugin {
  enabled: boolean;
}

/** One (agent, plugin) pair, for folding a "used by" column client-side. */
export interface DshPluginBinding {
  agentId: string;
  pluginId: string;
  enabled: boolean;
}

/** One row of the community index. */
export interface DshPluginCatalogEntry {
  name: string;
  owner: string;
  url: string;
  page: string;
  category: string;
  descriptionEn: string;
  descriptionZh: string;
  npmPackage: string;
  npmVersion: string;
  stars: number;
  downloads: number;
  addedOn: string;
  /** What to POST back to import this entry; "" when not installable. */
  sourceSpec: string;
}

/**
 * Where the browse data came from. `official` is always false — DeepSeek
 * publishes no catalog, so this is a community index and the UI says so.
 */
export interface DshPluginCatalogState {
  catalog: string;
  catalogVersion: string;
  entryCount: number;
  refreshedAt: string;
  sourcePackage: string;
  sourceRepo: string;
  sourceSite: string;
  license: string;
  official: boolean;
}

export interface DshPluginCatalogPage {
  entries: DshPluginCatalogEntry[];
  total: number;
  limit: number;
  offset: number;
  state: DshPluginCatalogState;
}

export interface DshPluginCatalogCategory {
  category: string;
  entryCount: number;
}

/** One hit from the npm registry's own search endpoint. */
export interface DshPluginRegistryResult {
  name: string;
  version: string;
  description: string;
  publisher: string;
  links: string;
}

export interface ImportDshPluginRequest {
  source: string;
  displayName?: string;
  configRow?: string;
  config?: Record<string, unknown>;
  catalog?: string;
  onConflict?: "fail" | "overwrite" | "skip";
}

export interface ImportDshPluginResult {
  status: string;
  plugin: DshPlugin | null;
  warnings: string[];
  existingPlugin: DshPlugin | null;
  error: string;
}
