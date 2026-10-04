# 预发分支归并到 feat/tag-multitenant

用户当前唯一目标：停止原验收推进，把预发关联分支归并到 feat/tag-multitenant；排除 codex/public-forwarding-ingress，解决语义冲突后部署预发。保留其他 session 未提交改动与 E2E 检查点，不新增产品修复、Runtime 镜像或正式发布。

范围来自 pipeline66 Run3110387322 release get：目标 c5c13339c6；evaluation-hub34dc79f7ea、backend-delivery11d6eb5061、progress-integratione5ef5adbea、progress-release36a18c03c2。被排除5ecfedf548不得进入目标分支。evaluation/progress-release历史已含该提交，因此受污染分支采用净变更归并，剔除公共入口改动；不能直接把 release 或污染分支完整祖先并入目标。已提交 Human880c 另列候选，本轮只按预发关联冻结范围，不自动扩大。

冲突保留目标 Direct/Tag 提示词及 GitHub 权限修复、Employee reader22/human1/eval1/一次性调度、原Webhook/quiet/进展/steer。共享字段按语义解析，不机械 ours/theirs。公共入口排除检查覆盖路由、nginx、middleware、handler、保留路径及启动白名单，既有连接器回调行为继续保留。

验收：各冻结来源净差分已归并；被排除提交不是新目标祖先且无其独立入口代码；受影响本地编译/定义/接缝测试通过。发布固定目标 SHA/CR/Run，构建扫描部署成功，两 live 新启动且 normal/marker符合。发布成功不冒称办公 E2E 通过，不关闭人工验收门。

流程：清洁隔离checkout→冻结预发分支与源SHA→语义合并及排除检查→受影响验证→回读远端目标→复用目标CR发一次pipeline66→核对实际release/live→执行表及报告。已有Run3110387322由另一交付者执行，部署资源空闲前不重发；本会话E2E heartbeat已暂停。
