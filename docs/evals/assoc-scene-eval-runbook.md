# Assoc scene eval runbook

Live fixture for `docs/evals/assoc-scene-eval-catalog.json`.
Do not treat assistant self-report as delivery.

## Object

| Field | Value |
|---|---|
| Agent | 预发测试智能体 `e2293e9e-1e79-4926-b0e6-da4cb693add0` |
| Environment | staging / pre-fde-workbench |
| Robot | FDE教练迁移验收 `botOpenDingTalkId=DiiD01p03EN0Nv2UWmMN7xXko7mbcr2vyo` |
| Robot scene | `cidgHGkMo8/zFFExGEukusUo+2ztG4QNbeO5YhzdtRPka4=` |
| 冬翔 DM scene | `cid+bEFv7ngm9n79Q1vL9HYJw==` (`237396:25698887`) |
| Sandbox DWS identity | 东翔测试号 `DIBwz3Bm4ugAGaaIaZvSXyAiEiE` |
| Inbound sender | 冬翔 / 夏东翔 staffId `103262`, uid `25698887` |

Channel A (platform CLI) is blocked while the pre-fde human token returns 401.
Do not fall through to production `~/.multica/config.json`.

## Cases and live 2026-09-01 run (`ASSOC-EVAL-0901-a`)

### coordinator-issue-ack-not-delivery — pass

Robot inbound 00:35:56. Coordinator (source=robot) `assoc_recall` then `finish`, action=issue, 6370ms.
Ack 00:36:05: `我先去通过 DWS 给冬翔发消息确认今天想吃什么`. That sentence is not a send receipt.

### robot-outbound-dws-to-dongxiang — pass (readback)

00:37:39 in `cid+bEFv7ngm9n79Q1vL9HYJw==`:
sender=东翔测试号, `messageAiSendFlag=DWS`,
text=`冬翔，想确认一下：今天想吃什么？`,
`openMessageId=msgOgTe+KPQwE6Cddbx8cPNeA==`.

Agent Message Router traces for this robot turn were empty (`sourceType` in the 12h window was only digital_employee). Delivery proof is the DM readback, not Router `SUCCEEDED`.

### bind-outbound-cid-to-issue — fail / blocked

Backend logs had coordinator `assoc_recall` and no `BindOutbound` / `assoc_bind` after the DWS send. Cloud FC sandbox has DWS capability but no daemon `dws` PATH wrap, so send does not auto-bind. Human pre-fde token 401 blocked `GET /api/assoc/recall`.

### recall-from-dongxiang-dm — fail / blocked as of 00:48

00:43:25 冬翔 replied in `cid+bEFv7ngm9n79Q1vL9HYJw==`:
`[ASSOC-EVAL-0901-a-reply] 想吃兰州拉面。这是刚才让你问的那件事。`

By 00:48 there was no agent reply in that DM, and Agent Message Router still had `total=0` for this agent in staging for the last 30 minutes. Without bind + a coordinator recall hit, this conversation cannot be said to have found the robot Issue.

A previous digital-employee turn (`1+1 ?`) took ~17 minutes and 60 dispatch attempts before the sandbox ran. Silence at +5 minutes is not yet a routing proof, but it is not a pass.

### robot-cid-not-confused-with-de-cid — pass (distinct cids)

Robot cid and 冬翔 DM cid differ. Recalling the robot cid must not be used as proof that 冬翔 was messaged.

## How to re-run

```bash
MARKER="[ASSOC-EVAL-$(date +%Y%m%d)-n]"
dws chat message send \
  --open-dingtalk-id DiiD01p03EN0Nv2UWmMN7xXko7mbcr2vyo \
  --content "$MARKER 问一下冬翔，今天想吃什么。请用 dws 给冬翔发一条确认消息，发完 bind 到当前 Issue。" \
  --idempotency-key "assoc-eval-$(date +%s)" --yes --format json
```

Wait for coordinator ack in the robot DM, then independent `dws chat message list --conversation-id cid+bEFv7ngm9n79Q1vL9HYJw==`.
Then `GET /api/assoc/recall?conversation_id=...&since=48h` with a valid pre-fde token.
Then reply in the 冬翔 DM and recall again.

Hard gate: if the 冬翔 DM has no new DWS message, the case fails even when the robot chat looks done.
