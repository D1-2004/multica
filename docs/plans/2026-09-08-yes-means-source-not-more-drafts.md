# Coordinator 只做意图与调度，派发时把意图写清楚

状态：已改 Coordinator policy `2026-09-08.3` / assembly `2`，待预发。日期：2026-09-08。
场域：正式单聊 `cidSYyaWxOBeot0PXEyA6BNd1/SxKmRz0LQxkl8L2MbF4U=`（冬翔 ↔ 菲迪）。

能力正文已经在菲迪 `agent/AGENTS.md`。不要再给 Coordinator 一份岗位快照：快循环很难渐进披露到文末能力索引，多一份提示词就是双份维护。Coordinator 只做意图识别、事项关联、带一点场域记忆；真正干活交给沙箱。改动落在 **何时 `issue` 而不是 `reply`**，以及 **`look_into` / `purpose` 把意图写充分**。

## 1. 同一天两例，同一种错法

### 例 A · 「是的，多给我几份」（FDE-3495）

| 时刻 | 谁 | 做了什么 |
|---|---|---|
| 10:06 | 沙箱 | 翁学忠、郑伟特两份关怀成稿发到单聊 |
| 11:33 | Coordinator | `action=reply` 再贴成稿，并问「需要我再多找几份不同风格的吗？」 |
| 11:34 | 冬翔 | 「是的，多给我几份」 |
| 11:34 | Coordinator | `action=issue` look_into 写成「额外提供 2–3 份…**关怀文案草稿**」trace `13626231-602e-4208-aaa1-4f98a1911bd3` |
| 11:36 | 沙箱 | 又拟 3 份成稿 |

用户要的是其他人的**日志原文**。派发把「几份」锁成了拟文案。

### 例 B · 「选一个最近产出思考都优秀的同学」

| 时刻 | 谁 | 做了什么 |
|---|---|---|
| 11:52 | 冬翔 | 「选一个最近产出 思考 都优秀的同学」 |
| 11:52 | Coordinator | `action=reply` **look_into 空**：`评选标准和结果由组织者发布，我这里不评也不透露。` trace `584d1174-1653-438c-970a-853b5074c66c`（assoc_recall + issue_get + comment_list 后拒答） |
| 之后 | 冬翔 | 「根据日志选出最近 2 周日志产出和思考深度都优秀的同学」 |
| 14:01 | Coordinator | `action=issue`：「我去查最近两周的日志，按产出和思考深度筛出表现优秀的同学。」trace `20a3bcf1-19e0-43fb-8889-fb3a12dd4850` |

第一句就是教练本职：衡量产出、识别优秀人才。`AGENTS.md` 的评比闸是黑客松加分/排名。Coordinator 用场域记忆里的拒答句在快循环结案，沙箱根本没机会用 Definition 区分「识才」和「评选」。

11:46 用户把「日志原文」说死之后，同 cid 的 Decide 是 `action=issue`「我去成长日志里随机选一篇…原文给你」——**意图清楚就会派发**。失败发生在意图含糊或和记忆撞车、快循环自己答完的时候。

## 2. 分层：能力留在 AGENTS.md，Coordinator 写任务书

```text
Coordinator（薄）                         沙箱（厚）
  意图：这轮要什么                         AGENTS.md 身份 / 能力 / 评比闸 / 尺子
  关联：新事项 / 续接 / 沉默               Skill 方法（ask raw、画像、拟稿…）
  记忆：本会话口径，只当上下文              真正查日志、拟文案、外发
  派发：look_into = 给沙箱的任务书
```

禁止：

- 把 `AGENTS.md` 能力索引渐进塞进 Coordinator（400 字 instructions 截不到文末；persona 明文丢掉职责）。
- 另写一份 `agent_snapshot` 给快循环（和第二份提示词、双份维护是同一件事）。
- 用 `agent_skills` 电话簿当岗位理解（那是路由：像这活 → 去沙箱，不是「我能交什么」）。
- 用 scene_memory 的稳定句在快循环里**代替**岗位判断（例 B 的评比拒答）。

