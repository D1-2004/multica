-- DSH plugins: the workspace asset, its per-agent binding, and the cached
-- community catalog the browse surface reads.

-- name: ListDshPluginsByWorkspace :many
SELECT * FROM dsh_plugin
WHERE workspace_id = $1
ORDER BY package_name ASC;

-- name: GetDshPluginInWorkspace :one
SELECT * FROM dsh_plugin
WHERE id = $1 AND workspace_id = $2;

-- name: GetDshPluginByWorkspaceAndPackage :one
-- Find-or-report for import: dsh_plugin_workspace_idx makes
-- (workspace_id, package_name) unique, so a second import of the same package
-- is a conflict the caller resolves rather than a duplicate row.
SELECT * FROM dsh_plugin
WHERE workspace_id = $1 AND package_name = $2;

-- name: CreateDshPlugin :one
INSERT INTO dsh_plugin (
    workspace_id, package_name, display_name, description, homepage,
    source_kind, source_spec, resolved_version, integrity,
    bundle_rows, config_row, config, catalog, validated_dsh_version, created_by
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
RETURNING *;

-- name: UpdateDshPlugin :one
UPDATE dsh_plugin SET
    display_name = $3,
    description = $4,
    source_spec = $5,
    resolved_version = $6,
    integrity = $7,
    bundle_rows = $8,
    config_row = $9,
    config = $10,
    validated_dsh_version = $11,
    updated_at = now()
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: DeleteDshPluginInWorkspace :execrows
DELETE FROM dsh_plugin
WHERE id = $1 AND workspace_id = $2;

-- name: ListDshPluginsForAgent :many
-- The composed plugin set for one agent, in a stable order so the profile
-- manifest a task boots is reproducible.
SELECT p.*, b.enabled
FROM dsh_plugin p
JOIN agent_dsh_plugin b ON b.dsh_plugin_id = p.id
WHERE b.agent_id = $1 AND p.workspace_id = $2
ORDER BY p.package_name ASC;

-- name: ListDshPluginBindingsByWorkspace :many
-- Every (agent, plugin) pair in the workspace, for the "used by" column on the
-- list page. Folded client-side exactly like skill assignments are.
SELECT b.agent_id, b.dsh_plugin_id, b.enabled
FROM agent_dsh_plugin b
JOIN dsh_plugin p ON p.id = b.dsh_plugin_id
WHERE p.workspace_id = $1;

-- name: AddAgentDshPlugin :exec
INSERT INTO agent_dsh_plugin (agent_id, dsh_plugin_id, enabled)
VALUES ($1, $2, $3)
ON CONFLICT (agent_id, dsh_plugin_id) DO UPDATE SET enabled = EXCLUDED.enabled;

-- name: RemoveAgentDshPlugin :execrows
DELETE FROM agent_dsh_plugin
WHERE agent_id = $1 AND dsh_plugin_id = $2;

-- name: DeleteAgentDshPluginsByPlugin :exec
-- No cascading deletes in this fork, so removing a plugin must drop its
-- bindings explicitly, in the same transaction as the parent delete.
DELETE FROM agent_dsh_plugin WHERE dsh_plugin_id = $1;

-- name: DeleteAgentDshPluginsByAgent :exec
DELETE FROM agent_dsh_plugin WHERE agent_id = $1;

-- name: UpsertDshPluginCatalogEntry :exec
INSERT INTO dsh_plugin_catalog_entry (
    catalog, name, owner, url, page, category,
    description_en, description_zh, npm_package, npm_version, tarball_url,
    stars, downloads, install_hint, added_on, catalog_version, refreshed_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, now())
ON CONFLICT (catalog, owner, name) DO UPDATE SET
    url = EXCLUDED.url,
    page = EXCLUDED.page,
    category = EXCLUDED.category,
    description_en = EXCLUDED.description_en,
    description_zh = EXCLUDED.description_zh,
    npm_package = EXCLUDED.npm_package,
    npm_version = EXCLUDED.npm_version,
    tarball_url = EXCLUDED.tarball_url,
    stars = EXCLUDED.stars,
    downloads = EXCLUDED.downloads,
    install_hint = EXCLUDED.install_hint,
    added_on = EXCLUDED.added_on,
    catalog_version = EXCLUDED.catalog_version,
    refreshed_at = now();

-- name: DeleteStaleDshPluginCatalogEntries :execrows
-- A refresh replaces the catalog wholesale: rows the new published version no
-- longer carries are dropped by version, not by a client-supplied id list.
DELETE FROM dsh_plugin_catalog_entry
WHERE catalog = $1 AND catalog_version <> $2;

-- name: SearchDshPluginCatalog :many
SELECT * FROM dsh_plugin_catalog_entry
WHERE catalog = $1
  AND (sqlc.arg(category)::text = '' OR category = sqlc.arg(category)::text)
  AND (
    sqlc.arg(query)::text = ''
    OR name ILIKE '%' || sqlc.arg(query)::text || '%'
    OR npm_package ILIKE '%' || sqlc.arg(query)::text || '%'
    OR owner ILIKE '%' || sqlc.arg(query)::text || '%'
    OR description_en ILIKE '%' || sqlc.arg(query)::text || '%'
    OR description_zh ILIKE '%' || sqlc.arg(query)::text || '%'
  )
  AND (NOT sqlc.arg(installable_only)::bool OR npm_package <> '')
ORDER BY stars DESC, downloads DESC, name ASC
LIMIT sqlc.arg(row_limit)::int OFFSET sqlc.arg(row_offset)::int;

-- name: CountDshPluginCatalog :one
SELECT count(*) FROM dsh_plugin_catalog_entry
WHERE catalog = $1
  AND (sqlc.arg(category)::text = '' OR category = sqlc.arg(category)::text)
  AND (
    sqlc.arg(query)::text = ''
    OR name ILIKE '%' || sqlc.arg(query)::text || '%'
    OR npm_package ILIKE '%' || sqlc.arg(query)::text || '%'
    OR owner ILIKE '%' || sqlc.arg(query)::text || '%'
    OR description_en ILIKE '%' || sqlc.arg(query)::text || '%'
    OR description_zh ILIKE '%' || sqlc.arg(query)::text || '%'
  )
  AND (NOT sqlc.arg(installable_only)::bool OR npm_package <> '');

-- name: GetDshPluginCatalogEntry :one
SELECT * FROM dsh_plugin_catalog_entry
WHERE catalog = $1 AND owner = $2 AND name = $3;

-- name: GetDshPluginCatalogState :one
-- What the browse surface shows above the list: which published catalog
-- version is cached, how many entries it has, and when it was last refreshed.
SELECT
    count(*)::bigint AS entry_count,
    coalesce(max(catalog_version), '')::text AS catalog_version,
    max(refreshed_at)::timestamptz AS refreshed_at
FROM dsh_plugin_catalog_entry
WHERE catalog = $1;

-- name: ListDshPluginCatalogCategories :many
SELECT category, count(*)::bigint AS entry_count
FROM dsh_plugin_catalog_entry
WHERE catalog = $1 AND category <> ''
GROUP BY category
ORDER BY entry_count DESC, category ASC;
