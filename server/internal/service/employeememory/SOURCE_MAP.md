# Employee memory source adaptation

This directory contains modified GawkBot code, copyright (c) 2026 Nex. The full
upstream Sustainable Use License is preserved in [LICENSE.gawkbot](LICENSE.gawkbot).
The original license continues to apply to those portions; this adaptation does
not relicense them.

Pinned source: `/Users/mac-m3/github/gawkbot`, commit
`71e82a1809565281cbd0bf8185d3c125b715d934`. The source files were clean at extraction.

| Source | Actual reused code | Destination and changes |
| --- | --- | --- |
| `internal/team/learnings.go` | `LearningRecord`, type/source enums, `dedupeLearnings`, `effectiveLearningConfidence`, `learningMatchesQuery`, instruction-override rejection | `learning.go`: additional evidence IDs; deterministic UUID tie break; non-decaying confidence requires Host trust rather than a model-claimed source. `store.go`: PostgreSQL replaces JSONL/wiki writes; exact ownership precedes matching. |
| `internal/team/scoped_memory.go` | `normalizeMemorySearchText`, `privateMemoryMatchScore`; adapted pure brief formatting and untrusted-data wrapping | `brief.go`: scope is authorized by the Host and directory before retrieval; bounded learning summaries include evidence IDs; delimiter neutralization prevents injected closing fences. No shared-memory fallback. |
| `internal/team/task_distill.go` | `learningKeyFromTitle`, `taskDistillInsight` | `distill.go`: verified Host snapshot replaces broker task; title prefix plus full Host task-ID digest prevents Chinese/title collisions; deterministic clipping; persistent Host task/execution replay identity replaces process-local single-flight. |
| `internal/team/memory_workflow.go` | Citation/artifact keys, normalization, merge and append functions; lookup/capture/promote step recorders | `workflow.go`: functions take a workflow snapshot instead of a broker task. `workflow_store.go` persists bounded progress in each learning record. Steps remain informational (`not_required`); no task-completion gate. |
| `internal/team/session_memory.go` | `BuildSessionRecovery` and request/task/highlight helpers | `recovery.go`: local immutable snapshot types; explicit running/isolation predicates; original blocking-request priority retained. |

`internal/team/learning_commit.go` was reviewed as an integration reference.
The wiki worker, GBrain, filesystem commits, actual promotion execution, and
periodic model synthesis were **not** imported. Workflow progress records only
Host-observed lookup results and committed artifact references; recording a
promotion step does not itself promote content or grant trust. The
pinned repository has `learnings.go` and `learning_commit.go`, not a singular
`learning.go`.

## Host and storage contract

- `Scope` and `TrustedEvidence` are constructed from trusted Host metadata. Never
  deserialize them from model tools. The model may propose a bounded
  `LearningRecord`; its claimed trust, source, scope, author, timestamps, and IDs
  cannot grant authority. Host-confirmed human evidence and Host-verified task
  execution are the only trusted sources. Verified evidence must contain Host
  `TaskID` and `ExecutionID`, which replace any model-selected record fields.
- Each operation resolves `scene.Ref` through the scene directory and checks its
  workspace, agent, and current trusted tenant. Reads query the exact tuple
  `(workspace, agent, tenant, scene, scope_kind, principal)` before fuzzy matching.
  Private scope requires a principal and remains scene-partitioned. No widening
  to global, organization, shared, or legacy memory is performed.
- Every write transaction takes `workspace FOR KEY SHARE` before the namespace
  state lock. The root workspace deletion transaction must delete
  `employee_learning` and then `employee_memory_state` while holding the workspace
  `FOR UPDATE` lock. There are no foreign keys or cascade operations.
- Corrections link `supersedes` to the current record in the same exact namespace,
  type and key. Untrusted proposals cannot overwrite trusted facts. PostgreSQL
  replay keys persist across process restarts and concurrent consumers. Ordinary
  capture identity is the Host source/evidence pair, independent of model key or
  type; verified identity is the Host task/execution pair.
