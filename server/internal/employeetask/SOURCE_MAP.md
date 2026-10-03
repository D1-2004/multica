# Work Object Compiler source map

The Work Object Compiler is a modified direct port from Nex / Wuphf (gawkbot),
fixed source commit `71e82a1809565281cbd0bf8185d3c125b715d934`, read from
`/Users/mac-m3/github/gawkbot` with `git show <sha>:<path>`. The port is active:
`Compile` invokes canonicalization and the adapted execution-packet builder.
There is no unused vendored implementation or alternate model path.

Copyright (c) 2026 Nex. The original [LICENSE](LICENSE) is retained verbatim.
These source portions remain under the Sustainable Use License and its use and
distribution restrictions, not the surrounding project's license. This is a
modified copy: Multica adaptations are identified below and in source notices.

| Fixed source | Local active symbol | Copy/adaptation |
| --- | --- | --- |
| `internal/team/task_definition.go::normalizeTaskDefinition` | `definition.go::NormalizeDefinition` | Directly copied goal trimming/required validation, fresh output construction, ordered deliverable/success-criteria normalization and blank-access filtering. Adapted to the existing value `Definition` and string deliverables; optional formats remain part of the supplied deliverable string. Errors wrap existing `ErrInvalid`. Removed caller-independent timestamp stamping because the pure compiler owns no clock or persistence. |
| `internal/team/task_definition.go::taskDefinitionPacketLines` | `definition.go::taskDefinitionPacketLines` | Directly copied definition section assembly and numbered success criteria, adapted to string deliverables. Removed clipping so goal or constraint tails cannot disappear. Access-needed output explicitly says requested, not granted. |
| `internal/team/notification_context.go::BuildTaskExecutionPacketWithContext` | `packet.go::buildTaskExecutionPacketWithContext` | Copied the real execution-packet function, then adapted its packet lines, correction-before-definition ordering, definition insertion, context manifest accumulation and report destination to Host-provided facts. The final function is called by `Compile`. |
| `internal/team/task_ledger.go::TaskLedgerEntry.ContextUsed` and packet builder return contract | `WorkPacket.ContextUsed` | Preserved a deterministic list of references actually injected during packet construction, not model-reported usage. Manifest order matches rendering; duplicate refs occur once. |
| `internal/team/task_definition_test.go::TestNormalizeTaskDefinition` | `definition_test.go::TestNormalizeDefinition` | Migrated required/blank-field checks, whitespace canonicalization, blank access omission and no-input-aliasing assertions. Omitted the nil-pointer and DefinedAt cases because neither exists in the local value contract. |
| `internal/team/task_definition_test.go::TestExecutionPacketCarriesDefinition` | `compiler_test.go::TestCompileWorkPacketCarriesDefinitionAndHostFacts` | Adapted execution packet assertions from the original real task definition test; the local seam is pure Host input rather than a live broker mutation. Added scope/principal, source, corrections, completed steps, references, capabilities and actual context-use checks. |

## Host boundary and deliberate removals

- `Compile(CompileInput) (WorkPacket, error)` is pure. It has no model, database,
  filesystem, retrieval, clock, cursor, delivery or resume dependency. It calls
  only existing pure scope validation, copied definition normalization, and the
  active packet renderer.
- Scope, principal, source, current corrections, completed steps, formal material
  references, verified capabilities and report address are selected by the Host.
  Every supplied material must match the exact scope and principal before any
  output is rendered. A shared material must first be authorized for that caller
  by the Host; the compiler never derives access from body text or display names.
- The source reference always appears in the packet and manifest because it is
  the actual provenance anchor. Other blank-body candidates are omitted from both
  text and `ContextUsed`. No free search or invented “retrieved context” is added.
- History is explicitly `unavailable`, `empty`, `available` or `truncated`.
  Unavailable is not treated as successful empty retrieval; truncated history is
  labelled partial. Unknown or internally inconsistent history states fail.
- `Goal` is the only required definition field. Deliverables, success criteria
  and access requests remain optional. Access requests never modify the separate
  Host capability list or authorize an action.
- Upstream `consumeTaskHumanNote` was removed: merely compiling/rendering a
  correction cannot clear a stop gate. Corrections and the complete task contract
  are not truncated. No snapshot hash or new definition readiness gate was added.
- Removed upstream forced wiki artifact completion, office roster/lead staffing,
  shell/worktree assumptions, live business execution forcing, retrieval/embedding
  calls, task mutation commands and channel-slug fallbacks. The Host supplies the
  true capability and return-address facts instead.
- The existing durable `Definition`, task Store and Resume implementation were
  left unchanged. The Host persists the normalized definition and packet/manifest
  using its existing atomic admission path.

## Validation

The behavior tests were observed failing against empty compiler/canonicalizer
scaffolds before porting the production functions. The pure suite checks the
migrated canonicalization contract, actual context manifest, all material kinds
against cross-scope/cross-principal leakage, distinct history states, full long
human corrections, stable output, input immutability and optional definition data.

```sh
cd server
go test -race ./internal/employeetask -run 'TestNormalizeDefinition|TestCompile' -count=1
go vet ./internal/employeetask
```

Database Store tests remain owned by the task-domain validation suite; no database
is needed to compile a Work Packet.

The WorkPacket now also carries a Host-validated completion-notice policy and
its exact selected-source quote. The Host's deterministic completion hook adapts
GawkBot completion delivery to Multica's verified native-file receipts; receipt
and provider checks remain outside the kernel, with no extra model call.

## Lifecycle v2 (design reuse only, no copied code)

`lifecycle.go` re-implements GawkBot mechanisms from the same fixed commit
`71e82a1809565281cbd0bf8185d3c125b715d934`; no source text was copied, so no
symbol carries the Sustainable Use License.

| GawkBot design | Local implementation |
| --- | --- |
| `internal/team/broker_lifecycle_transition.go::transitionLifecycleLocked` single chokepoint and closed state enum | `transition`/`lifecycleEdges`: every persisted Task state write in `mutate` is validated against a per-version table with the requesting operation as its cause. Illegal writes return a typed `LifecycleError` before anything is written; v1 rows keep the accepted pre-v2 table. |
| `office_eval_jobs_live_paths.go` zero-work approve returns a structured 409, never terminal; `office_eval_jobs_task_integrity.go` one done-post per double terminal attempt | `CompleteGoal` returns `not_ready/no_execution_evidence` without a Run of the current goal revision that succeeded or an explicit evidence ref; a source replay returns the same entry, a second source gets `conflict/already_completed`, and a unique index allows one `goal_completed` entry per goal revision. |
| `broker_tasks_human_note.go` halt gate (a stop blocks completion) | A human stop is final for v2: `CompleteGoal`, `ReadyTask`, `WaitTask`, `StartRun`, `Steer` and `Resume` return `stopped`, inputs never lift it, and open waits close with the stop. |
| `broker_tasks_lifecycle.go` dependency release only on a real terminal fact | Waits are durable `employee_task_wait` rows resolved only by `ReadyTask` with Host evidence; the Task state is their projection, never a model claim or a TTL. |
| `governor.go` turns since the last human checkpoint | `autonomous_rounds` counts non-human wakes (`NoteAutonomousRound`, source-deduplicated) and resets on accepted human input. No cap is enforced here. |

Deliberately not ported: in-memory indexes, the unknown-state migration shim
(PostgreSQL CHECKs reject unknown states instead), review/approval states, and
the per-process governor pause channel.
