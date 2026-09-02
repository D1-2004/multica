# FC Runtime 分支构建与候选沙箱切换闭环

> 状态：已完成
> 创建日期：2026-09-02
> 完成日期：2026-09-03
> 计划 ID：20260902-fc-runtime-dev-loop
> 基线 Commit：`develop@2a46c86eef665eaecfbd59fa2e49762574cac079`
> dt-fde-multica 分支：`codex/fc-runtime-dev-loop`
> Runtime 分支：`codex/fc-runtime-dev-loop-20260903@eacc27688d4f4184479bd67accfabeb0da242fb8`

## 一句话结论

把已经存在但分散在 Aone CI 和 Multica HTTP API 中的能力收敛为一个开发者 Skill：开发者提交并推送 Runtime 分支后，Skill 触发隔离的 FC 候选流水线，等待真实镜像、E2B Template 和 smoke test 全部成功，提取不可变 `template_id`，再用 Multica PAT 创建候选 Runtime 或原子切换现有候选 Runtime，最后读回验证。

## Why

当前两端其实都具备底层能力：

- `multica-fc-hermes-runtime` 的 Aone CI 可以按指定分支运行，候选流水线会生成 `candidate-<commit>` 镜像、E2B Template 并执行真实沙箱 smoke。
- `dt-fde-multica` 已有候选 Runtime 创建和模板切换 API。

缺口是没有一条安全、可复验的操作链把两端连接起来。人工操作容易混用正式流水线、从日志抄错别名而不是 Template ID、把 Token 放进命令历史、切换 stable-managed Runtime，或在 CI 尚未成功时提前切换。

## 目标

1. 在 `.agents/skills/` 提供可自动触发的开发者 Skill。
2. 用确定性脚本完成 CI 触发、终态等待、产物解析、API 创建/切换和读回验证。
3. 构建身份同时记录 Runtime commit、Multica ref、Aone run ID 和 E2B Template ID。
4. Token 只从环境或本机 Multica profile 读取，不进入参数、日志、仓库或结果文件。
5. 提供 dry-run、鉴权预检和 stubbed 端到端测试。

## 非目标

- 不改变 stable channel 或正式发布流程。
- 不临时修改正式 `master` 流水线的触发规则。
- 不在 FC 沙箱内安装本 Skill；它在开发机上编排构建和切换。
- 不把 ACR/E2B Secret 搬出 Aone Secret。
- 不自动提交开发者未提交的代码，也不替开发者猜测要推送的分支。

## 工作流

```text
Runtime 本地改动
  -> commit + push 独立分支
  -> Aone FC candidate pipeline（指定 runtime branch + multica_ref）
  -> SUCCESS + ACR digest + READY E2B template + real sandbox smoke
  -> 从结构化 step log 提取 template_id/runtime_commit/provider_fingerprint
  -> Multica PAT 鉴权和 stable-publisher 预检
  -> PATCH 现有 candidate Runtime 或 POST 新 candidate Runtime
  -> GET Runtime 列表读回 template_id
```

## 安全与失败语义

- 只有 CI 权威状态为 `SUCCESS` 才进入 API mutation。
- CI 返回的 commit 必须与 Template 日志中的 `runtime_commit` 一致。
- API mutation 前验证 Template 为 ready、provider 被声明、目标 Runtime 属于 `aliyun_fc` 且 channel 为 `candidate`。
- stable-managed Runtime 返回明确错误，不尝试绕过 stable release。
- API 失败不回滚已经发布的候选 Template；候选 Template 是可复用构建产物，脚本返回 run/template 身份供重试。
- 切换会让服务端废弃目标 Runtime 的热沙箱；下一次任务按新 Template 冷启动。

## 实施步骤

- [x] 调查 Aone CI、Runtime API、认证和权限合同。
- [x] 实现 Skill、脚本和工作流 reference。
- [x] 实现 dry-run、CI stub、HTTP stub 和完整闭环测试。
- [x] 在 Runtime 仓库增加独立分支候选 YAML，push 后由 Aone 自动创建 pipeline `295064`。
- [x] 用真实 Aone 候选流水线验证分支构建、ACR、E2B Template 和真实沙箱 smoke。
- [x] 用预发 PAT 创建候选 Runtime并读回验证。
- [x] 将状态更新为完成并记录实际 run/template/runtime 证据。

## 验收