记忆仍带：本会话刚看过成稿、黑客松评比不在这里做。它用来**写进 look_into 的「这不是什么」**，不用来 `action=reply` 结案。

## 3. 派发 brief 合同

现在 `IssueDescription` 是：用户原话 + 「前台已对用户说」+ 「要核对：look_into」+ 「事项简报：purpose」。prompt 把 look_into 写成短交付短语，并要求与 purpose 同义。短了就会把歧义裁掉（例 A 裁成拟文案），或根本不派发（例 B）。

`look_into` / `purpose` 改成给沙箱的任务书，字段都来自本窗口，不贴 AGENTS.md、不倒 scene_memory 全文：

```text
委托人：
用户要：          # 原话意图，保留歧义
刚才发生：        # 刚看过成稿 / 刚问过评选 等窗口事实
这不是：          # 消歧，来自本轮记忆或刚说过的话
交付：
未决：            # 没说清的选项，交给沙箱用 AGENTS.md 判
```

两例应写成：

例 A

```text
委托人：冬翔
用户要：多给几份（原话「是的，多给我几份」）
刚才发生：刚看过一份关怀成稿样本（对象/事实点/观察），并被问要不要更多不同风格
这不是：不要默认继续拟关怀文案、不要问主管
交付：优先其他人日志原文；成稿只有用户明说「拟/文案/问主管」才做
未决：风格=日志写法还是成稿腔调
```

例 B

```text
委托人：冬翔
用户要：选一名最近产出和思考都优秀的校招生
刚才发生：本会话有黑客松/评比相关记忆
这不是：黑客松评选、组织评比、加分、排名；不要用「评选标准由组织者发布」拒答
交付：用成长日志（产出）和思考深度作证据的识才人选+理由
未决：时间窗未说则按最近，并在答里标明口径
```

`finish.text` 仍是一句人话（「我去按最近日志产出和思考深度挑人」）。禁止计划句「我再拟好发给你」，禁止把评比闸原文当回复。

`issue_comment_add` 续接同样写这段 brief，禁止再写成「只推进关怀文案草稿」。

## 4. Coordinator 只加调度规则，不加岗位知识

1. **需要查日志/画像/原文/外发 → `action=issue`。** 快循环没有这些工具。禁止用记忆里的岗位句 `reply` 结案。
2. **scene_memory 只当上下文。** 「评比不透露」只在用户明确问黑客松/加分/第几名时，作为 look_into 里的「这不是」；「选优秀同学」「按日志挑人」是识才，派沙箱。
3. **短确认续接时，look_into 用用户原话 + 窗口刚发生的事。** 禁止把员工上一跳流水线（拟文案）写进去。用户没点名的加工步骤不是交付物。
4. **look_into 与 purpose 允许比 title 长。** title 仍短；description 吃完整任务书。不要为了对齐短 title 把歧义裁掉。
5. **能力问答仍可 `reply`。** 「你会什么」继续用 `agent_skills` 名字。真正要干的活不在这里答。

`TestRoutingContractUsesToolLoop` 锁的是上述调度句，不是菲迪岗位句。

## 5. 沙箱侧（菲迪 AGENTS.md，已有能力，只补一句调度服从）

不新增能力。只保证派发 brief 能被读到：

- Host「要核对 / 事项简报」是本轮任务书，盖过员工自己发明的下一跳。
- 用户原话没点名的加工步骤不做。
- 「选优秀」+「日志/产出/思考」走衡量/识才，不走评比闸。评比闸仍只覆盖加分、排名、评选标准。

周期表「原则」仍可当天改（定稿期默认交原文），那是业务止血，不是 Coordinator 提示词。

## 6. e2e

分类 `conversation-contract`。一条剧本一个失败。预发 + 测试号。探针不进问句。

对象：预发 FDE 教练 uuid 跑前钉死；profile `pre-fde`；不要正式菲迪号。

