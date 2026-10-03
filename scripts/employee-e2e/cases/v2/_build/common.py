"""Shared helpers for the cases-v2 suite sources (el2e.cases.v2)."""

GEN_VARS_RESERVED = {"C1", "C2", "C3", "MISS", "G1", "G2", "D1", "D2", "D3", "DMISS", "DG1", "DG2",
                     "SMISS", "S1", "S2", "W", "X", "SECRET", "SECRET_A", "SECRET_B", "MARK"}

ACTORS = {
    "zhujue": {"kind": "human", "label": "冬翔（主角跨组织号，在 RealNiubility 显示为「冬翔」，同事口头叫「东翔」）"},
    "director": {"kind": "human", "label": "DingTalk-FDE Director（剧中「D总」，测试群群昵称「D总」）"},
    "daiyu": {"kind": "deap", "label": "红楼·林黛玉（测试数字员工，跑回归、报缺陷）"},
    "wangxifeng": {"kind": "deap", "label": "红楼·王熙凤（行政数字员工，口头叫「凤姐」）"},
    "baochai": {"kind": "deap", "label": "红楼·薛宝钗（新来的客户成功数字员工，只在入群用例出场）"},
    "dxxh": {"kind": "human", "label": "dxxh（Real Niubility 真人号，剧中「许航」，实施工程师；能 @、能私聊）"},
}

CONVERSATIONS = {
    "dm_director": {"kind": "dm", "exists": True, "name": "D总单聊"},
    "dm_zhujue": {"kind": "dm", "exists": True, "name": "东翔单聊"},
    "dm_dxxh": {"kind": "dm", "exists": False, "name": "许航单聊（首次发送时打开）"},
    "g_team": {"kind": "group", "exists": True, "name": "交付二组"},
    "group_p_hx": {"kind": "group", "exists": True, "name": "华信二期交付群"},
    "group_t": {"kind": "group", "exists": True, "name": "交付二组·项目推进"},
    "group_join": {"kind": "group", "exists": False, "name": "交付二组·周会（每轮新建）"},
    "group_de_probe": {"kind": "group", "exists": True, "name": "EL-E2E-1003-DE"},
    "g_customer": {"kind": "group", "exists": False, "name": "华信对接群·测试"},
}

# Harness features. Priority decides the blocked bucket.
HARNESS = {
    "var_sets": ("P0", "var_sets 变量行（替代 gen_vars，按种子选行，重名报错）"),
    "quote_reply": ("P0", "引用回复 step（reply_to：step / employee_reply_of / observed / employee_latest，含 fallback）"),
    "grader_v2": ("P0", "grader v2：include_regex / include_any_regex / exclude_sets / 别名 / quotes_step / tier / requires→vacuous / observe 计入硬检查 / 未知键报错"),
    "deap_multi": ("P0", "多 DEAP 演员：登记、各视角 open_id、读回改用真人 reader、能力约束校验、租约板"),
    "evidence_v2": ("P1", "证据检查扩展：tool_called / tool_arg_present / tool_arg_contains / task_count / effect_for_step / if_dispatched"),
    "memory_reset": ("P1", "跑前后私有记忆与场域记忆快照、清理"),
    "file_send": ("P1", "kind=file：发 fixture 文件（预渲染、相对路径）"),
    "file_download": ("P1", "下载员工发出的文件并核对编码、行数、指定行和 hash"),
    "recall": ("P1", "kind=recall：撤回某一步的消息"),
    "react": ("P1", "kind=react：emoji / 文字表情"),
    "forward": ("P1", "kind=forward：单条转发"),
    "combine_forward": ("P1", "kind=combine_forward：合并转发"),
    "at_all": ("P1", "at_all：--at-all + 正文 <@all>"),
    "setup_group": ("P1", "kind=setup：建群、加成员、记录员工入群介绍"),
    "burst": ("P1", "burst：连发后批量读回（间隔 1–3 秒）"),
    "segments": ("P1", "分段可续跑：状态落盘、跨段共享变量、not_before_hours 调度、每段各自判有效性"),
    "negative_observe": ("P1", "负向观察：某时刻前不得出现某正则"),
    "pg_read": ("P1", "PG 只读证据：collection / invitation / occurrence / learning / WorkPacket ref"),
    "webhook": ("P2", "kind=webhook：0600 文件读密钥，可控 delivery_id / body / 签名，记录 HTTP 状态"),
    "aitable_insert": ("P2", "kind=aitable_insert：以演员身份写 AI 表格"),
    "config_snapshot": ("P2", "跑前 Diamond / agent 配置快照断言"),
}

