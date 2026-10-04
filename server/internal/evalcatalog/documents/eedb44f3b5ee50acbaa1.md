# Semantica MCP relay in Multica

The task-scoped `semantica_mcp_relay` tool is hosted by the existing `/api/mcp`
endpoint. The authenticated active task must belong to the configured Agent.
The server forwards directly to the configured Semantica HTTPS endpoint, using
an upstream-only Bearer credential. No task token, caller headers or target URL
crosses this boundary. This replaces the separate FaaS hop for this capability.

## References and boundaries

The implementation ports task authorization from `feat/faas-mcp-relay` and
JSON/SSE normalization from `dt-fde-semantica-mcp-relay/src/relay.ts`.
It keeps the existing Multica managed-MCP transport, so cloud sandboxes require
no new networking path. The scope is one deployment-configured connector.
The former FaaS connector CRUD screen cannot configure this relay and must not
be presented as its management interface.
No database migration or new Redis account is required.

The existing Multica `storeRedis` client performs atomic INCR + PEXPIRE for
per-task (30/minute), Agent (120/minute), and workspace (600/minute) limits.
Keys use `mcpconn:<environment>:semantica:rate:` with a 120-second TTL. Missing
or unavailable Tair fails closed. There is no inter-service HMAC/nonce layer:
task authentication and upstream forwarding now execute inside one process.

Only `search_knowledge`, `query_knowledge_cypher`,
`get_knowledge_graph_schema`, and `get_knowledge_node_schema` can be called.
`method=tools/list` returns their current upstream schemas; `method=tools/call`
requires `tool_name` and object `arguments`. The upstream enforces read-only
query semantics. Responses are limited to 2 MiB and calls to 45 seconds; redirects
are rejected. After task authorization and the shared rate checks, the handler
flushes the JSON response headers before waiting on Semantica. This keeps the
existing public sandbox relay's 30-second response-header deadline from
prematurely cancelling the 45-second call; the full JSON result still arrives
only when the upstream completes. No public/production relay change is needed.
SSE notifications are skipped until the matching JSON-RPC result.
Audit events contain task/Agent/workspace/tool/outcome/duration, never contents
or credentials.

## Configuration

Diamond `dt-fde-multica-runtime.json` / `DEFAULT_GROUP`:
`features.semantica_mcp_relay` is read on every discovery and call. An omitted
value defaults on only when the authoritative environment is explicitly
`pre`, `prepub`, `pre_publish`, or `staging`; production/unknown default off.
Explicit false disables immediately for subsequent calls (in-flight calls may
finish). Invalid Diamond updates retain the last validated snapshot, following
the existing runtime-config contract.

Aone managed environment secrets/config (whitelisted by `src/main.sh`):

- `MULTICA_SEMANTICA_MCP_URL`: fixed HTTPS endpoint.
- `MULTICA_SEMANTICA_MCP_BEARER_TOKEN`: upstream-only secret; never in Diamond.
- `MULTICA_SEMANTICA_MCP_TARGET_AGENT_ID`: authorized Agent UUID.

Incomplete settings leave this tool unavailable without breaking server startup.
The environment comes from AONE_ENV_TYPE, ENV_TYPE, GO_ENV, APP_ENV in that
order; unknown values use an isolated `unknown` key namespace and default off.

## Legacy compatibility

The `semantica_mcp_relay` tool and its existing env keys remain available for
active pre-release tasks during migration. The workspace-status endpoint stays
behind workspace and Agent access gates, exposes no upstream URL or secret,
and supplies a temporary preset card in the generic connector UI. New
connectors use the separate native MCP route described in
[internal-mcp-connectors.md](/evals?tab=tools&doc=docs%2Finternal-mcp-connectors.md). The legacy tool is not
evidence that the generic route has passed end-to-end validation.

## Acceptance

Deploy only through pipeline 66, using this CR and the latest develop baseline.
The previous FaaS connector CR and other old pending CRs may be removed from
the pre-release release set as requested on 2026-09-27; take care not to drop
changes already on develop. Do not merge or advance production/manual
validation. Send a real message as 冬翔 to 东翔测试号
in pre, requesting schema then an MCP search. Correlate the DingTalk receipt,
Multica task, `semantica_mcp_relay_call` audit event and returned schema/search
summary. Unit tests and direct probes are supporting evidence, not acceptance.

Before rolling back to a binary that predates this feature, remove the new
Diamond field first: the existing strict config parser rejects unknown fields
at startup. During forward rollout, omit the field until every replica runs
the new binary, then publish the explicit pre-release value.