- `Reset` forgets only the exact namespace and retains inactive replay tombstones.
  Replayed old writes return their original receipt but cannot restore the old
  memory in `Search` or `Brief`. This is namespace forgetting, not erasure of the
  retained audit/evidence record.
- `Record` and `Distill` attach capture progress to the saved learning record.
  `RecordWorkflow` records bounded Host-observed lookup/capture/promote progress;
  `Workflow` reads it under the same exact authorization scope. Neither accepts
  model-supplied progress as authoritative. Each step preserves its Host source
  and evidence IDs. Forgotten or superseded learnings reject workflow updates.
  `Brief` remains read-only.
- `Distill` must be invoked by a durable background consumer after verified task
  completion commits. It does no LLM work. `Brief` only reads stored facts;
  `Record` performs synchronous database writes and should run in a capture job
  when originating from an interactive turn. This package does not supply the
  event/outbox worker or claim production integration by itself.

## Limits and verification

Search considers the newest 2,000 active records in one exact namespace and
returns at most 100 (default 5); the brief clips each insight to 300 runes.
Input insights are capped at 4,000 bytes, evidence IDs at 256, and ancillary arrays
at 16 items. Workflow arrays are capped at 16 entries, one update at 6,000 bytes
and the complete record at 14,000 bytes. There are no external memory backends,
embedding retrieval, model synthesis, or automatic cross-scene promotion in this
batch.

The PostgreSQL tests use a fresh isolated schema and the real 9620–9624 migrations,
plus the scene directory migrations. Enable them explicitly with
`EMPLOYEE_MEMORY_TEST_DATABASE_URL`. No production database was used. Tests cover
new/legacy isolation, ownership/tenant/principal mismatch, correction and trust,
confidence decay, parallel replay and restart replay, reset tombstones, verified
admission, and workspace deletion fencing. Pure tests cover source-adapted
recovery, key/insight assembly, and prompt-fence handling. Review regression
tests cover distinct Chinese tasks, changed model key/type after reset,
Host-owned task binding, persisted informational workflow, duplicate citation
merging, workflow reset fencing, and rejection of model-authored progress.

Source SHA-256 values:

```text
5184a3a6d1ea9e16f71a8cd9fb3365b48beb90ef2a9351cff9cf21059ae3ef4d  internal/team/learnings.go
8edc6fee7c42a0f5b4aa418be2433b4827c9a20a539a047fcd7809dbbd1d83f1  internal/team/scoped_memory.go
221c38a911e5bb0369fcc273776daf6cdf36b8ede9f12d44cda475ee03c76ed9  internal/team/session_memory.go
a52522932758ce33b4f4014e1525884e844817ea7d89c3c6a950f2d4f863cb86  internal/team/task_distill.go
9a1481df5c3e0556e39b68e220339c99d694d23901d527ecd0d782685ed544f6  internal/team/memory_workflow.go
fc468cc42d61e463265c07994c99161a11cbede8ea48d5ef45a5981bcdb5cad0  LICENSE
```

## Scene memory management (D10, Multica Host extension)

`management.go` is a Multica database adapter, not copied GawkBot code. It lists
and reads only the shared scene namespace, preserves the learning evidence model,
and resets one exact scene with an optional expected revision. Private principal
namespaces are never merged into these views or reset by a scene manager. Existing
replay tombstones remain, so a previously forgotten source cannot repopulate the
scene merely by replaying.

The management HTTP seam is `handler/employee_memory_management.go`, reached from
the existing scene-memory routes with explicit `loop=coordinator|employee`. New UI
requests always send loop; query keys include it. Writes additionally carry
`scene_id`, `org_id`, and `expected_revision`, and lock workspace → selected agent
mode → memory revision in one transaction. A changed mode, tenant or revision is
rejected; Employee PUT/association-clear is unavailable because learning is not an
editable Coordinator text blob. Coordinator association cleanup is transactional
with its own reset and never runs for Employee memory.

