# working-on-issues source map

Task cancellation and steer: `docs/task-steer.md`;
`server/internal/service/task_steer.go` (`createSteeredExternalFollowUp`,
`AcknowledgeTaskProcessStopped`); `server/internal/handler/daemon.go`
(`CancelTask`, `AckTaskCancelled`); `server/pkg/db/queries/agent.sql`
(`CancelAgentTask`, both claim queries); `server/internal/service/fc_e2b_task_stop.go`
(`stopAbortedTaskProcesses`). Logical cancellation is not process-exit evidence.

Evidence layer for `SKILL.md`. Every contract the skill states is traced to a
current `file:line` here. Lines were re-derived against `feat/builtin-skills`
after the latest `main` merge; the prior skill cited pre-merge lines that have
since moved (see the "drifted" column). Re-confirm with the verification command
at the bottom before relying on an exact line.

## `multica issue pull-requests` — read PR links from Multica

| Behavior | File:line | Drifted from |
|---|---|---|
| CLI command `pull-requests <id>` (alias `prs`) | `server/cmd/multica/cmd_issue.go:183` | `:104` |
| `runIssuePullRequests` handler | `server/cmd/multica/cmd_issue.go:744` | new citation |
| Calls `GET /api/issues/<id>/pull-requests` | `server/cmd/multica/cmd_issue.go:759` | `:522` (unchanged) |
| API route registration | `server/cmd/server/router.go:480` | `:480` (unchanged) |
| Handler `ListPullRequestsForIssue` → `Queries.ListPullRequestsByIssue` | `server/internal/handler/github.go:687,692` | `:466` |
| Row → response mapper `issuePullRequestRowToResponse` | `server/internal/handler/github.go:205` | `:149` |

The CLI resolves the issue ref, GETs the endpoint, and (for `--output json`)
prints the raw `{"pull_requests": [...]}` body. Only `--output` is accepted; the
default `table` shows `NUMBER STATE TITLE URL`.

## PR response shape

`GitHubPullRequestResponse` struct: `server/internal/handler/github.go:58`. JSON
fields the agent can read off each element of `pull_requests`:

- `provider` (`json:"provider"`, line 63)
- `number` (`json:"number"`, line 67)
- `html_url` (`json:"html_url"`, line 70)
- `title` (`json:"title"`, line 68)
- `state` (`json:"state"`, line 69) — the folded lifecycle enum (see below)
- `merged_at` (`json:"merged_at"`, line 74), `closed_at` (line 75)
- `mergeable_state` (`json:"mergeable_state"`, line 80) — mirrors GitHub; UI only
  surfaces `clean`/`dirty`, other values round-trip as unknown
- `snapshot_available` (`json:"snapshot_available"`, line 100) — for GitHub,
  true only when the App snapshot feature is enabled and the snapshot head
  matches the current PR head (`currentGitHubSnapshotAvailable`, lines 258-265)
- `mergeable` / `merge_state_status` (lines 90, 94) — conflict-only verdict vs
  the complete merge gate; "ready" requires `merge_state_status == "clean"`
- `checks_rollup` (`json:"checks_rollup"`, line 105) and run-level
  `checks_total` / `checks_passed` / `checks_failed` / `checks_running`
  (lines 111-114), plus `failed_check_names` (line 118)
- `checks_conclusion` (`json:"checks_conclusion"`, line 108) — coarse
  `"passed"`/`"failed"`/`"pending"` or `null`; GitHub derives it only from an
  available current-head snapshot (mapper lines 242-254), while self-hosted VCS
  providers use `aggregateChecksConclusion` (line 275)

There is **no** standalone `draft` or `merged` boolean in the response. The
PR lifecycle is encoded in the single `state` string by `derivePRState`
(`server/internal/handler/github.go:1317`):

```
merged   → if PullRequest.Merged
closed   → else if PullRequest.State == "closed"
draft    → else if PullRequest.Draft
open     → otherwise
```

`derivePRState` is called when the webhook upserts the row
(`server/internal/handler/github.go:1115`), so `state` is what the list endpoint
returns. "Is it merged?" = `state == "merged"` (or `merged_at != null`); "is it a
draft?" = `state == "draft"`. Combine with `checks_conclusion` for CI status.