- `python3 .agents/skills/fc-runtime-dev-loop/scripts/fc_runtime_dev.py doctor ...` 能验证本机依赖、Aone pipeline 与 Multica 权限。
- `build --dry-run` 不执行写操作且输出完整、无 Secret 的动作计划。
- `build` 只在 CI 成功并解析出 Template ID 后返回成功。
- `switch` / `create` 能读回并证明 Runtime 的 `metadata.template_id` 等于目标 Template ID。
- `cutover` 能一次完成 build + switch/create，且任何前置失败都会阻止后续 mutation。
- 单元测试覆盖 Secret 不出现在命令行/异常、CI 失败阻断、provider/channel 校验和读回验证。

## 实际闭环证据

### 1. 首次真实运行发现并阻断构建缺陷

- Pipeline：`277205`（既有 feature 候选流水线，仅用于启动时验证）
- Run：`68431025`
- Runtime commit：`52b615d1c0b6b84712f66364e62aa0afa1d0ec90`
- Multica ref：`2a46c86eef665eaecfbd59fa2e49762574cac079`
- 结果：`FAILED`
- 根因：Dockerfile 从 `registry.npmjs.org` 下载 128125014-byte Codex tarball，四次请求均在 300 秒超时，BuildKit 在第 32/59 层以 curl exit 28 失败。
- 安全结果：Skill 返回 `candidate Runtime mutation is blocked`，预发 Runtime 数量保持 55，没有提前写入。

### 2. Runtime 仓库修复与独立分支流水线

- 分支：`codex/fc-runtime-dev-loop-20260903`
- Commit：`eacc27688d4f4184479bd67accfabeb0da242fb8`
- 修复：`CODEX_NPM_REGISTRY` 从公网切到 `https://registry.npmmirror.com`。
- 制品预检：镜像源 7 秒下载完成，大小 128125014 bytes，SHA256 仍为 `9e4c4a25b88d9c93ce0ee4f64fba23fc55949f66d9ecb79e419c541b64b80997`。
- 分支流水线：新增 `.aoneci/runtime-fc-runtime-dev-loop-candidate.yaml`，只监听上述分支；push 后 Aone 自动创建 pipeline `295064` 并触发第一次 `PUSH` run。
- Runtime 仓库验证：10 个相关合同测试通过。

### 3. 镜像、Template 与沙箱 smoke 成功

- Pipeline：`295064`
- Run：`68449977`
- 触发方式：`PUSH`
- 权威终态：`SUCCESS`
- Runtime commit：`eacc27688d4f4184479bd67accfabeb0da242fb8`
- Multica ref：`2a46c86eef665eaecfbd59fa2e49762574cac079`
- E2B Template ID：`gzjr36hynucvy721p9qk`
- display alias：`multica-m7-va2eb67817f146ef4-r1-eacc27`
- provider fingerprint：`a2eb67817f146ef4`
- 流水线成功口径包含 ACR 发布、READY Template 和真实 E2B sandbox smoke。

### 4. Token + API 创建并读回候选 Runtime

- 预发 server：`https://pre-fde-workbench.dingtalk.com`
- Workspace：`浴发空间 / f26b4b03-7f10-4da7-8330-ce04d25fa513`
- 鉴权：为隔离的 `pre-fde` profile 创建 90 天 PAT；明文没有进入命令、对话、仓库或证据文件。
- `doctor`：pipeline ID/path/repo 校验通过，`can_publish_candidates=true`，Template catalog 可读。
- Runtime ID：`f069b307-2bc1-4ab5-92ff-7fdd5bcf2114`
- 名称：`AI FC Candidate 68449977`
- 读回：`runtime_mode=cloud`、`provider=hermes`、`status=online`、`visibility=private`、`sandbox_backend=aliyun_fc`、`artifact_channel=candidate`、`template_id=gzjr36hynucvy721p9qk`、`template_status=ready`、`runner_protocol=root-log-v1`。

### 5. Skill 验证

- `fc_runtime_dev.py` 单元测试：6/6 通过。
- Skill frontmatter：`quick_validate.py` 通过。
- `.skill` 打包：成功。
- 两轮 response eval：带 Skill 15/15；无 Skill 14/15。第二轮带 Skill 输出平均 4955 字符，无 Skill 9775 字符，减少约 49%。
- 静态 review：`.agents/skills/fc-runtime-dev-loop-workspace/fc-runtime-dev-loop-review.html`（评测工作区，不纳入产品提交）。

## 遗留项

- 新候选 Runtime 尚未绑定 Agent；本计划的完成边界是“真实镜像/Template smoke + Runtime 创建/读回”。首次 Agent 任务会按该 Template 冷启动，可作为后续业务验收。
- Runtime 仓库修复分支需要通过正常 Code Review 合入 `master`，正式发布仍遵循原 master 双流水线，不由本 Skill 执行。