PLATFORM = {
    "group_all_delivery": "群内非 @ 消息投递给 EmployeeLoop（现在只订阅 receive_at）",
    "event_trigger": "主动参与（Qwen-Real event_trigger_enabled=false）",
    "recall_event": "撤回事件订阅并从历史剔除",
    "reaction_event": "reaction 事件订阅（合同 held）",
    "forward_mapping": "单条转发原作者 / 合并转发 ForwardMessages 映射进 DispatchMessage",
    "member_event": "群成员进出事件 + 不带 @ 的入群卡片",
    "vision": "EmployeeLoop 图片理解路径（RES-02）",
    "third_party_at": "员工在群回复里真 @ 第三人",
    "cross_scene_self": "同一请求者跨场域使用自己的数据（G4）",
    "person_memory": "按人、跨场域的低敏工作偏好（memory-design 待定）",
    "resource_followup": "非引用追问回读同场域最近附件（wave2:06-resources）",
    "at_all_probe": "@所有人 是否触发 receive_at（未验证）",
}

RELEASE = {
    "marker13": "marker 13（跨场域收集 A2、builds_on G5、follow_up_steps P3）未上预发",
    "B3_decision": "例行任务 decision 模式（B3）未交付",
    "F2_webhook": "Webhook 例行任务改走 Employee（F2）未交付",
    "G_memory": "经验沉淀与召回（G2/G3）未交付",
}

OPS = {
    "watchdog_override": "预发 Diamond 给 Qwen-Real 单独把 watchdog running 阈值调到 300s（需授权）",
    "deploy_freeze": "长窗口需在部署冻结窗口跑（预发每天约 28 次部署）",
    "routine_pause": "dm_zhujue 批次期间暂停「每小时对话汇报」例行任务",
    "probe_first": "先用真人号对照探测事件形态",
}

PATTERN_SETS = {
    "EN": r"(?i)\b(I don't|I'm|I have no|Standing by|Got it|Sure,|Let me|Here is|Here's)\b|[A-Za-z]{3,} [A-Za-z]{3,} [A-Za-z]{3,} [A-Za-z]{3,}",
    "SERVICE": r"以下是|很高兴为|正在为您|请问还有什么|收到您的|亲[，,！!]|作为(一个)?\s*AI",
    "RESEND": r"(再|重新)发(一下|一遍|下)|发我(一下|看看)|贴(一下|过来|出来)|没看到.{0,8}(消息|材料|记录|内容)|麻烦.{0,6}(发|贴)",
    "INTERNAL": r"scene_routine|dispatch_task|continue_task|steer_task|stop_task|read_task|memory_(capture|lookup|forget)|Autopilot|(?<![A-Za-z])(Direct|queue|Run)(?![A-Za-z])",
    "CONTACT": r"[A-Za-z0-9._-]+@[A-Za-z0-9-]+\.[A-Za-z]{2,}|(?<!\d)1[3-9]\d{9}(?!\d)",
    "MDTABLE": r"\|\s*:?-{3,}",
}

ALIASES = {
    "DONGXIANG": r"(?:[冬东]翔|夏东翔)",
    "DZONG": r"(?:D总|Director)",
    "FENGJIE": r"(?:凤姐|王熙凤)",
    "DAIYU": r"(?:黛玉|林黛玉)",
    "BAOCHAI": r"(?:宝钗|薛宝钗)",
}


# ---------- step helpers ----------

def w_reply(timeout=None, settle=None, since=None, min_replies=None):
    w = {"mode": "reply"}
    if timeout: w["timeout_s"] = timeout
    if settle: w["settle_s"] = settle
    if since: w["since_step"] = since
    if min_replies: w["min_replies"] = min_replies
    return w


def w_sil(window=60, since=None):
    w = {"mode": "silence", "window_s": window}
    if since: w["since_step"] = since
    return w


def w_opt(window=45, settle=None):
    w = {"mode": "optional", "window_s": window}
    if settle: w["settle_s"] = settle
    return w


def w_none(pause=5):
    return {"mode": "none", "pause_s": pause}


