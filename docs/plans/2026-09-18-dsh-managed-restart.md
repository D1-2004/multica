# DSH managed restart and Tavern acceptance

Status: preproduction deployment, integration, and real native/DingTalk acceptance
passed on 2026-09-18. Production application/runtime rollout is not part of this
release. The existing preproduction robot was restored to its original settings.

## Fixes and root causes

- Tavern 2.3.9 assumed compressed logs. The `dsh-tavern-multica` fork supports
  native plaintext JSONL and Zstandard logs, preserves the existing encoding,
  and registers its client bundle under the fork's package name. Version
  `2.3.9-multica.3` uses the native conversation draft service for insertion,
  preserves existing draft text, and verifies readback before reporting success.
- Native marketplace restart previously escaped the managed supervisor. Runtime
  commit `f1874cbbd8e658fc125501b384c68bf28e1c42c7` supervises child restart,
  preserves the profile, and rejects restart during active tasks or mutations.
- Native synchronization failure no longer prevents reading saved plugin/profile
  configuration. `native_sync_pending` explicitly means the saved configuration
  is readable but not confirmed current; writes still require synchronization.
- Bare `/new` and `/reset` lost their command text in DingTalk normalization.
  The adapter now preserves canonical command text for the durable reset handoff.
- Clearing provider resume state alone left database-backed history intact.
  Claims now use an immutable input reset boundary and exclude late answers from
  pre-reset tasks. Visible transcripts are retained.
- DSH also had a permanent chat-to-native-Session mapping. Durable reset epochs
  now select a new native Session, while task retries retain exact committed
  Session/request identities. Explicit old native sessions and schedules retain
  their own epoch. Model history excludes messages owned by other native Sessions.

## Release evidence

Application CR: https://cd.aone.alibaba-inc.com/unite/micro/cr/app/342160/36211542

| Run | Commit | Result |
| --- | --- | --- |
| 3108891954 | 43ad47c53906cf0ac05db95bf3bc35edea99ab5d | Deployment and integration SUCCESS |
| 3108895617 | 058ed3400 | Deployment and integration SUCCESS |
| 3108905913 | d5cb56cc6 | Epoch expansion: deployment and integration SUCCESS |
| 3108907874 | 7eed27fd7 | Epoch activation: deployment and integration SUCCESS |

Epoch expansion retained the old uniqueness index until all replicas supported
new epochs. Activation then removed it via `9270_dsh_session_epoch_scope`;
bootstrap readback confirmed both expansion and activation migrations. Do not
roll back to pre-epoch binaries after activation; preserve epochs in a forward
repair. The pipeline is at the normal manual preproduction verification gate.

Runtime CI 73671213 passed; candidate template `u7rcxpnirvu7csz1i2m7` uses image
`dsh-managed-restart-f1874cbbd8e658fc125501b384c68bf28e1c42c7`, digest
`sha256:7e814e1d6df2d61cbef8f8608981e80ae515243bcd345cc42d1c17c9b2c67fe3`.
Candidate runtime: `387d4e89-a0a3-486d-a04b-fd1349b24c0f`.
Pre plugin: `92e70a9a-3491-4b8a-a1e1-cc55dc33ca0a`, version 2.3.9-multica.3.

## Native restart, role and insertion

- Native boot changed `41-1789723257420` -> `349-1789723345450` on sandbox
  `sbx-3ef7a97d-346d-4628-a5c4-3fcf582baa13`. The same entry recovered and
  profile revision 142 remained applied/current.
- During task `72a54f8e-adba-40ee-9c60-f7e0e9dcc6fd`, restart returned HTTP 409;
  the task completed normally with the expected role.
- Native task `1c7557ca-72aa-432f-a3de-4ed1b65c4d65` and ordinary workbench task
  `61dd554b-7f8c-437e-8d21-33c2983542de` introduced 星野澄 and her cat 团子.
  Ordinary new chats require the desired preset to be the native default:
  choosing a preset for one existing session does not change that default.
- In the Codex in-app browser on the existing pre bot, selecting tavern-lite in
  Tavern management loaded the card. Clicking Insert into current conversation
  placed the full card in the composer and displayed the instruction to return
  and send. Insertion is a draft action, not automatic submission. The test
  draft was cleared. The earlier browser-provider blocker was resolved.

## Existing robot reset acceptance

No robot was created. Existing Agent `167f831a-73cb-4087-a86a-d1cbe4c08145`
(须莫v7 Pre ASB Pi), installation `b0aaa072-b462-42ef-bce5-99906a6daa82`, robot
须莫v7_Pre_Pi_钉钉组织 was temporarily bound to the candidate DSH runtime with
coordinator disabled and the test Tavern card. Native DingTalk sends triggered
the preproduction tasks. DWS API personal sends delivered IM messages but did
not trigger this robot callback, so they were not used as execution evidence.

Transcript: https://pre-fde-workbench.dingtalk.com/yufa/chat?session=6cf89e63-cc53-4ca6-b781-7877c1ca8193

Before the epoch fix, task `605ad253-4659-467d-9d5a-12616a5d6509` recalled the
old token after a successful reset acknowledgement and reused the old Session.
That failed test motivated the durable epoch fix.

