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
