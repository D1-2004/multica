# Multica Account-key Reconciliation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Persist the trusted DingTalk account key in Multica, reconcile every active local projection with Router ownership, conditionally unbind the exact account, clean takeover remnants locally, and close Agent deletion through a durable account-key outbox.

**Architecture:** `channel_installation.id` remains Multica's local attempt/projection ID. Message binding config stores the Router channel source only for domain-specific APIs and separately stores `router_platform + router_tenant_id + router_account_id` as the cross-domain account identity. All Router ownership calls combine that account key with the immutable Multica Agent ID; no Router binding ID or relation lifecycle is stored.

**Tech Stack:** Go 1.26, PostgreSQL/sqlc, React Query, TypeScript/Zod, React/Vitest.

**Execution boundary:** Execute in the current `dt-fde-multica` worktree. Preserve unrelated dirty changes. Do not commit, push, deploy, or run a formatter unless the PM separately authorizes it.

---

### Task 1: Remove binding-ID-specific work without losing deletion closure

**Files:**
- Delete: `server/cmd/backfill_dingtalk_binding_ids/`
- Delete: `server/internal/integrations/agentmessagerouter/binding_id_backfill.go`
- Delete: `server/internal/integrations/agentmessagerouter/binding_id_backfill_test.go`
- Modify: `server/internal/integrations/agentmessagerouter/client.go`
- Modify: `server/internal/integrations/agentmessagerouter/dingtalk_account_config.go`
- Modify: `server/internal/integrations/agentmessagerouter/service.go`
- Modify: `server/pkg/db/queries/dingtalk_account_binding.sql`
- Modify: `server/migrations/257_dingtalk_binding_unbind_outbox.up.sql`
- Modify: all generated/test/UI/docs files containing `router_binding_id`, `binding_id`, or `replaced_binding_ids`

- [ ] **Step 1: Inventory superseded fields and files**

Run:

```bash
git status --short
rg -n "router_binding_id|binding_id|replaced_binding_ids|backfill_dingtalk_binding_ids" \
  server packages docs
```

Expected: the current uncommitted binding-ID project and any unrelated historical audit fields are reported. Record unrelated occurrences before editing.

- [ ] **Step 2: Remove only relation-ID behavior**

Remove `RouterBindingID`, relation GET/check/delete client methods, relation-based local CAS queries, and binding-ID backfill. Keep the Agent deletion outbox/worker structure, but change its identity in Task 5.

- [ ] **Step 3: Confirm there is no cross-service relation ID**

Repeat the search. Expected: no DingTalk account-binding config, DTO, callback, outbox, command, or UI state depends on a Router binding ID.

### Task 2: Persist and validate the canonical account key

**Files:**
- Modify: `server/internal/integrations/agentmessagerouter/dingtalk_account_config.go`
- Modify: `server/internal/integrations/agentmessagerouter/dingtalk_account_config_test.go`
- Modify: `server/internal/integrations/agentmessagerouter/completion.go`
- Modify: `server/internal/integrations/agentmessagerouter/service.go`
- Modify: `server/internal/integrations/agentmessagerouter/service_test.go`

- [ ] **Step 1: Add config fields**

Use:

```go
RouterPlatform  string `json:"router_platform,omitempty"`
RouterTenantID  string `json:"router_tenant_id,omitempty"`
RouterAccountID string `json:"router_account_id,omitempty"`
```

Keep `RouterSourceID` for channel subscription detail and surface updates. An active message projection is ownership-checkable only when all four values below are present:

```text
agent_id + router_platform + router_tenant_id + router_account_id
```

- [ ] **Step 2: Extend successful callback parsing**

Accept this account identity from the authenticated completion callback:

```json
{
  "source_id": "source-channel",
  "platform": "dingtalk",
  "tenant_id": "corp-a",
  "account_id": "employee-uid",
  "previous_agent_id": "agent-a"
}
```

Require trimmed identifiers and `platform=dingtalk`. `previous_agent_id` is optional and must differ from the new row Agent when present.

- [ ] **Step 3: Add and run focused tests**

Cover missing tenant/account, whitespace, callback/row Agent mismatch, successful persistence, repeated callback idempotency, and rejection of a second account on the same Agent.

Run:

```bash
cd server
go test ./internal/integrations/agentmessagerouter -run 'Test.*(Callback|AccountKey|Config)' -count=1
```

Expected after implementation: PASS.

### Task 3: Replace relation checks with batch account ownership reconciliation

**Files:**
- Modify: `server/internal/integrations/agentmessagerouter/client.go`
- Modify: `server/internal/integrations/agentmessagerouter/client_test.go`
- Modify: `server/internal/integrations/agentmessagerouter/service.go`
- Modify: `server/internal/integrations/agentmessagerouter/service_test.go`
- Modify: `packages/core/types/dingtalk-account-binding.ts`
- Modify: `packages/core/api/schemas.ts`
- Modify: `packages/core/api/schemas.test.ts`

