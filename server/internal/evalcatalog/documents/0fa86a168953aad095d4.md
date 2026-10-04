# DingTalk message triggered automations

## Contract

An automation assigned to an active Agent with an active DingTalk account binding can add a `dingtalk_message` trigger. Each trigger has a merge interval of 1–1440 minutes (default 5). The first eligible new message starts a fixed collection window; later messages do not extend it. An empty period creates no run. Each automation owns its own window and receives counts grouped by conversation, with conversation title/type/ID, interval start/end, first/last message time, total messages and mentions. Message bodies are not included. The UI previews this contract. Only the bound account's currently subscribed conversations qualify; the account's own outgoing messages are excluded to avoid loops.

The proactive conversation feature remains independent and unchanged. Existing hourly summaries must no longer create issues or tasks.

## Ownership and durability

Router records metadata only after its existing source, subscription, binding and inbound deduplication checks. A new SchedulerX processor relays these observations through the existing durable dispatch queue and authenticated per-Agent endpoint. It does not create an automation run. Multica admits observations only to active triggers belonging to the receiving bound Agent and workspace, deduplicates them per trigger, and owns collection deadlines in PostgreSQL. Its worker freezes due windows and invokes the existing automation execution path with a stable occurrence key. Both `create_issue` and `run_only` receive the statistics. Retries reuse the occurrence; replicas coordinate with database locks. Disabling/deleting a trigger or changing the assignee invalidates unprocessed windows. Binding validity is checked again before dispatch.

No old hourly bucket is replayed into a new automation. Router metadata transport and Multica collection can coexist with proactive delivery. SchedulerX configuration is changed only for the new processor after pre-release code and migrations are deployed; existing job configuration is preserved.

## Acceptance

- Create/edit UI eligibility, interval validation, persisted trigger, and payload preview.
- API rejects unbound/non-Agent/cross-workspace assignees and invalid intervals; pause/delete/reassign stop pending work.
- Real pre-release messages from two conversations merge into one run with exact counts; duplicates do not inflate counts.
- No new messages yield no additional run; a later window yields another run.
- Both execution modes receive the statistics, with durable issue/task and user-visible result evidence.
- Proactive behavior remains available independently; legacy hourly dispatch cannot create work.
- Migrations, replica concurrency, retry recovery, payload validation, source binding and interval boundaries have targeted tests.
