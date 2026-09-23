# DSH startup investigation — CR 36224658

## Scope and evidence

The September 21 production incident proves a 30-second native-create deadline
crossing, but does not prove that all 75 similarly worded failures share one
internal cause. Twenty fresh failed sessions contained initialization records
only; their first events appeared 30.160–34.642 seconds after task start.
Thirty-eight successful fresh sessions reached their first events in
23.756–29.875 seconds. Some failed creates continued after the platform failure.

Production task `7241ff44-eb0a-426e-8e15-5157ebc6ec5b` started at
2026-09-21 10:16:30.840 +08:00 and failed at 10:17:00.947. Its native header and
initial events appeared at +30.553 and +31.057 seconds respectively.
Exact SLS/Langfuse evidence for `d8f3afdc-3d2f-4bb4-9d71-05ad5b1baa16`
showed no business model/tool observation before failure.

The installed production Runtime was `94f5c3d6484d07451f0770a7f3d85f3f84c190e0`,
DSH `0.1.6-alpha.1`, template `3rg3ebpz7rxmqe6ii5jn`.
At inspection time its Home contained 5,453 projects and 5,512 sessions.
Read-only production probes using fresh, nonexistent IDs measured:

| Probe | Global encoding traversal | Global ID lookup | Total stat |
| --- | ---: | ---: | ---: |
| Sandbox 1 | 7.353 s | 7.529 s | 14.883 s |
| Sandbox 2 | 8.454 s | 8.818 s | 17.272 s |

These are direct measurements of substantial historical metadata overhead,
not stage spans from the original failing requests. Preset composition, target
session restoration and contention contributions still require observation.

## Narrow repair

The companion Runtime branch is
[`fix/dsh-history-create-36224658`](https://code.alibaba-inc.com/dingtalk-ai-lab/multica-fc-hermes-runtime/tree/fix/dsh-history-create-36224658),
commit `242bea0`.

- Check root directory metadata without enumerating its children.
- Validate encoding/layout at the selected session artifact, not across the Home.
- The managed create carrier supplies the platform-allocated UUID, persisted cwd
  and new/resume intent. An atomic immutable location claim resolves subsequent
  stat/open/create calls directly. Existing session headers and original writer
  locks remain authoritative. A missing resume is not silently recreated.
- The platform owns UUID allocation. New claims do not discover unindexed legacy
  IDs in other projects; native explicit by-ID browsing retains its legacy lookup.
  This is a managed-task path optimization, not a replacement for native browsing.
- Keep the original request deduplication and complete target-session baseline.
  Do not change projection recovery, preset composition or task admission policy
  based only on suspected cost. Do not increase timeouts or automatically replay
  uncertain operations.

## Diagnostics

The daemon logs `dsh_native_startup_phase` for create_new/create_resume,
history_baseline and task_admit, with session/request/agent/generation and duration.
Native phase receipts split locate, observe, compose, restore and new_agent.
The supervisor preserves only these bounded records in
`/tmp/multica-dsh-host/diagnostics.jsonl`, rotating at 4 MiB with two backups.
Raw plugin exception bodies, prompts, credentials and launch URLs are excluded.

Native RPC receipts distinguish deadline, transport, native rejection and startup
module-missing categories; elapsed time is retained in the platform failure.
A deadline/transport failure still means the side-effect outcome is uncertain.

## Delivery and acceptance status

The application change and Runtime companion must be validated together. A new
application deployment alone does not replace the embedded daemon/native packages
in an existing Runtime template. Keep both immutable artifact identities in the
next preproduction acceptance record.

The user paused immediate publication and narrowed the change on September 21.
No new candidate build, preproduction deployment or production release has been
triggered for this repair. No recovery or improved production success rate is
claimed. Local checks are static/unit evidence only: Go native backend/transport
suite, Python host/install contracts, and six JavaScript location/phase cases.
Real native E2E checks were not run locally.

The next preproduction round must use a fresh candidate template and cold Host,
with a comparable historical directory count, fresh UUIDs and existing-session
resume. Verify task terminal results, phase durations, native files and exact
Runtime/application commits. Observe any remaining failures before expanding
repair scope.
