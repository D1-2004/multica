# ASB outbound network policy

ALIBABA-2026-45495120 requires sandbox creation to include a `networkPolicy`
with `defaultAction: deny`. This applies to outbound destinations outside
loopback, including intranet services.

Every ASB creation request now includes a deny policy. Task sandboxes and
enterprise identity seeds both receive explicit allowed destinations. The
control-plane client rejects allow-by-default, CIDRs and arbitrary wildcards.
Only the two code-reviewed regional transfer families `*.trans.dingtalk.com`
and `*.down.dingtalk.com` are allowed as built-in rules; user/deployment
configuration still accepts exact hosts only.

Default destinations combine:

- Exact DWS MCP, gateway, terminal, API and identity service hosts.
- DWS direct transfer, document OSS, mail attachment, Stream and distribution
  dependencies, audited in [the DWS network review](asb-dws-network-audit.md).
- BUC, AuthX, Idem, Aone, Code and sandbox service hosts.
- npm and Python package registries used by sandbox tools.
- Current deployment configuration: Multica callback/public/upload origins,
  LLM endpoint, agent identity endpoints and integration service URLs.
- HTTP/WebSocket hosts configured in bound Agents' MCP, custom environment
  and runtime configuration. Credentials and request headers are excluded.
- The current task's generated MCP overlay hosts (added only to its policy).

Operators may add deployment-wide exact domains/IPs in Diamond
`runtime.asb.network_allowlist` (`dt-fde-multica-runtime.json`), or
`MULTICA_ASB_NETWORK_ALLOWLIST` for environment-based deployments.
The Runtime details page shows the computed defaults and supports up to 256
additional exact domains/individual IPs. GET/PUT
`/api/runtimes/{runtimeId}/asb-network-policy` requires workspace owner/admin.
Only the custom array is editable; it cannot remove mandatory services or
change the default action. Runtime metadata stores the normalized custom list.
Metadata updates use the same database advisory lock as sandbox launches.

A SHA-256 policy fingerprint is stored in sandbox metadata. Before reusing a
sandbox, the launcher requires a matching fingerprint. Legacy instances with
no fingerprint and instances with changed dependencies are replaced through
the existing idle-sandbox cleanup path. Active tasks finish under their old
policy. Files stored only in the replaced sandbox are not preserved. Saved
allowlist changes take effect on the next task; they are not live patches.

Validation must confirm creation payload and live policy, an actual completed
ASB task, required service connectivity, a denied unlisted destination, and
custom allow/remove behavior. A successful deployment alone is insufficient.
Production rollout and fleet-wide retirement are separate from pre-release
validation; do not close the production finding after a pre-release deploy.
