# DSH managed restart and Tavern acceptance

Scope: supervised native marketplace restart, readable saved configuration during
native sync failure, DingTalk bare reset handoff, and Tavern draft insertion.

Evidence: production task 5117962c-ae25-4032-8d6b-5bf065dd43c8 reused
session-19c85307-3f2f-4e18-8ae3-87626fd03577 after a bare /new. The adapter
removed the directive without preserving CommandText, so Router could not arm
pending_fresh. Tests cover text/richText, leading mentions, and /reset.
No semantic Coordinator policy or intent rules change.

Production task 29865813-d476-4e66-905f-1cfc903408be contains the full role card
in its system/message event while its answer denies having a card. This is not
accepted as successful role behavior. Real fresh-session validation is pending.

Local adapter/router, Profile schema, supervisor/gateway and plugin tests passed.
Preproduction deployment and real native restart/role/reset acceptance: pending.

Preproduction native task 1c7557ca-72aa-432f-a3de-4ed1b65c4d65 completed and
introduced the configured lighthouse observer 星野澄 and her cat 团子. This proves
fresh native role behavior for the imported 2.3.9-multica.3 fork.
Application CR 36211542 deployed successfully in run 3108891954; integration
stage passed. Runtime f1874cbb CI 73671213 passed and produced candidate template
u7rcxpnirvu7csz1i2m7. Isolated runtime 387d4e89-a0a3-486d-a04b-fd1349b24c0f
is bound only to pre test Agent 616590ea-be68-4432-a7f7-e6cd79605bec.

Additional reset defect: cloud claims rebuild context from the persisted transcript
after clearing the provider session. A task-input reset boundary now limits that
model-only history on the reset turn and following turns. The visible transcript
remains intact. Immutable input ownership bounds the query so future queued resets
cannot affect earlier turns. No schema migration is needed. The query is generated
with the isolated sqlc generator because the existing full generator has a known
migration-order/compatibility issue. Reset-boundary unit tests passed; real channel
reset and candidate restart validation remain pending.


Native restart acceptance: boot 41-1789723257420 -> 349-1789723345450 on
sandbox sbx-3ef7a97d-346d-4628-a5c4-3fcf582baa13. Same entry recovered;
profile revision 142 remained applied/current. During task
72a54f8e-adba-40ee-9c60-f7e0e9dcc6fd, restart returned HTTP 409 and the role
answer completed normally. Task 4fdcbb8b-8e09-4c91-8e4e-b430d55f4b57 also
passed role evaluation on the candidate runtime.

Default preset distinction: plain workbench task 83905af2-fd8c-406d-9b2a-732a2d198002
used the pre Agent's standard preset and answered as the test Agent. Updating the
native agent-presets setting default to tavern-lite made the next ordinary chat
task 61dd554b-7f8c-437e-8d21-33c2983542de correctly introduce 星野澄 and 团子.
This setting applies to newly created sessions and does not migrate old sessions.

Reset-boundary follow-up deploy: run 3108895617, commit 058ed3400.
Browser click acceptance is blocked by the IAB provider (nodeRepl.fetch request
failed; browser inventory unavailable). Unit coverage confirms draft service
insertion and readback; do not label it browser E2E. A preproduction DSH bot/chat
target is still needed for the actual DingTalk /new send/receive acceptance.

Run 3108895617 completed deployment and integration successfully; it is at the
normal manual preproduction verification gate. Post-deploy ordinary chat task
5cd5caf7-2f67-469b-9411-78f026f3f363 completed with the correct cat identity,
exercising persisted history and the new reset-boundary query with no reset.

User requested reuse of an already-bound preproduction bot. Existing DSH Agents
v21 and v25 have active DWS execution identities but message_route=unbound and
no robot installation. Existing v7 Pi Agent 167f831a-73cb-4087-a86a-d1cbe4c08145
has installation b0aaa072-b462-42ef-bce5-99906a6daa82, robotCode
dingzvwprcls6j6p4ofi. Official production developer-platform readback confirms
that robot's name is 须莫v7_Pre_Pi_钉钉组织 and mode STREAM/ONLINE. Its backend
binding is preproduction only. Test IM delivery succeeded, but no corresponding
preproduction task has been observed yet. This is not reset acceptance.
