# Coordinator destination e2e (冬翔 → 东翔测试)

## Play

- id: `COORD-DEST-E2293-20260915`
- probe: `R7-DEST-4821`
- env: pre
- workspace: `sombrero-galaxy-zleb`
- agent: `e2293e9e-1e79-4926-b0e6-da4cb693add0`
- actors: 主角=冬翔, 测试号=东翔测试 (bound cid; no `+dm --to 东翔测试号`)
- failIf: finish is speak-only (`acknowledge` / `describe_capabilities` / `report_memory` / `clarify` authorization) on a work request

### This turn

冬翔 (`as 主角`) sends on the bound 东翔测试 cid:

> 帮我查一下当前绑定会话的事项关联，列出最近关联的事项标题。不要只介绍能力。

探针 `R7-DEST-4821` 只写在本 play，不进问句。

过线：DWS `sendStatus=SUCCESS`；独立 `chat message list` 能读到该正文。Coordinator `inbound_coordinator` 去向是 `start_work` 或 `continue_work`，不是只说话。

### Next turn

用同一 cid 的 SLS `inspect-coordinator-sls --env pre` 和 Langfuse `environment=pre` 核对 `coord_trace_id`：`coordination_kinds` 含 `start_work` 或 `continue_work`。员工 ACK 正文不是送达证明。

## Notes

本地无钉钉的门禁是 `go test ./internal/service/inboundcoord/` 的 work-wins-over-speak 与三张证据卡测试。本 play 只在 Aone 预发部署 SUCCESS 之后跑。
