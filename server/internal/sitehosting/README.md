# Static Site Hosting Contract

Static Sites are public-unlisted assets whose management authority is scoped to
an authenticated user inside an authenticated workspace. The public URL remains
opaque and does not expose either authority identifier.

## MCP deployment

`prepare_static_site_deploy` accepts archive metadata and an optional existing
Site ID. It never accepts a user or workspace identifier from tool arguments.
The server uses `X-User-ID` and `X-Workspace-ID` after authentication has stamped
them. Calls without both authorities are rejected.

An upload validates the ZIP before switching the active revision. During that
validation, the service parses the configured HTML entrypoint and stores a
normalized, 200-character maximum `<title>` in the revision manifest. The title
is metadata only and does not affect public serving.

## Management API

Listing, status lookup, update, and deletion all require both `owner_user_id`
and `workspace_id`. Human list and delete requests resolve the current workspace
from the server-validated workspace request context and verify membership.

Older Sites whose `workspace_id` is absent are excluded from every workspace
list. An authenticated owner who still has the Site ID can redeploy it; that
explicit update claims the legacy Site for the request's authenticated
workspace. Older active revisions without title metadata remain valid, and
clients fall back to the opaque public Site ID as their display label.

## History

- 2026-08-31: Restored workspace isolation for hosted Site management and added
  entrypoint HTML title metadata. The change prevents one user's Sites from
  being mixed across workspaces while replacing opaque IDs with useful labels.
