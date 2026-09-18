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