## Two distinct webhook paths: link vs close-intent

Both run inside the `pull_request` webhook handler, gated by the workspace
auto-link flag (`workspaceAutoLinkPRsEnabled`, `github.go:1074`).

### Path 1 — link (title OR body OR branch)

- `extractIdentifiers` regex helper: `server/internal/handler/github.go:1028`
- driving regex `identifierRe` (`\b([a-z][a-z0-9]{1,9})-(\d+)\b`, case-insensitive):
  `server/internal/handler/github.go:490`
- call site: `server/internal/handler/github.go:727` —
  `extractIdentifiers(p.PullRequest.Title, p.PullRequest.Body, p.PullRequest.Head.Ref)`

Every `PREFIX-NUMBER` mention in **title, body, or branch** resolves to an issue
in the workspace and writes a link row (`LinkIssueToPullRequest`, ~`github.go:762`).
This is what `multica issue pull-requests` later reads back.

**Reference-only flag (MUL-3739).** The link row carries a `reference_only`
boolean (`migrations/127_issue_pull_request_reference_only.up.sql`). The handler
computes a `qualifyingIdents` set = identifiers in **title or branch** (any
`extractIdentifiers` match) ∪ **body closing keywords** (`closingIdents`). A
linked identifier NOT in that set was matched only by a bare body mention, so its
row is written with `reference_only = true`. Both `ListPullRequestsByIssue` and
`GetIssuePullRequestCloseAggregate` filter `AND NOT reference_only`, so
reference-only links are hidden from the CLI / UI PR list **and** excluded from
the auto-advance gate (an open body-only mention must not silently block the
issue from reaching `done` while invisible in the list). The row still exists for
edit-time close-intent tracking. `reference_only` follows the same
`preserve_close_intent` terminal gate as `close_intent`.

Drifted from the prior skill's `github.go:727` citation, which pointed at the old
call-site location for the link logic.

### Path 2 — close intent (title OR body only, keyword-adjacent)

- `extractClosingIdentifiers` regex helper: `server/internal/handler/github.go:1051`
- driving regex `closingIdentifierRe`
  (`\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?)[:\s]+([a-z][a-z0-9]{1,9})-(\d+)\b`):
  `server/internal/handler/github.go:501`
- call site: `server/internal/handler/github.go:736` —
  `extractClosingIdentifiers(p.PullRequest.Title, p.PullRequest.Body)` (no branch arg)

Only a `PREFIX-NUMBER` immediately after a closing keyword
(`Closes`/`Fixes`/`Resolves`, optional `:` then whitespace) sets the link row's
`close_intent` flag — the gate that auto-advances the issue to `done` on merge.
`Fix MUL-1` closes; `Fix login MUL-1` does not (adjacency). Branch names are
deliberately excluded (function doc, `github.go:1044-1050`): a branch like
`mul-1/fix-login` links but must never declare close intent.

Drifted from the prior skill's `github.go:736` citation.

Net: a bare title prefix (`MUL-2759: ...`) or a branch ref links only (shown in
the PR list); `Closes MUL-2759` links **and** records close intent; a bare body
mention with no title/branch ref and no closing keyword links as `reference_only`
and is hidden from the PR list.

## Status side effects (enqueue contracts)