Omitted-loop legacy reads retain the Coordinator API namespace. Omitted-loop
writes also require the agent to still select Coordinator, preventing an old page
from editing/resetting Coordinator memory after switching the agent to Employee.
Current explicit management reads check the current tenant directory. The initial
management surface intentionally does not expose private memory; a manager role
alone never authorizes reading or combining other principals' records.

`ResetScene(ctx, scope, expectedRevision)` is reusable by the separately owned
inbound `/reset-memory` Host. Its nil-revision option is for an already-authorized
command with a frozen loop/source; it neither parses inbound actors nor chooses a
loop. `ResetSceneTx` supports the caller's existing transaction.

Evidence: local PostgreSQL management tests cover namespace/private isolation,
CAS reset, tombstones, stale selection, unsupported free editing and old-tenant
rejection. UI/API tests cover loop-aware keys, explicit selectors, scope/revision
submission, hidden legacy controls and dropping a confirmation across a loop
switch. No live model or pre-release service is required.


## Background capture and inbound reset

`handler/employee_learning_capture.go` consumes terminal Employee-owned Run evidence
through `internal/employeelearning`. Since M1 it no longer records ordinary queue
outcomes as inferred, confidence-3 requester-private candidates: each Run gets
its durable consumption receipt with reason `candidate_retired` and no learning
row (GawkBot `task_distill.go`: distill verified outcomes only). Verified
outcomes are distilled by `internal/employeeverification`. Rows written by older
binaries stay stored but are never injected (inferred is excluded at read time).

Employee inbound `handler/employee_scene_entry_memory.go` handles an exact standalone
`/reset-memory` per frozen source. In one journaled transaction it calls
`ResetSceneTx` for shared memory and `ResetPrivateTx` for only that verified sender.
It does not change Coordinator associations/memory or another requester's private
records. Other messages in a collected window still reach the Loop. The management
page's existing shared-only reset contract remains unchanged.


## Requester-private source capture (Multica Host extension)

`handler/employee_memory_tools.go` connects the adapted record/search/correction
mechanisms to Employee native tools. Only a unique source in a single-known-
requester window is eligible. The journal transaction binds the authenticated
platform principal separately from the provider-attributed requester; scope,
evidence identity and receipt-created timestamp are Host-owned. Outer-message
exact quotes produce observed confidence-4, untrusted records. Quoted background
and reactions cannot supply the quote; quoted replies may carry a new outer
correction. The wire protocol does not attest a human sender or originality,
and native forwards are not fully represented in DispatchMessage.

`RecordPrivateObservationTx` adds ordering only for this new path. It persists
Host `evidence_occurred_at` and compares all same-key/type records, including
inactive tombstones. Earlier/equal sources are recorded inactive, so changing a
proposal's key/type cannot reuse their evidence. Legacy records without that
field use their Host `created_at` as a conservative fence. Existing background
Record/RecordTx correction behavior is unchanged. Trusted active facts still
reject untrusted replacement.

`SearchTx`, `PrivateEntryTx`, and `ForgetPrivateTx` use the caller's transaction;
no pool connection is borrowed while the journal holds one. Exact forget retains
its replay tombstone and never deletes a replacement. Capture responses expose
active/forgotten/superseded state, including after evidence replay. The
employeeentry journal supports an effect-free replay projection for current
source authorization and record state. Unchanged lookup records retain frozen
confidence values; changed state can cause the following model-journal request
to conflict and end the wake through its existing failure checkpoint.

Tests in employee_memory_tools_test.go exercise real admission, provider-visible
next-turn memory, correction/forget, trust and source boundaries, mixed-window
isolation, one-connection execution, atomic journal rollback, source ordering,
same-native-call replay, and old-schema recovery. private_entry_test.go covers
store ordering, legacy evidence times and exact transaction rollback. These are
local PostgreSQL checks, not a claim of pre-release acceptance.