- [ ] **Step 1: Add Router DTOs**

Use:

```go
type DigitalEmployeeBindingKey struct {
    AgentID         string   `json:"agentId"`
    Platform        string   `json:"platform"`
    TenantID        string   `json:"tenantId"`
    AccountID       string   `json:"accountId"`
    ExpectedDomains []string `json:"expectedDomains"`
}
```

Response status is `valid`, `unbound`, `bound_to_other_agent`, or `inconsistent`; `currentAgentId` is accepted only with `bound_to_other_agent`.

- [ ] **Step 2: Make List reconcile all active rows once**

Build expected domains as `channel` plus `calendar` only when `calendar_start_enabled=true`. Send one batch request. Preserve pending and identity-only rows. Map active message rows as follows:

```text
valid                    -> active
unbound                  -> invalid/unbound
bound_to_other_agent     -> invalid/bound_to_other_agent
inconsistent             -> invalid/inconsistent
Router failure           -> router_unavailable
```

- [ ] **Step 3: Add schema compatibility tests**

The Zod schema must default missing reconciliation fields for older backends and reject malformed known values. UI logic must use explicit equality checks.

- [ ] **Step 4: Run focused tests**

Run:

```bash
cd server
go test ./internal/integrations/agentmessagerouter -run 'Test.*(List|Reconcile|Check)' -count=1
cd ..
pnpm test --filter @multica/core
```

Expected: PASS.

### Task 4: Make user unbind conditional on the complete binding key

**Files:**
- Modify: `server/internal/integrations/agentmessagerouter/client.go`
- Modify: `server/internal/integrations/agentmessagerouter/service.go`
- Modify: `server/pkg/db/queries/dingtalk_account_binding.sql`
- Regenerate: `server/pkg/db/generated/dingtalk_account_binding.sql.go`
- Modify: focused client/service/sqlc tests

- [ ] **Step 1: Replace Agent-wide user unbind**

Call `POST /api/digital-employee-bindings/unbind` with stored `agentId`, `router_platform`, `router_tenant_id`, and `router_account_id`. Do not call `/api/subscriptions/digital-employees/{agentId}` from the user action.

- [ ] **Step 2: Apply Router outcomes safely**

```text
unbound              -> revoke exact local row
ownership_changed    -> revoke exact stale local row
inconsistent         -> keep row and return conflict
Router unavailable   -> keep row and return unavailable
```

The local revoke query must compare installation ID, Workspace ID, Agent ID, active status, and all three stored account fields before clearing config.

- [ ] **Step 3: Prove stale requests cannot damage newer state**

Add tests for old E1/A while Router has E1/B, old E1/A while Router has E2/A, and a callback replacing the local row before revoke CAS. None may remove an unrelated Router or local binding.

- [ ] **Step 4: Regenerate and verify**

Run:

```bash
make sqlc
cd server
go test ./internal/integrations/agentmessagerouter ./internal/handler -count=1
```

Expected: PASS.

### Task 5: Retarget the durable Agent-removal outbox to account keys

**Files:**
- Modify: `server/migrations/257_dingtalk_binding_unbind_outbox.up.sql`
- Modify: `server/migrations/257_dingtalk_binding_unbind_outbox.down.sql`
- Modify: `server/pkg/db/queries/dingtalk_binding_unbind_outbox.sql`
- Regenerate: `server/pkg/db/generated/dingtalk_binding_unbind_outbox.sql.go`
- Modify: `server/internal/integrations/agentmessagerouter/binding_unbind_worker.go`
- Modify: `server/internal/integrations/agentmessagerouter/binding_unbind_worker_test.go`
- Modify: Agent archive/delete/runtime cleanup handlers and tests already touched by the current work

- [ ] **Step 1: Store the complete intent snapshot**

The outbox row contains:

```text
installation_id, workspace_id, agent_id,
platform, tenant_id, account_id,
status, attempt_count, available_at, lease fields, last_error_code
```

Remove `router_binding_id` and source-only target selection.

- [ ] **Step 2: Enqueue before ownership rows disappear**

In the same PostgreSQL transaction as Agent archive/hard deletion, copy the complete active account key into the outbox before the installation or Agent join becomes unavailable.

- [ ] **Step 3: Call conditional account unbind outside the transaction**

Treat `unbound` and `ownership_changed` as terminal success. Retry transport, authentication, and upstream-unsubscribe failures. Keep `inconsistent` for operational review.

- [ ] **Step 4: Exercise concurrency**

Test lease expiry, duplicate enqueue, worker races, Router ownership change, Agent deletion, and restart retry.

Run:

```bash
cd server
go test ./internal/integrations/agentmessagerouter -run 'Test.*UnbindWorker' -count=1
go test ./internal/handler -run 'Test.*(Archive|Delete|Cleanup).*DingTalk' -count=1
```

