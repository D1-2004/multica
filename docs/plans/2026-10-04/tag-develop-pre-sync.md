# 同步远端 develop 并解决预发冲突

用户要求：拉取远端最新代码，将远端develop合入feat/tag-multitenant，然后解决当前预发发布冲突。源码基线aone/feat/tag-multitenant@1938a059cd（已含Human首次询问7af）；develop@32feedf1bd，净来源为GitHub预发安装cookie回调25858298b5，4文件、无schema/Runtime镜像变化。其他session WIP和原测试现场保持。

验收：develop祖先进入目标，保留目标Outlook/AgentMail、GitHub权限、Employee累计reader22/human1/eval1及各来源绑定。GitHub cookie callback已有等价代码也必须按语义核对，禁止重复声明或机械ours/theirs。编译与受影响GitHub回调/权限测试通过；无新增办公业务测试，旧FAIL不升级。

流程：精确远端heads→清洁隔离checkout合并→冲突与净差分核对→定向验证→远端目标回读→读取当前预发Run/task sourceRevision/targetBranch→按固定release合同保留源码祖先解决冲突→继续同Run；若旧实例已CANCEL/不含新源码，只提交一次新的目标CR发布，明确记录。发布只到pipeline66，真实source/release/live启动/normal/health分别留证，人工验收门不自动关闭。

上轮四CR退出接口均exitCount1，但当前页面仍六项，新的退出实例3110388688已CANCEL；原退出结论未签收，不重复对旧快照执行。当前按用户新的develop/预发冲突目标先推进，退出清单的最终状态如仍未证另列限制。