Final acceptance:

1. Task `f8d8ee20-e389-4129-8cb9-125bf848d200` introduced the expected role and
   remembered 松风航标482 in `session-cce5bfd7-29ff-4578-a7ab-b0a70a7600b5`.
   DingTalk readback: `msgZcHE9yYrD7avlKxF6WkFDQ==` at 18:13:15.
2. Native DingTalk `/new` at 18:20:14 returned the reset acknowledgement:
   `msgLZ+6u6NOIqSPMtAhPP7RIg==` at 18:20:15.
3. Task `317c461c-bb76-4a01-81ac-60891b14747b` used new Session
   `session-5d77ba22-e249-4b8b-ab01-192163b0f0a6`, kept 星野澄/团子, and explicitly
   did not know the old token. DingTalk readback `msgBgRZ/g8Dh/4DdB9jm/I1Cg==`.
4. Follow-up task `8ef8dc28-067b-4dd3-bd29-72713d9282e4` retained that new Session
   and still did not know the token. Readback `msgtnXk0h53IvfTC+avhwp40g==`.
5. Explicit native old-session task `a5a5142d-7b94-4692-8e19-1634a5f42bde`
   successfully recalled 松风航标482 in the original Session, proving old history
   was preserved. The stale pre-deploy browser entry was reopened from the Agent
   configuration; the fresh native page displayed both sessions.
6. Subsequent DingTalk task `71a7a829-ff2e-4e73-a82f-e1f65ff4a80b` stayed in the
   new Session, retained the role, and still did not know the old token despite
   the later old-session answer in the visible transcript. Final readback
   `msgHmmplOtM7GBnFLaiNYDU7g==` at 18:25:34.

After all tasks completed, readback confirmed original runtime
`67819224-27b7-4202-aeac-0bc0fb8d71f8`, coordinator=true, and empty plugin
bindings restored. Test grants were revoked; the test browser tab was closed.
Historical transcripts and DSH Home data were preserved.

## Check coverage

Local focused Go tests, Go vet for dshhost/dshschedule, reset-history unit tests,
sqlc generation check, adapter/router tests, frontend schema/type checks, runtime
supervisor/gateway tests, and plugin tests passed. Coordinator checker reported
PASS_STRUCTURAL_ONLY, not semantic certification. The isolated real PostgreSQL
concurrency tests could not connect from the laptop or FC sandbox; their attempts
are not test passes. Real preproduction tasks above exercised the deployed schema,
epoch selection, old-session admission, and cross-session history exclusion.

## September 20 recurrence: old Runtime and unsaved native edits

The user's preproduction v25 Agent still used template `3rg3ebpz7rxmqe6ii5jn`
(runtime commit `94f5c3d`), while the managed restart fix existed only in the
`f1874cbb` candidate. Installing the Tavern fork did not update this binding.
Its native market restart at 03:16:58 UTC spawned an unmanaged replacement on
port 34001 while waiting for port 32921. The supervisor's control operation then
failed; profile reads returned `native_sync_pending` with `host_start_failed`.
Native revision 144 contained dshmarket 1.49.0 and @xmanrui/dsh-im 4.22.0, but the
saved source still contained only dshmarket 1.47.0 and the Tavern fork.

Temporary recovery switched this Agent to the existing candidate Runtime and
normal lifecycle replacement created generation 3. Revision 145 was applied,
but IM was absent because the native edits had not been imported. This is
recovery evidence only, not acceptance of the permanent fix. The native source
files were backed up in the employee Home before replacement.

The application now gates both native marketplace restart routes before proxying:
check the installed adapter supports managed restart, then persist the exact
browser Host's plugin snapshot. Unsupported images and synchronization failures
return an error without forwarding restart. This gate runs only on explicit
restart requests, not task admissions or normal native reads. The existing
Runtime supervisor still rejects active tasks/mutations and owns child restart.
The plugin editor now separates an unconfirmed receipt (edits remain locked)
from active application progress; failures no longer show an endless spinner.

Focused Go proxy tests cover old/unsupported Runtime, snapshot/save failure,
both restart endpoints, successful forwarding, invalid origin/method/query,
and ordinary read passthrough. Deployment and real preproduction regression
acceptance for this follow-up are pending.

The first follow-up deployment (CR 36224658, run 3109032489, two targets) passed
build, deployment and integration. Real regression on v25 proved both restart
routes reject the old `94f5c3d` template with HTTP 409 without changing its boot.
On the fixed Runtime, installing IM and upgrading dshmarket reproduced a second
failure: native snapshots were valid, but the managed resolver rejected IM's
8,595,641-byte `lib/index.js` against its 8 MiB per-member limit. The new restart
gate correctly refused to stop DSH when persistence failed.

The follow-up raises only the per-member bound to 16 MiB. Compressed downloads
remain capped at 32 MiB and total decompressed archives at 48 MiB. Tests cover a
9 MiB bundled entry, an over-16-MiB member, and aggregate expansion above the
unchanged total budget. Profile worker failures now distinguish native sync,
Host startup and other application errors instead of calling every failure a
Host startup failure. The complete plugin/restart/replacement acceptance remains
pending the second deployment.