| Behavior | File:line | Drifted from |
|---|---|---|
| Create-time: agent-assigned, non-backlog issue enqueues immediately | `server/internal/handler/issue.go:2263-2264` | new citation |
| `shouldEnqueueAgentTask` returns false for `backlog` (parking lot) | `server/internal/handler/issue.go:2644-2648` | new citation |
| Backlog → non-backlog (not done/cancelled) enqueues on update | `server/internal/handler/issue.go:2537-2540` | `:2523` |
| Same contract in batch update | `server/internal/handler/issue.go:3021-3024` | new citation |
| Child → `done` notifies + wakes the parent, gated by the stage barrier | `server/internal/handler/issue_child_done.go:68` (`notifyParentOfChildDone`; doc comment at `:16`; barrier gate at `:124`) | `:66` |
| Status change (incl. → `cancelled`) does NOT cancel in-flight tasks; only issue deletion does (MUL-4465) | no-cancel note in `server/internal/handler/issue.go:2652-2658` (`UpdateIssue`) and `:3170-3171` (`BatchUpdateIssues`); deletion still cancels at `:2863` (`DeleteIssue`) / `:3239` (`BatchDeleteIssues`) via `CancelTasksForIssue` (`server/internal/service/task.go:1229`) | new citation |
| `--no-start` suppresses the run a status/assignee change would enqueue | CLI flag on `issueUpdateCmd` / `issueStatusCmd` / `issueAssignCmd` in `server/cmd/multica/cmd_issue.go`, sending `suppress_run`; honored by `UpdateIssueRequest.SuppressRun` in `server/internal/handler/issue.go` (single update and batch paths) | new citation |
| `StartTask` / `CompleteTask` do not write issue status (agent CLI owns progress) | `server/internal/service/task.go` (`StartTask` / `CompleteTask` comments) | new citation |
| Assignment brief: ordinary agent `in_progress` then `in_review`; squad leader `in_progress` only on first dispatch | `server/internal/daemon/execenv/runtime_config_sections.go` (`writeWorkflowAssignment`) | new citation |
| Failed task may roll `in_progress` → `todo` when no active task remains | `server/internal/service/task.go` (`HandleFailedTasks`) | new citation |

Creation with `--status todo` (or any non-backlog status) on an agent-assigned
issue fires the agent immediately; `--status backlog` parks it with the assignee
set but no trigger. Promoting `backlog → todo` later fires it then (update path,
line 2537).