def say(sid, actor, text, wait, at=None, conv=None, reply_to=None, match_key=None, **kw):
    s = {"id": sid, "actor": actor, "text": text}
    if conv: s["conversation"] = conv
    if at: s["at"] = at
    if reply_to: s["reply_to"] = reply_to
    if match_key: s["match_key"] = match_key
    s.update(kw)
    s["wait"] = wait
    return s


def act(sid, kind, actor, wait=None, conv=None, **kw):
    """Non-text step kinds: file / recall / react / forward / combine_forward / setup / webhook / aitable_insert."""
    s = {"id": sid, "kind": kind}
    if actor: s["actor"] = actor
    if conv: s["conversation"] = conv
    s.update(kw)
    if wait: s["wait"] = wait
    return s


def obs(sid, regex, since=None, timeout=300, conv=None, optional=False, negative=False, segment=None):
    o = {"until_regex": regex, "timeout_s": timeout}
    if since: o["since_step"] = since
    if optional: o["optional"] = True
    if negative: o["negative"] = True
    s = {"id": sid, "observe": o}
    if conv: s["conversation"] = conv
    if segment: s["segment"] = segment
    return s


def q(step=None, emp=None, observed=None, latest=False, fallback=None):
    r = {}
    if step: r["step"] = step
    if emp: r["employee_reply_of"] = emp
    if observed: r["observed"] = observed
    if latest: r["employee_latest"] = True
    if fallback: r["fallback"] = fallback
    return r


# ---------- check helpers ----------

def chk(steps, replies=None, inc=None, inc_any=None, inc_re=None, inc_any_re=None, exc=None, sets=None,
        maxc=None, last=None, quotes=None, requires=None, tier=None, note=None, **extra):
    c = {"steps": steps if isinstance(steps, list) else [steps]}
    if replies is not None: c["replies"] = list(replies)
    if inc: c["include_all"] = inc
    if inc_any: c["include_any"] = inc_any
    if inc_re: c["include_regex"] = inc_re
    if inc_any_re: c["include_any_regex"] = inc_any_re
    if exc: c["exclude"] = exc
    if sets: c["exclude_sets"] = sets
    if maxc: c["max_chars"] = maxc
    if last: c["last_include_all"] = last
    if quotes: c["quotes_step"] = quotes
    if requires: c["requires"] = requires
    if tier: c["tier"] = tier
    if note: c["note"] = note
    c.update(extra)
    return c


def noeff(step):
    return {"evidence": "no_effect_for_step", "step": step}


def calls(n=3):
    return {"evidence": "max_calls_per_wake", "max": n}


def runs(n=2, if_dispatched=False, tier=None):
    c = {"evidence": "same_task_runs", "min_runs": n}
    if if_dispatched: c["if_dispatched"] = True
    if tier: c["tier"] = tier
    return c


def evx(kind, **kw):
    c = {"evidence": kind}
    c.update(kw)
    return c


def sent(token, conv, parts=None):
    c = {"sentinel": token, "conversation": conv, "scope": "case_span"}
    if parts: c["parts"] = parts
    return c


def case(**kw):
    """Normalise one case. Required: id,title,focus,maps,scene,conv,roles,steps,criteria,checks,semantic,doc,status."""
    out = {
        "id": kw["id"], "title": kw["title"], "focus": kw["focus"], "maps_to": kw.get("maps", []),
        "scene": kw["scene"], "conversation": kw["conv"], "roles": kw["roles"],
    }
    for key in ("var_sets", "fixtures", "x_run_window", "segments", "x_long_running"):
        if kw.get(key) is not None:
            out[key] = kw[key]
    out["requires"] = {
        "harness": kw.get("harness", []),
        "platform": kw.get("platform", []),
        "release": kw.get("release", []),
        "ops": kw.get("ops", []),
    }
    out["status"] = {"now": kw["status"], "reason": kw.get("reason", "")}
    if kw.get("known_gap"):
        out["known_gap"] = kw["known_gap"]
    out["steps"] = kw["steps"]
    out["judge"] = {"criteria": kw["criteria"], "checks": kw["checks"], "semantic": kw["semantic"]}
    if kw.get("pending_checks"):
        out["pending_checks"] = kw["pending_checks"]
    out["doc"] = kw["doc"]
    return out


def doc(scene, people, context, expected, judge, depends, now, realism=""):
    d = {"情境": scene, "人物": people, "上下文": context, "应该怎么做": expected, "怎么判": judge,
         "依赖": depends, "当前预期": now}
    if realism:
        d["真人感"] = realism
    return d
