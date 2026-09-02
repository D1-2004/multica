# 跑次记录

剧本正文（谁发给谁、说什么、查什么）只在 `../SKILL.md`。这里只记命令备忘和每次预发结果。

## 命令备忘

```bash
DWS='python3 $HOME/.agents/skills/dws-env/scripts/dws_env.py'
$DWS status   # 必须 environment=pre

# 主角 → 测试号单聊
$DWS as 主角 -- chat +messages-send --as user \
  --chat-id 'cid+bEFv7ngm9n79Q1vL9HYJw==' --text '你好' --yes --format json

# 配角 → 测试号（同组织可用姓名）
$DWS as 配角 -- chat +dm --to 东翔测试 --content 'ping' --yes --format json

# 查 Coordinator
scripts/query-coordinator-sls.sh --env pre --cid 'cid+bEFv7ngm9n79Q1vL9HYJw=='
```

## 最近一次预发记录

新结果插到表顶。不要改基线行结论。

| 日期 | 剧本 | 谁→谁 | cid / coord_trace_id | 结论 |
|---|---|---|---|---|
| 2026-09-02 | P1.1 | 主角→测试号单聊 | `7bd5ee60-4e74-43de-a889-d2137a9f9e96` | 通过，reply 无 issue |
| 2026-09-02 | P1.2 | 主角→测试号单聊 | `b4db44f2-9db0-4bc0-801c-db112dff3ec2` | **失败**，纠正进 WS-31 |
| 2026-09-02 | P1.4 | 主角→测试号单聊 | `49133eb5-7aaf-41f1-8e09-d4fb6eb2aaa3` | 通过，`action=issue` 空 issue_id |
| 2026-09-02 | P2 | 主角→测试号单聊 | `7f5655d7-a806-4dd6-8589-7e0e60b1a139` | 通过，新 bind |