Automatic private brief injection is limited to new snapshots whose fenced
agent_scene row explicitly says dm and whose original window has one known
requester. A group does not automatically receive recent private records;
explicit memory_lookup still searches its authorized scene/requester namespace.
The shared-scene layer, stored records and ranking are unchanged. Unknown kinds
are not treated as dm. Previously frozen group snapshots retain their historical
input and journal identity. Regression tests cover provider-visible group input,
scoped explicit lookup, DM first-call answers and historical group replay.


## Verified distill transaction and Chinese-aware retrieval (G1)

`DistillTx` lets the durable verified-distill consumer
(`internal/employeeverification`) write the learning and its consumption
receipt in one transaction. `VerifiedRun.OccurredAt` carries the Host work-end
time of the verified Run; it is the reset fence and is never refreshed by
retries or late verification. Replay identity stays the Host (task, execution)
pair, so a re-verified Run returns the original (possibly forgotten) record.

`retrieval.go` is a Multica re-implementation (no verbatim code) of the design
of `internal/team/context_assembler.go`: IDF-weighted distinct-unit overlap
with a minimum of two units, and a mandatory retrieval block that states what
was searched when nothing matched. The upstream tokenizer splits on
non-letters and drops tokens shorter than three bytes, which yields nothing
usable for unspaced Chinese; here CJK runs become character bigrams (overlap
units, with particle/function-character filtering) and trigrams (bonus
weight), and ASCII words keep the three-character floor. `Retrieve` ranks only
the exact authorized namespace. Its production consumer is the foreground
brief below; `Search`/`memory_lookup` still use substring matching (tests pin
the gap).

```text
47b6cf75210a73bdc977b34a80637306aaa2a3a653c3e1db5901bd9f0bf7dda7  internal/team/context_assembler.go
```


## Foreground brief v2 and DM person view (M1, Multica Host extension)

`foreground.go` and `person.go` are Multica code (no verbatim GawkBot code);
the retrieval design follows `internal/team/context_assembler.go` through
`RankLearnings`. `ForegroundBrief` builds the frozen `Input.Memory` of every new
chat and task-wake snapshot through `handler/employee_memory_input.go`:

- Corpus: one SQL read of the scene layer (newest 500 active, non-inferred
  records) plus, only in a DM window with one known requester, that
  requester's private records (300). Records whose text reads like an
  instruction (English or Chinese phrases) are dropped at read time as well as
  rejected at write time.
- Sections: pinned preferences (`type=preference`, observed or user-stated,
  no query needed; group 3 from the scene layer, DM 4 with private first), a
  mandatory retrieval block for the current window (states the searched terms
  and "无命中" or "无可检索词"; never falls back to newest records) and verified
  experience (trusted `execution` records). Group/enterprise ≤4 KiB, DM ≤4.5 KiB.
- Rendering keeps the fence and its neutralization, shows type, attribution and
  date, never evidence/source IDs, confidence or requester refs. Entries carry
  `(id=<uuid>)` for the v1 forget tool, or `[mN]` when the snapshot's tools
  resolve labels. The manifest (`m1..mN`, section, id, scope, origin scene,
  bytes) and stats are frozen beside the input and sent to Langfuse as
  metadata (ids and sizes only).
- Person view: in a DM with one known requester whose ref is org-qualified
  `dingtalk:<tenant>:uid:` or `:open_id:`, private records of that requester
  from every scene of the same workspace, agent and tenant are read, each
  re-fenced against `agent_scene` and bounded by this DM's own private
  `reset_at` (9872 `employee_learning_person_idx`). staffId-only refs keep the
  exact DM namespace. A group never reads private memory, even from a single
  speaker, so nothing flows from a DM into a group.
- Old snapshots are replayed as frozen; the brief is only built for new ones.
