# Internal MCP 按域名接入

## 目标与 Why
用户授权：允许 dingtalk.com / alibaba-inc.com 下的内网 MCP；不按只读标记、部署归属或工具快照筛选，建分支并完成预发部署。

## 参考与边界
参考 MCP 2025-06-18 tools 规范：https://modelcontextprotocol.io/specification/2025-06-18/server/tools 。annotations 是可选提示；tools/list / tools/call 由上游提供实际工具与授权。沿用本仓固定上游和加密凭证架构，不引入新的代理服务。
域名白名单是接入边界。保留 HTTPS、工作区/Agent 授权、凭证加密、审计和资源上限；取消 readOnlyHint、工具数量及快照白名单门禁。长工具名只作可逆别名，不过滤。
Capability link 从任意允许域名提取 token 并加密保存；保存去掉 token 的地址，请求时恢复 capability 路径，不查本部署凭证库，也不依赖目标部署新增路由。

## 步骤
1. 先更新当前行为文档。
2. 修改创建、工具发现、调用、连通性检查和界面文案。
3. 用现有测试覆盖写工具、缺失注解、空工具列表、跨部署链接及动态工具，运行受影响 Go 检查。
4. 只提交当前 session 文件，更新预发域名范围为 dingtalk.com,alibaba-inc.com，提交 Aone CR 到流水线 66。
5. 等预发部署成功，核对版本/健康及真实 MCP 创建和工具发现。

## 结果
- 实现：接入/发现/调用/连通性不再按 readOnlyHint、部署归属、工具数或保存快照过滤。跨部署 capability 凭证加密，出站才恢复路径；补充 MCP-Protocol-Version。
- 验证：handler 受影响测试和 go vet 通过；router 两个真实数据库测试在隔离本地 schema 通过并清理；UI 两文件 13 个测试通过；git diff --check 通过。
- 测试价值：复用现有授权和凭证隔离测试；补一个表驱动远端凭证/空列表/70 个写工具回归，覆盖此次实际失败与凭证泄漏风险。
- 部署：等待流水线空闲后提交本分支 CR，预发域名配置按授权扩为两域名家族；部署结果待回填。

## 冲突合并范围更新
预发集成分支已有 Context Capabilities 和外部官方应用 OAuth/catalog 实现。解冲突保留场景/个人凭证解析、OAuth 刷新和外部 catalog 的写权限契约；本次域名接入规则作用于自定义内网 MCP。保留新界面/catalog 文案，仅更新本次工具提示。合并后的 relay 分开 raw discovery 和工具显示，不丢失官方应用的 session transport。

合并复核：全局和场景挂载中空工具快照只阻塞 catalog；内网 MCP 无该条件，允许上游后续新增工具。
