# Remove DWS Reply Tracker Implementation Plan

> **For Codex:** Implement each task in order with test-driven-development and verification-before-completion. The optional executing-plans skill is unavailable in this workspace, so execute the checklist directly in this task.

**Goal:** Remove the legacy daemon-side DWS reply receipt as a completion fact source and make the provider-selected final `output` the normal terminal and delegation-update body sent through Router.

**Architecture:** Keep both Router outboxes and their persisted `result_message` snapshots because they are current delivery contracts. Remove the tracker-specific `ResultMessage` channel from provider results, daemon terminal reports, daemon HTTP callbacks, handler DTOs, and failed-task JSON. Completed terminal callbacks derive their body from immutable provider `output`; failure callbacks retain the explicit `error` and `failure_reason` contract, while delegated comment callbacks retain their persisted thread-reply mapping. Remove the DWS reply command from every active prompt path; compatibility inputs also finish with ordinary provider output.

**Tech Stack:** Go, PostgreSQL/sqlc, Markdown protocol documentation.

---

### Task 1: Pin the new output-source behavior with RED tests

**Files:**
- Modify: `server/internal/service/task_completion_test.go`
- Modify: `server/internal/handler/task_complete_request_test.go`
- Modify: `server/internal/daemon/client_test.go`
- Modify: `server/internal/handler/agent_dispatch_v2_test.go`

- [x] Change terminal completion expectations so legacy `result_message` cannot override provider `output`.
- [x] Assert daemon complete/fail callbacks no longer send tracker-only `result_message`.
- [x] Assert legacy inbound JSON is tolerated but omitted from the typed terminal payload.
- [x] Assert the DWS dispatch prompt no longer requires `dws chat message reply`.
- [x] Run the narrow tests and capture the expected failures before production edits.

### Task 2: Remove the tracker and its daemon transport

**Files:**
- Delete: `server/internal/daemon/dws_reply_tracker.go`
- Delete: `server/internal/daemon/dws_reply_tracker_test.go`
- Modify: `server/pkg/agent/agent.go`
- Modify: `server/internal/daemon/types.go`
- Modify: `server/internal/daemon/daemon.go`
- Modify: `server/internal/daemon/client.go`
- Modify: `server/internal/daemon/pending_reports.go`
- Modify: related daemon tests

- [x] Stop observing provider tool messages for DWS reply receipts.
- [x] Remove `ResultMessage` from provider and daemon result/report structs.
- [x] Preserve provider `Output` unchanged through complete callback persistence and pending-report replay.
- [x] Preserve explicit error/failure-reason semantics for fail callbacks and permanent-complete fallback.
- [x] Keep usage merging and terminal ordering behavior unchanged.

### Task 3: Remove tracker-only server passthrough

**Files:**
- Modify: `server/internal/handler/daemon.go`
- Modify: `server/pkg/protocol/messages.go`
- Modify: `server/internal/service/task.go`
- Modify: `server/internal/service/task_completion.go`
- Modify: related handler/service tests

- [x] Remove daemon request DTO `result_message` fields and failure-result JSON encoding.
- [x] Derive completed terminal Router `ResultMessage` from provider `Output`.
- [x] Keep failure output empty unless the current persisted comment/thread reply contract supplies it.
- [x] Keep execution-update freezing in the terminal transaction and outbox retry payload immutable.
- [x] Keep Router DTOs, terminal/update outbox columns, claim ordering, and rolling-binary visibility rules unchanged.

### Task 4: Update prompts and protocol/design history

**Files:**
- Modify: `server/internal/handler/agent_dispatch_v2.go`
- Modify: `server/internal/service/builtin_skills/multica-delegating-to-issues/SKILL.md`
- Modify: `docs/agent-dispatch-v2-execution-contract.md`
- Modify: `docs/issue-delegation-design.md`

- [x] Describe ordinary provider final output as the Router delivery body.
- [x] Remove requirements and factual-source references for the legacy DWS reply command/tracker.
- [x] Append dated history entries and the cleanup reason at each protocol/design document end.

### Task 5: Verify and commit locally

**Files:** All changed files.

- [x] Run focused GREEN tests for service, handler, daemon, and Router workers.
- [x] Run the requested daemon/service/handler regressions and integration tests.
- [x] Run `make build`.
- [x] Run `git diff --check` and inspect `git diff --stat` plus the full diff.
- [x] Confirm no formatter, push, CR, deploy, or Aone action occurred.
- [x] Append one local conventional commit without amending `277159283afacc54a25211f01d2092e8d8265edc`.
