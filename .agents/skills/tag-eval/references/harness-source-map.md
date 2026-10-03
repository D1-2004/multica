# Employee e2e harness source map

2026-10-04。仓库交付合同 `docs/employee-delivery-workflow.md` 优先于历史 eval 说明。此文档描述驱动与证据边界，不固定 runtime/revision/账号认证状态。

| 合同 / 能力 | 实现 | 生产事实 / 边界 |
| --- | --- | --- |
| capabilities、88条准入 | `scripts/employee-e2e/el2e/cases_v2.py`: load_capabilities / classify / dry_run；`cases/v2/capabilities.json` | harness 与 platform/release/ops 分开；后3类默认 false，不根据静态 authored_status 判通过 |
| memory_reset | `el2e/memory.py`: plan / snapshot / reset；`driver_v2.py`: run_case_v2 final cleanup | `/reset-memory` 的真实引用回复；Coordinator POST scene-memory reset 另记；私有存储与学习行无读API，不猜 |
| pg_read | `el2e/preapi.py` / `api_facts.py` | GET employee-tasks、任务detail/runs、scene routines/runs；场域目录 GET tenants/{org}/groups 支持 group+dm，offset/has_more；任务cursor按 updated_at+id。API失败和分页耗尽抛错，collect写error |
| 管理API权威 | `server/internal/handler/agent_tenants.go`: ListAgentTenantGroups；`employee_tasks_http.go`: ListEmployeeTasks / GetEmployeeTask / ListEmployeeTaskRuns | scene_id仅取服务端directory；tasks按scene和用例窗口过滤。聚合数量不证明逐人邀请与输入归属，completed不代替exit_confirmed |
| segments | `driver_v2.py`: segment_ids / run_case_v2 / renew_access；`grader_v2.py`: segment_validity | 共享变量与checkpoint、按段时间窗、not_before_hours；晚段缺席为in_progress，不签完整pass |
| evidence_v2 | `grader_v2.py`: evidence_check_v2 / pending_result / grade_case_v2；`grader.py`: evidence_summary；`evidence.py`: collect_case | LF以当前窗口openMsgId归属wake，后台task通过employee_job_id关联。原始trace存langfuse目录；摘要截断或API gap必须手动补原始证据；空列表不能证明零调用 |
| file_send / file_download | `driver_v2.py`: fixture_bytes / act_file / download_resource；`im.py`: send_file / locate_new | 每row匹配fixture；独立回读源文件落地；下载留真实bytes/hash/编码/行数。没有资源引用、没有文件、内容未核对不能业务pass |
| burst / negative_observe | `driver_v2.py`: burst / observe_v2；`im.py`: read_window；`grader_v2.py`: pending_result | 连续发完再按观察者senderId定位；重复落地不吞成一次。否定窗需分页完整、同一conversation，错误读取不能算静默 |
| quote / forward / combine_forward / at_all / setup_group | `driver_v2.py`: speak / act_forward / act_setup；`im.py`: send / reply | 当地dws CLI help核对命令参数；driver实现不证明Provider摄入或模型效果。setup仅记录已创建cid/请求结果，成员事件与实际投递另验 |
| 排除与缺口 | HARNESS_IMPLEMENTED / ACTIONS注册表；`api_facts.py`: API_GAPS | recall/react草稿保全但未注册；webhook/aitable_insert/config_snapshot未实现。T-07/T-10/T-11与M-10/M-13仍blocked_harness |
| 判定 | `grader_v2.py`: grade_case_v2 | pending_evidence→incomplete；partial/vacuous/pending_checks不能由judgement变完整pass；硬失败不可用语义分抵消。部署窗口缺证为partial scope |

所有driver动作仍需当轮授权及场域独占；本次收尾只做离线验证，未发IM或修改共享预发。默认具体名单与计数见 `scripts/employee-e2e/CLOSEOUT.md`，重新 dry-run 后将实际开关写入当波 manifest。