Expected: PASS.

### Task 6: Clean takeover remnants only in the callback's database

**Files:**
- Modify: `server/pkg/db/queries/dingtalk_account_binding.sql`
- Regenerate: `server/pkg/db/generated/dingtalk_account_binding.sql.go`
- Modify: `server/internal/integrations/agentmessagerouter/service.go`
- Modify: `server/internal/integrations/agentmessagerouter/service_test.go`

- [ ] **Step 1: Add exact previous-owner cleanup**

Match:

```sql
agent_id = :previous_agent_id
and config ->> 'router_platform' = :platform
and config ->> 'router_tenant_id' = :tenant_id
and config ->> 'router_account_id' = :account_id
```

Exclude the winning installation ID and never call Router from this cleanup.

- [ ] **Step 2: Make absence idempotent**

Test current-environment match, current-environment absence, another account on the previous Agent, and another Agent owning the same account locally. Only the exact old tuple may be revoked.

- [ ] **Step 3: Run callback-focused tests**

Run the Task 2 command. Expected: PASS.

### Task 7: Enrich historical local projections without binding IDs

**Files:**
- Create: `server/cmd/backfill_dingtalk_binding_account_keys/main.go`
- Create: `server/cmd/backfill_dingtalk_binding_account_keys/main_test.go`
- Create: `server/internal/integrations/agentmessagerouter/account_key_backfill.go`
- Create: `server/internal/integrations/agentmessagerouter/account_key_backfill_test.go`
- Modify: `server/pkg/db/queries/dingtalk_account_binding.sql`
- Regenerate: `server/pkg/db/generated/dingtalk_account_binding.sql.go`

- [ ] **Step 1: Keep dry-run as the default**

Only `--apply` may write. Scan `dingtalk_account` rows without joining `agent`, so deleted-Agent projections remain visible.

- [ ] **Step 2: Resolve from the stored channel source**

For rows with `router_source_id` and missing account fields, batch-call
`POST /api/digital-employee-bindings/source-identities` and require each found
Router source identity to satisfy:

```text
platform=dingtalk
domain=channel
sourceType=digital_employee
non-empty persisted tenantId and accountId
returned sourceId equals stored router_source_id
```

Current Agent ownership may differ and must be reported, not used to reject safe identity enrichment.

- [ ] **Step 3: Write through full-snapshot CAS**

Outcomes are `would_update`, `updated`, `already_complete`, `source_not_found`, `missing_tenant`, `invalid_local`, `router_unavailable`, and `cas_conflict`. Audit output fingerprints account/source identifiers and never prints raw values.

- [ ] **Step 4: Test and build**

Run:

```bash
cd server
go test ./internal/integrations/agentmessagerouter -run 'Test.*AccountKeyBackfill' -count=1
go test ./cmd/backfill_dingtalk_binding_account_keys -count=1
go build ./cmd/backfill_dingtalk_binding_account_keys
```

Expected: PASS.

### Task 8: Update the binding list UI and product states

**Files:**
- Modify: `packages/views/agents/components/integrations/dingtalk-account-binding.tsx`
- Modify: `packages/views/agents/components/integrations/dingtalk-account-binding.test.tsx`
- Modify: `packages/views/locales/en/agents.json`
- Modify: `packages/views/locales/zh-Hans/agents.json`
- Modify: `packages/views/locales/ja/agents.json`
- Modify: `packages/views/locales/ko/agents.json`

- [ ] **Step 1: Lock the required Chinese states**

```text
bound_to_other_agent: 该数字员工已经绑定到其他智能体，消息订阅已失效
router_unavailable: 暂时无法核验消息订阅状态
```

- [ ] **Step 2: Preserve the unbind affordance**

Keep unbind available for `active`, `unbound`, and `bound_to_other_agent`. Disable automatic Router mutation for `inconsistent` and keep the local record visible for diagnosis.

- [ ] **Step 3: Test enum fallback and run checks**

Unknown status renders a safe unavailable state and never hides or auto-deletes the projection.

Run:

```bash
pnpm test --filter @multica/views -- dingtalk-account-binding.test.tsx
pnpm typecheck
```

Expected: PASS.

### Task 9: Verify and hand back evidence

- [ ] **Step 1: Run every focused command above**
- [ ] **Step 2: Run `make test` and `pnpm test` if focused checks pass and time permits**
- [ ] **Step 3: Run `git diff --check` and inspect the complete diff/status**
- [ ] **Step 4: Confirm no formatter was run and no unrelated dirty file was altered**
- [ ] **Step 5: Report PASS/FAIL for persistence, reconciliation, stale unbind safety, takeover cleanup, deletion outbox, enrichment, UI states, and protocol history**
- [ ] **Step 6: Stop without commit, push, migration execution, backfill execution, or deployment**