Moving an issue to `cancelled` used to call `CancelTasksForIssue` and stop every
active task on it (the old #940 behavior). MUL-4465 removed that from both
`UpdateIssue` and `BatchUpdateIssues`: a status flip — `cancelled` included —
never cancels tasks now. `CancelTasksForIssue` fires only from the issue-deletion
paths (`DeleteIssue` / `BatchDeleteIssues`), where the owning issue row is going
away, so no task is left orphaned.

## Sub-issue stages (barrier wake)

| Behavior | File:line |
|---|---|
| `issue.stage` column (nullable, `>= 1`) | `server/migrations/123_issue_stage.up.sql` |
| Stage barrier: notify+wake fire only when the lowest unfinished stage is all-terminal; unstaged set = one implicit stage | `server/internal/handler/issue_child_done.go:370` (`stageBarrierClosed`) |
| **Terminal = `done` or `cancelled` only** — `in_review` does NOT close a stage | `server/internal/handler/issue_child_done.go:340` (`isTerminalChildStatus`) |
| Per-stage summary + next stage for the wake comment | `server/internal/handler/issue_child_done.go:402` (`stageProgressSummary`) |
| Trailing "promote the next stage / decide whether to wrap up" instruction | `server/internal/handler/issue_child_done.go:452` (`stageAdvanceInstruction`) |
| `--stage` on `issue create` / `issue update` | `server/cmd/multica/cmd_issue.go:481,504` |
| `multica issue children <id>` (sub-issues grouped by stage) | `server/cmd/multica/cmd_issue.go:191,877`; route `GET /api/issues/{id}/children` → `ListChildIssues` |
| CLI barrier projection: `barrier_closed` / `waiting_on` per stage, top-level `next_stage`, `unstaged_barrier_closed` | `server/cmd/multica/cmd_issue.go:1010` (`groupChildStages`), `:1069` (`unstagedBarrierState`), `:1086` (`childBarrierNotice`, the table one-liner) |
| CLI copy of the terminal rule (must change with the handler's) | `server/cmd/multica/cmd_issue.go:965` (`isTerminalChildStatus`) |
| Injected brief's one-line stage semantics (every agent reads this) | `server/internal/daemon/execenv/runtime_config_sections.go:711` (`writeSubIssueCreation`) |

Advancement is agent-driven: the server only detects the closed barrier and
wakes the parent assignee. Promoting the next stage's `backlog` sub-issues to
`todo` is the woken agent's decision, not a server side effect. When the woken
assignee (often a squad leader) decides the parent is complete, the system
comment explicitly asks for `multica issue status <parent-id> in_review` —
comment-triggered runs otherwise must not change status unless asked.

The terminal set is the sharp edge here, and it is mirrored in four places that
must stay in sync: `isTerminalChildStatus`
(`server/internal/handler/issue_child_done.go:340`), the CLI copy
(`server/cmd/multica/cmd_issue.go:965`), `ChildIssueProgress`'s
`status IN ('done', 'cancelled')` filter
(`server/pkg/db/queries/issue.sql:411`), and the sub-issue progress bar in
`packages/views/issues/components/issue-detail.tsx:652`.
`in_review` is not in it, while the assignment brief asks ordinary
agents to finish there — so an agent-assigned child that "finishes" normally
holds its stage open, with no timeout and no reconciler behind it. That is why
both the brief (`writeSubIssueCreation`) and `multica issue children --output
json` now state the rule and the barrier state explicitly.

## Metadata CLI

| Behavior | File:line |
|---|---|
| `multica issue metadata set <issue-id> --key --value [--type]` | `server/cmd/multica/cmd_issue_metadata.go:80,109-111` |
| `multica issue metadata delete <issue-id> --key` | `server/cmd/multica/cmd_issue_metadata.go:93,113` |
| API routes (PUT/DELETE `/metadata/{key}`) | `server/cmd/server/router.go:478-479` |

`--value` is JSON-parsed by default (bool/number sniff); `--type` forces
`string`/`number`/`bool`.

## Label usage CLI and task attribution

| Behavior | File:line |
|---|---|
| `multica label usage <label-ref>` flags and read-only handler | `server/cmd/multica/cmd_label_usage.go:15,34` |
| `--all` follows task pages and stamps `pagination.complete` | `server/cmd/multica/cmd_label_usage.go:92-122` |
| HTTP label-usage task DTO | `server/internal/handler/label_usage.go:54` |
| Executor and accountable-human hydration | `server/internal/handler/label_usage.go:293-339` |
| SQL task grain, executor, usage, and attribution columns | `server/pkg/db/queries/issue_label.sql:187` (`ListIssueLabelUsageTasks`) |

The CLI preserves the complete API response. Each task has `agent_id`,
`agent_name`, and the same `TaskAttribution` shape as the Issue execution log;
the summary retains the authoritative-cost vs uncosted-token split.

## Custom properties CLI

| Behavior | File:line |
|---|---|
| `multica property list/get/create/update/archive/unarchive` | `server/cmd/multica/cmd_property.go` |
| `multica issue property list/set/unset` (name→id translation) | `server/cmd/multica/cmd_property.go` (`encodeIssuePropertyValue`) |
| Definition CRUD, admin gate, agent-actor rejection | `server/internal/handler/property.go` (`requirePropertyAdmin`) |
| Optional catalog icon field and allowlist validation | `server/internal/handler/property.go` (`PropertyResponse`, `validatePropertyIcon`) |
| Per-type value validation (self-correcting errors) | `server/internal/handler/property.go` (`validatePropertyValue`) |
| API routes (`/api/properties`, PUT/DELETE `/api/issues/{id}/properties/{propertyId}`) | `server/cmd/server/router.go` |

## Verification command

Re-derive any line above before depending on it:

```bash
cd server
grep -n 'pull-requests <id>'                 cmd/multica/cmd_issue.go
grep -n 'ListPullRequestsForIssue'           cmd/server/router.go internal/handler/github.go
grep -n 'func issuePullRequestRowToResponse\|type GitHubPullRequestResponse struct\|func derivePRState\|func extractIdentifiers\|func extractClosingIdentifiers\|closingIdentifierRe' internal/handler/github.go
grep -n 'extractIdentifiers(\|extractClosingIdentifiers(\|derivePRState(' internal/handler/github.go
grep -n 'qualifyingIdents\|reference_only\|ReferenceOnly' internal/handler/github.go pkg/db/queries/github.sql
grep -n 'prevIssue.Status == "backlog"\|func (h \*Handler) shouldEnqueueAgentTask' internal/handler/issue.go
grep -n 'func notifyParentOfChildDone'       internal/handler/issue_child_done.go
grep -n 'func isTerminalChildStatus\|func stageBarrierClosed\|func stageProgressSummary\|func stageAdvanceInstruction' internal/handler/issue_child_done.go
grep -n 'func runIssueChildren\|func groupChildStages\|func unstagedBarrierState\|func childBarrierNotice\|func isTerminalChildStatus' cmd/multica/cmd_issue.go
```