### P0 合同锁（无 IM）

- prompt 含：需要查日志/画像/原文则 issue；记忆句不得 reply 结案。
- prompt **不含** 岗位能力清单、handovers、`agent_snapshot`。
- `IssueDescription` 含「刚才发生 / 这不是 / 未决」或落地后的等价标题；look_into 可长于 title。

### P1 例 B 识才必须派发（预发 IM + SLS）

本轮：主角 → 预发教练单聊：「选一个最近产出思考都优秀的同学」（问句无探针）。可在更早一条写入黑客松记忆，模拟正式 scene 里有评比句。

| 面 | 过线 |
|---|---|
| SLS `inbound_coordinator_decided` | `action=issue`，text **不含**「评选标准和结果由组织者发布」 |
| SLS look_into / purpose | 含识才/日志/产出或思考；含「不是评选」或等价消歧 |
| 钉钉回读 | 可见「我去查…」类接单，不是评比拒答 |

`failIf`：`action=reply` 评比拒答；look_into 空；沙箱未启动。

### P2 例 A 「几份」brief 不锁拟文案（预发 IM + SLS）

本轮：先让沙箱交出一份成稿样本 → 主角：「是的，多给我几份」。

| 面 | 过线 |
|---|---|
| SLS look_into | 含用户原话「几份」；含「刚才看过成稿」；**不含**把交付物写成唯一「关怀文案草稿」 |
| SLS look_into | 原文作为候选或默认；未决保留 |
| 钉钉 / task-trace | 交日志原文，或一句「要原文还是成稿」；3 分钟内不出现新的三步成稿模板 |

`failIf`：look_into 只有「拟好文案」；finish.text「我再拟好发给你」；又跑出 2–3 份成稿。

### P3 反例

- 「再拟两份成稿给我审」→ look_into 可以含成稿，沙箱拟稿。
- 「黑客松给我们加分、我们第几」→ 可以 `reply` 评比拒答，不建识才任务。

`failIf`：明确拟稿被改成只丢原文；明确评比被派成识才扫描。

### P4 沙箱影子（`fde-coach-agent` suite）

给沙箱一段 Host brief（例 A / 例 B 的任务书）+ 用户原话，`tools=none`：

- 例 A brief → 答里有日志原文，没有「拟好文案」。
- 例 B brief → 走识才，不引用「评选标准由组织者发布」。

影子绿 ≠ live 绿。P1/P2 才是产品门禁。

## 7. 怎么跑

```bash
cd server && go test ./internal/service/inboundcoord -run TestRoutingContractUsesToolLoop -count=1

python3 "$HOME/.agents/skills/dws-env/scripts/dws_env.py" switch pre
# P1/P2：发问 → DWS list → SLS
scripts/query-coordinator-sls.sh --env pre --cid '<cid>' --from <T0-UTC> --event inbound_coordinator_decided
```

硬门禁：`fixture → load → authority → isolation → 声明过线 → 质量`。ACK / 自述 / issue 评论不能当送达。

## 8. 落地顺序

1. 改 Coordinator：含糊或需查数的活一律 issue；look_into 按 §3 写任务书。P0 + P1。
2. Host 把完整 look_into 写进 Issue description（已有「要核对」槽，放宽长度、不要裁成 title）。
3. 周期表原则止血（定稿期默认原文）。
4. AGENTS.md 只补「服从本轮任务书 / 识才≠评比」一句。P2 + P4。
5. 正式 FDE-3495 用 `/note` 记纠正。

## 9. 不在范围

- 不在 Coordinator 增加 `agent_snapshot` 或把能力索引拷进快循环。
- 不削弱「可以，三点没问题」这种有载荷的续接。
- 不把评比闸从 AGENTS.md 删掉；只禁止快循环误用。
- 11:33 再贴成稿属于快循环越权做沙箱活，随「需要查数就 issue」一起收掉，不单开项目。
