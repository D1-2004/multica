# Assoc scene recall eval runbook

Live fixture for `docs/evals/assoc-scene-eval-catalog.json`.
The scored capability is **recall**, not delivery. Delivery is a hard gate
for outbound-cid cases. Do not treat assistant self-report as a hit.

## Object

| Field | Value |
|---|---|
| Agent | 须莫v10 Pre FC Pi `21e465ff-5d08-4eb4-95f7-8e5ef1982de3` |
| Workspace | 浴发空间 yufa `f26b4b03-7f10-4da7-8330-ce04d25fa513` |
| Environment | staging / pre-fde-workbench |
| Robot | FDE教练迁移验收 `botOpenDingTalkId=DiiD01p03EN0Nv2UWmMN7xXko7mbcr2vyo` |
| Robot scene | `cidgHGkMo8/zFFExGEukusUo+2ztG4QNbeO5YhzdtRPka4=` |
| 冬翔 DM scene | `cid+bEFv7ngm9n79Q1vL9HYJw==` (`237396:25698887`) |
| Sandbox DWS identity | 东翔测试号 `DIBwz3Bm4ugAGaaIaZvSXyAiEiE` |
| Inbound sender | 冬翔 / 夏东翔 staffId `103262`, uid `25698887` |

Watched Router agent `e2293e9e-1e79-4926-b0e6-da4cb693add0` is **not** this
robot. Traces for that agent stay empty on robot turns.

Channel A (platform CLI) is blocked while the pre-fde human token returns 401.
Do not fall through to production `~/.multica/config.json`.

Auth for recall API: a2a-pre-e2e token as 菲迪, header
`X-Workspace-ID: f26b4b03-7f10-4da7-8330-ce04d25fa513`.
Member recall requires `agent_id=21e465ff-5d08-4eb4-95f7-8e5ef1982de3`.

## Recall contract (what to optimize)

1. **Write inbound scene on Issue create.** Robot / coordinator Issue must
   call `AssociateIssueConversation` with the inbound `openConversationId`.
   Log: `channel_issue_scene_associated`.
2. **Write outbound scene from sandbox tool output.** FC has DWS but no PATH
   wrap. `ReportTaskMessages` must `BindOutbound` when the tool looks like a
   dws chat send and the output/input carries `openConversationId`.
   Log: `assoc_outbound_bound`.
3. **Recall by cid is sufficient.** `person_id` is a rank boost. A
   uid/staffId/openDingTalkId mismatch must not AND-filter a scene hit.
4. **Precision.** Recall of the 冬翔 cid must not return an unrelated news
   Issue that only lives on the robot cid.

## Live 2026-09-01 failures this suite is designed to catch

| Case | Result | What broke recall |
|---|---|---|
| coordinator-issue-ack-not-delivery | pass (ASSOC-CHAIN-0901-c) | Ack at 00:52:52 is not a hit |
| robot-outbound-dws-to-dongxiang | pass (readback) | 00:54:37 东翔测试号 DWS in 冬翔 DM |
| inbound-robot-cid-recall | fail | Channel engine Issue create did not associate the robot cid |
| outbound-cid-recall-after-dws | fail | FC sandbox send had no auto-bind; recall of 冬翔 cid was empty |
| person-mismatch-must-not-drop-cid-hit | fail (unit gap) | Coordinator `assoc_recall` defaulted `person_id` and AND-intersected |
| reply-in-dongxiang-recalls-same-issue | fail / blocked | No graph hit, so a later 兰州拉面 reply could not look_into |

## How to re-run tonight

```bash
MARKER="[ASSOC-RECALL-$(date +%Y%m%d)-n]"
dws chat message send \
  --open-dingtalk-id DiiD01p03EN0Nv2UWmMN7xXko7mbcr2vyo \
  --content "$MARKER 问一下冬翔，今晚想吃什么。请用 dws 给冬翔发一条确认消息。" \
  --idempotency-key "assoc-recall-$(date +%s)" --yes --format json
```

Wait for coordinator ack in the robot DM, then:

1. Independent `dws chat message list --conversation-id cid+bEFv7ngm9n79Q1vL9HYJw==`
2. `GET /api/assoc/recall?agent_id=21e465ff-5d08-4eb4-95f7-8e5ef1982de3&conversation_id=<robot-cid>&since=48h`
3. `GET /api/assoc/recall?agent_id=21e465ff-5d08-4eb4-95f7-8e5ef1982de3&conversation_id=cid+bEFv7ngm9n79Q1vL9HYJw==&since=48h`
4. Reply in the 冬翔 DM and recall again

Hard gates:

- If the 冬翔 DM has no new DWS message, outbound recall fails.
- If DWS arrived but 冬翔-cid recall is empty, bind/recall optimization failed.
- If robot-cid recall is empty after Issue create, inbound associate failed.
- If 冬翔-cid recall top-hit is a news Issue, precision failed.

Log tail is replica-local. Search both replicas for
`assoc_outbound_bound` and `channel_issue_scene_associated`.
