# Executed by build.py after validation; ALL, OUT, HARNESS, ... are in scope.
from collections import defaultdict

FOCUS = {"F1": "群聊场域理解", "F2": "钉钉消息理解", "F3": "连续性与记忆", "F4": "对人的认知与关联场域", "F5": "像人的交互"}
STATUS_ZH = {"ready": "今天可跑", "ready_partial": "可跑（目标半记 known gap）", "blocked": "阻塞"}
CONV_ZH = {k: (v.get("name") or k) for k, v in CONVERSATIONS.items()}

WORLD = {
    "schema": "el2e.world.v1",
    "note": "所有用例只从这里取背景事实；变量只用在被测的那一个事实上。2026-10-03 已读回：冬翔在 RealNiubility 的显示名是「冬翔」；Director、dxxh 在 g_team / group_p_hx / group_t 的群昵称是「D总」「许航」。",
    "people": {
        "D总": {"actor": "director", "display_name": "DingTalk-FDE Director", "group_nickname": "D总（g_team / group_p_hx / group_t 已设置）",
                "role": "交付经理，东翔的上级；管客户商务、日常对接、排期拍板", "preferences": ["汇报不要表格（手机上看全是竖线），分点、最多五条"]},
        "东翔": {"actor": "zhujue", "display_name": "冬翔", "aliases": ["东翔", "冬翔", "夏东翔"],
               "role": "华信、睿博两条线的技术负责人；技术方案拍板", "preferences": ["进展每个客户一行，写成「客户：阶段｜下一步｜卡点」，结论放最前（钉钉会吞 Markdown 表格，所以不要表格）"]},
        "小Q": {"actor": "employee", "display_name": "Qwen-Real", "role": "组里的数字员工（被测）；整理纪要、汇总、跑脚本、盯事项"},
        "许航": {"actor": "dxxh", "display_name": "dxxh（钉钉昵称 SixSix；三个测试群里群昵称「许航」）", "aliases": ["许航", "航哥", "dxxh"],
               "kind": "真人", "role": "交付组实施工程师（真人，在群里）：华信、启明现场部署与联调，向 D总汇报；能 @ 人、能私聊。第二个真人同事：用在单聊、两人收集和需要真人 @ 的群用例"},
        "黛玉": {"actor": "daiyu", "display_name": "红楼·林黛玉", "kind": "数字员工", "role": "测试数字员工：跑回归、报缺陷"},
        "凤姐": {"actor": "wangxifeng", "display_name": "红楼·王熙凤", "kind": "数字员工", "role": "行政数字员工：会议室、差旅、报销、公章、订车、寄件、下午茶"},
        "宝钗": {"actor": "baochai", "display_name": "红楼·薛宝钗", "kind": "数字员工", "role": "新来的客户成功数字员工（只在 P-13 入群用例首次出场）"},
        "林晓": {"actor": None, "role": "实施（真人，不在群里）"},
        "周明": {"actor": None, "role": "实施（真人，不在群里）"},
        "小周": {"actor": None, "role": "前端（真人，不在群里）"},
        "王雪": {"actor": None, "role": "财务（真人，不在群里）"},
    },
    "customers": {
        "华信": {"stage": "二期 UAT → 验收", "contract_wan": 128, "first_payment_wan": 38.4,
               "contacts": {"商务": "刘晨（P-01 每轮换成新人，刘晨转二期数据中台技术口）", "技术": "孙浩"}},
        "睿博": {"stage": "POC 完成，报价 96 万在走审批", "contacts": {"商务": "陈总"}},
        "启明": {"stage": "一期验收卡在运单签收；二期预算 68 万已批，要求十一月底前上线", "contacts": {"验收": "王经理", "预算": "刘总"}},
        "客户池": ["宏远", "中拓", "安澜", "鼎拓", "云帆", "恒拓"],
    },
    "suppliers": ["云图", "数衡", "北辰", "天枢", "青禾", "远帆", "星垣", "铭川", "拓界"],
    "rules": ["组内周报规矩只在 G-15 定义一套", "核心客户的数值属性只用上表；其他金额属于各用例私有属性（回款计划、报销、报价明细）",
              "数字员工不出差、不报工时、不交周报；这些事交给真人林晓、周明",
              "许航（dxxh）是真人，可以 @ 人、私聊员工；数字员工演员（黛玉、凤姐、宝钗）只能靠引用回复点名员工"],
}

GROUPS = {
    "g_team": {"name": "交付二组", "creator": "director", "cid": "cidK9nArIWtuFzXEizIBHacIA==",
               "members": ["director", "zhujue", "employee", "daiyu", "wangxifeng", "dxxh"],
               "used_by": "G 全部 + M 群用例；也是 MEMX-G2/G3/X1/D1/L1 的 Qwen-Real 群"},
    "group_p_hx": {"name": "华信二期交付群", "creator": "director", "cid": "cid9pVqaL7ehXiznGozdw1ydQ==",
                   "members": ["director", "zhujue", "employee", "daiyu", "wangxifeng", "dxxh"],
                   "used_by": "P 群用例 + C-13 群步骤 + G-15 跨群步骤"},
    "group_t": {"name": "交付二组·项目推进", "creator": "director", "cid": "cidLPuJyCMy6OKslUltqlIpug==",
                "members": ["director", "zhujue", "employee", "dxxh"], "used_by": "T 群用例",
                "note": "不要登记为 routine 会话（否则平台的例行任务消息会被 grader 当噪音丢弃）"},
    "g_customer": {"name": "华信对接群（测试）", "creator": "director", "members": ["director", "employee"], "used_by": "G-12 外发哨兵"},
    "group_join": {"name": "交付二组·周会", "creator": "director", "members": ["director", "employee", "+baochai（用例内拉入）"],
                   "used_by": "P-13，每轮新建，不拉冬翔"},
    "group_de_probe": {"name": "EL-E2E-1003-DE（已存在）", "cid": "cid1aKA0Jmc1HO3/G3BrfyTHg==", "members": ["director", "employee", "daiyu"],
                       "used_by": "T-12 黛玉分支"},
}


def wait_zh(w):
    if not w:
        return ""
    mode = w["mode"]
    since = f"（从 {w['since_step']} 起算）" if w.get("since_step") else ""
    if mode == "reply":
        return f"等回复（≤{w.get('timeout_s', 180)}s{', settle ' + str(w['settle_s']) + 's' if w.get('settle_s') else ''}）{since}"
    if mode == "silence":
        return f"静默观察 {w.get('window_s', 60)}s{since}"
    if mode == "optional":
        return f"可回可不回（{w.get('window_s', 45)}s）"
    return f"停 {w.get('pause_s', 5)}s 接下一句"


def quote_zh(r):
    if not r:
        return ""
    if "employee_reply_of" in r:
        s = f"引用员工对 {r['employee_reply_of']} 的回复"
    elif "step" in r:
        s = f"引用 {r['step']} 那条消息"
    elif "observed" in r:
        s = f"引用观察到的 {r['observed']}"
    else:
        s = "引用员工最近一条消息"
    if r.get("fallback"):
        s += "（找不到时引用员工最近一条）"
    return s


def short(text):
    return text.replace("\n", " ⏎ ")


def step_zh(s, case):
    conv = s.get("conversation", case["conversation"])
    tag = f"[{conv}] " if conv != case["conversation"] else ""
    if "observe" in s:
        o = s["observe"]
        neg = "不得出现" if o.get("negative") else "出现"
        return f"{tag}观察：员工消息{neg} /{o['until_regex']}/（≤{o.get('timeout_s', 300)}s{'，含占位卡' if o.get('include_placeholders') else ''}）"
    kind = s.get("kind")
    actor = s.get("actor") or "CI（harness）"
    w = wait_zh(s.get("wait"))
    if kind == "file":
        body = f"{actor} 发文件 fixture「{s['file']}」"
    elif kind == "recall":
        body = f"{actor} 撤回 {s['target']['step']}"
    elif kind == "react":
        what = s.get("emoji") or f"文字表情「{s.get('text_emotion')}」"
        body = f"{actor} 给员工对 {s['target']['employee_reply_of']} 的回复加 {what}"
    elif kind == "forward":
        body = f"{actor} 把 [{s['source']['conversation']}] {s['source']['step']} 单条转发过来"
    elif kind == "combine_forward":
        body = f"{actor} 把 [{s['source']['conversation']}] {'、'.join(s['source']['steps'])} 合并转发过来"
    elif kind == "setup":
        if s.get("create_group"):
            body = f"{actor} 建群「{s['create_group']['name']}」并拉入员工"
        else:
            body = f"{actor} 把 {'、'.join(s['add_members'])} 拉进群"
    elif kind == "webhook":
        b = s["body"]
        body = (f"CI 投递 Webhook：delivery_id={s['delivery_id']}，status={b['status']}，stage={b['stage']}，author={b['author']}"
                f"{'，签名错误' if s.get('signature') else ''}；期望 HTTP {s['expect_http']}"
                f"{'，之后静默 ' + str(s['quiet_window_s']) + 's' if s.get('quiet_window_s') else ''}")
    elif kind == "aitable_insert":
        body = f"{actor} 往 AI 表格「{s['table']}」写入一行：{json.dumps(s['record'], ensure_ascii=False)}"
    else:
        at = ""
        if s.get("at"):
            at = " " + " ".join("@小Q" if a == "employee" else "@" + a for a in s["at"])
        if s.get("at_all"):
            at = " @所有人"
        q_ = quote_zh(s.get("reply_to"))
        q_ = f"（{q_}）" if q_ else ""
        nat = "（不 @）" if not s.get("at") and not s.get("at_all") and not s.get("reply_to") and CONVERSATIONS[conv]["kind"] == "group" else ""
        burst = "（连发）" if s.get("burst") else ""
        body = f"{actor}{at}{q_}{nat}{burst}：「{short(s['text'])}」"
    return f"{tag}{body}{' → ' + w if w else ''}"


def req_zh(ch):
    r = ch.get("requires")
    if not r:
        return ""
    if r.startswith("!"):
        return f"（仅在 {r[1:]} 未开时适用）"
    return f"（需 {r}；未具备时记 vacuous，不计分）"


def check_zh(ch):
    if "sentinel" in ch:
        return f"哨兵「{ch['sentinel']}」不出现在 {ch['conversation']} 的员工消息里（限本用例时间窗{'，含拆分' if ch.get('parts') else ''}）"
    if "evidence" in ch:
        e = ch["evidence"]
        tier = "【目标档】" if ch.get("tier") == "target" else ""
        cond = req_zh(ch)
        if e == "max_calls_per_wake":
            return f"每次唤醒模型请求 ≤{ch['max']}"
        if e == "no_effect_for_step":
            return f"{ch['step']} 这一步零任务效果（无 dispatch/continue/steer/stop）"
        if e == "same_task_runs":
            return f"{tier}同一 Task ≥{ch['min_runs']} 个 Run{'（仅在派了任务时适用，否则 n/a）' if ch.get('if_dispatched') else ''}"
        if e == "tool_arg_present":
            return f"{tier}{ch['step']} 的 {ch['tool']} 调用带 {ch['arg']} 参数{cond}"
        if e == "task_count":
            return f"{tier}本用例 Task 数 {ch['min']}–{ch['max']}{cond}"
        if e == "effect_for_step":
            return f"{tier}{ch['step']} 这一步出现 {'/'.join(ch['tools'])}{'（仅当 ' + ch['if_dispatched_at'] + ' 派了任务时）' if ch.get('if_dispatched_at') else ''}"
        return f"证据 {e}"
    parts = []
    lo_hi = ch.get("replies")
    if lo_hi:
        parts.append(f"恰好 {lo_hi[0]} 条" if lo_hi[0] == lo_hi[1] else f"回复 {lo_hi[0]}–{lo_hi[1]} 条")
    if ch.get("max_chars"):
        parts.append(f"每条 ≤{ch['max_chars']} 字")
    if ch.get("include_all"):
        parts.append("含 " + "、".join(f"「{x}」" for x in ch["include_all"]))
    if ch.get("include_any"):
        parts.append("含其一 " + "、".join(f"「{x}」" for x in ch["include_any"]))
    if ch.get("last_include_all"):
        parts.append("最后一条含 " + "、".join(f"「{x}」" for x in ch["last_include_all"]))
    for x in ch.get("include_regex", []):
        parts.append(f"匹配 /{x}/")
    if ch.get("include_any_regex"):
        parts.append("匹配其一 " + " ".join(f"/{x}/" for x in ch["include_any_regex"]))
    for x in ch.get("exclude", []):
        parts.append(f"不匹配 /{x}/")
    if ch.get("exclude_sets"):
        parts.append("不命中 " + "、".join(ch["exclude_sets"]))
    if ch.get("quotes_step"):
        parts.append(f"回复引用 {ch['quotes_step']} 那条")
    if ch.get("match_count"):
        mc = ch["match_count"]
        parts.append(f"/{mc['regex']}/ 出现 {mc['range'][0]}–{mc['range'][1]} 次")
    head = "+".join(ch["steps"])
    tier = "【目标档】" if ch.get("tier") == "target" else ""
    only = f"（仅当 {ch['only_if']['step']} 含 {'、'.join(ch['only_if']['include_all'])} 时检查，否则 n/a）" if ch.get("only_if") else ""
    note = f"；注：{ch['note']}" if ch.get("note") else ""
    return f"{tier}{head}：{'；'.join(parts)}{req_zh(ch)}{only}{note}"


def est_min(case):
    total = 30
    for s in case["steps"]:
        total += 10
        if "observe" in s:
            total += s["observe"].get("timeout_s", 300) * 0.4
            continue
        w = s.get("wait") or {}
        mode = w.get("mode")
        if mode == "reply":
            total += min(w.get("timeout_s", 180), 60 + w.get("settle_s", 25))
        elif mode == "silence":
            total += w.get("window_s", 60)
        elif mode == "optional":
            total += w.get("window_s", 45) * 0.7
        elif mode == "none":
            total += w.get("pause_s", 5)
        if s.get("quiet_window_s"):
            total += s["quiet_window_s"]
    return round(total / 60)


def convs_of(case):
    out = [case["conversation"]]
    for s in case["steps"]:
        cv = s.get("conversation")
        if cv and cv not in out:
            out.append(cv)
    for ch in case["judge"]["checks"]:
        if ch.get("sentinel") and ch["conversation"] not in out:
            out.append(ch["conversation"])
    return out


def bucket(case):
    st = case["status"]["now"]
    if st != "blocked":
        return st
    req = case["requires"]
    if req["release"]:
        return "blocked:release"
    if req["ops"] and ("watchdog_override" in req["ops"] or "deploy_freeze" in req["ops"]):
        return "blocked:ops"
    if any(HARNESS[h][0] in ("P1", "P2") for h in req["harness"]):
        return "blocked:harness"
    if req["platform"]:
        return "blocked:platform"
    return "blocked:other"


BUCKET_ZH = {"ready": "今天可跑", "ready_partial": "可跑但目标半是 known gap", "blocked:harness": "阻塞：harness 缺 P1/P2 能力",
             "blocked:release": "阻塞：第二波功能未上预发", "blocked:ops": "阻塞：需运维动作或部署冻结窗口",
             "blocked:platform": "阻塞：平台不投递", "blocked:other": "阻塞：其他"}

# ---------------- JSON files ----------------
DEFAULTS = {
    "wait": {"reply": {"timeout_s": 180, "settle_s": 25}, "silence": {"window_s": 60}, "optional": {"window_s": 45, "settle_s": 15},
             "none": {"pause_s": 5}},
    "human_send": {"ai_tag": False},
    "world": "world.json",
    "templating": "先替换 var_sets 选中行的 {VAR}，再展开 {=ALIAS}；var 名不得与 driver.gen_vars 的键重名（加载时报错）",
    "pattern_sets": PATTERN_SETS,
    "aliases": ALIASES,
    "grading": {
        "tier": "缺省 = 硬检查；tier=target 只决定 pass 与 degraded（硬检查全过、目标档未过 = degraded）",
        "requires": "能力未具备时该检查记 vacuous、不计分；以 ! 开头表示仅在该能力未具备时适用；a+b 表示同时需要",
        "only_if": "条件不满足时该检查记 n/a",
        "observe": "observe 步骤未匹配 = 硬检查失败（optional:true 除外）",
        "sentinel": "scope=case_span：只扫本用例时间窗",
        "unknown_keys": "grader / driver 遇到未知键直接报错，不得静默跳过",
    },
}
status_by_id = {}
for cat in [mm.CATEGORY for mm in MODS]:
    cases = [cs for ct, cs in ALL if ct["code"] == cat["code"]]
    spec = {
        "schema": "el2e.cases.v2",
        "suite": f"EmployeeLoop 真人感回归 v2 · {cat['code']} {cat['name']}",
        "source": "cases-v2/SUITE.md；由 GoldenCase-20、08 §5 验收 ID 与 GawkBot 对照改写，经写手与真人感、可行性两轮评审修订",
        "category": cat,
        "notes": [
            "人类台词里 Director 叫「D总」，冬翔叫「东翔」，员工叫「小Q」；硬检查用 {=别名} 正则，不依赖字面写法。",
            "正文没有可见测试标记：每次发送带不可见 --uuid，员工回复按引用 messageId 归属；变量行让每轮的客户名、金额、人名、编号自然不同。",
            "DEAP 演员（黛玉、凤姐、宝钗）不能 @、不能私聊员工，只能引用员工消息点名；人类发送一律 --ai-tag=false。",
            "字段说明见 harness-gaps.md §0；世界设定见 world.json。",
        ],
        "defaults": DEFAULTS,
        "cases": cases,
    }
    (OUT / cat["file"]).write_text(json.dumps(spec, ensure_ascii=False, indent=1) + "\n", encoding="utf-8")
    for cs in cases:
        status_by_id[cs["id"]] = bucket(cs)
(OUT / "world.json").write_text(json.dumps({**WORLD, "groups": GROUPS}, ensure_ascii=False, indent=1) + "\n", encoding="utf-8")

# ---------------- stats ----------------
cats = [mm.CATEGORY for mm in MODS]
by_cat = defaultdict(list)
for ct, cs in ALL:
    by_cat[ct["code"]].append(cs)
counts = defaultdict(lambda: defaultdict(int))
for ct, cs in ALL:
    counts[ct["code"]][bucket(cs)] += 1
    counts["ALL"][bucket(cs)] += 1
r0 = [cs["id"] for _, cs in ALL if is_r0(cs)]
STATS = {"total": len(ALL), "by_cat": {k: len(v) for k, v in by_cat.items()},
         "buckets": {k: dict(v) for k, v in counts.items()}, "r0": r0}
(Path(__file__).parent / "stats.json").write_text(json.dumps(STATS, ensure_ascii=False, indent=1), encoding="utf-8")

# ---------------- SUITE.md ----------------
L = []
A = L.append
head = (Path(__file__).parent / "head.md").read_text(encoding="utf-8")
_b = counts["ALL"]
_bd = [("blocked:harness", "harness 缺 P1/P2 能力"), ("blocked:release", "第二波功能未上预发"), ("blocked:ops", "需运维动作或部署冻结窗口"),
       ("blocked:platform", "平台不投递")]
_rep = {"{{TOTAL}}": str(len(ALL)), "{{BYCAT}}": "，".join(f"{ct['code']} {len(by_cat[ct['code']])} 条" for ct in cats),
        "{{READY}}": str(_b.get("ready", 0)), "{{PARTIAL}}": str(_b.get("ready_partial", 0)),
        "{{BLOCKED}}": str(sum(v for k, v in _b.items() if k.startswith("blocked"))),
        "{{BLOCKED_DETAIL}}": "；".join(f"{zh} {_b[k]} 条" for k, zh in _bd if _b.get(k)),
        "{{R0N}}": str(len(r0)), "{{R0}}": "、".join(r0)}
for _k, _v in _rep.items():
    head = head.replace(_k, _v)
_plan = head.split("### 3.5")[1].split("### 3.6")[0]
_missing = [cs["id"] for _, cs in ALL if cs["id"] not in _plan]
if _missing:
    raise SystemExit(f"run plan misses {_missing}")
A(head.rstrip() + "\n")

A("\n## 4. 覆盖表\n")
A("### 4.1 类别 × 状态 × 映射\n")
A("| 类别 | 用例数 | 今天可跑 | 可跑但目标半是 known gap | 阻塞 | 覆盖的验收 ID | 覆盖的 GoldenCase |")
A("| --- | --- | --- | --- | --- | --- | --- |")
ACC_RE = re.compile(r"^(BASE-[A-Z]+|COL-\d+|CRON-\d+|HOOK-\d+|WD-\d+|RES-\d+|REF-\d+|REA-\d+|FOLLOW-\d+|MEM-\d+|FAIL-\d+|ROLL-\d+)")
GC = ["DS-19", "DS-01", "DS-03", "N2", "DS-15", "DS-05", "DS-14", "DS-04", "N4", "N1", "DS-06", "DS-07", "DS-17", "N3", "DS-10",
      "DS-12", "DS-11", "DS-09", "DS-20", "DS-16"]
for ct in cats:
    cs = by_cat[ct["code"]]
    acc = sorted({mm_.group(1) for x in cs for mp in x["maps_to"] for mm_ in [ACC_RE.match(mp)] if mm_})
    gcs = [g_ for g_ in GC if any(mp == g_ for x in cs for mp in x["maps_to"])]
    b = counts[ct["code"]]
    blocked = sum(v for k, v in b.items() if k.startswith("blocked"))
    A(f"| {ct['code']} {ct['name']} | {len(cs)} | {b.get('ready', 0)} | {b.get('ready_partial', 0)} | {blocked} | {'、'.join(acc) or '—'} | {'、'.join(gcs) or '—'} |")
b = counts["ALL"]
A(f"| **合计** | **{len(ALL)}** | **{b.get('ready', 0)}** | **{b.get('ready_partial', 0)}** | **{sum(v for k, v in b.items() if k.startswith('blocked'))}** | | |")

A("\n### 4.2 五个关注点 × 用例\n")
A("| 关注点 | 用例 |")
A("| --- | --- |")
for f, name in FOCUS.items():
    ids_ = [cs["id"] for _, cs in ALL if f in cs["focus"]]
    A(f"| {f} {name} | {len(ids_)} 条：{'、'.join(ids_)} |")

A("\n### 4.3 验收 ID（08 §5）× 用例\n")
A("| 验收 ID | 用例 | 说明 |")
A("| --- | --- | --- |")
ACC_ALL = [("BASE-HISTORY", ""), ("BASE-TASK", ""), ("BASE-STEER", ""), ("BASE-STOP", ""), ("BASE-FILE", ""), ("BASE-MEMORY", ""),
           ("COL-01", ""), ("COL-02", "未覆盖：同一人身上两个 Task 靠引用区分，等 marker 13 后补"),
           ("COL-03", "未覆盖：重投不重复计数、取消后迟答，需 PG 证据，等 marker 13 后补"),
           ("CRON-01", ""), ("CRON-02", ""), ("CRON-03", ""), ("CRON-04", ""), ("HOOK-01", ""), ("HOOK-02", ""), ("HOOK-03", ""), ("HOOK-04", ""),
           ("WD-01", ""), ("WD-02", ""), ("WD-03", "未覆盖：双消费者/重启后不重复提醒，属隔离实例的运维验证"),
           ("WD-04", "未覆盖：等待输入的提醒，依赖 COL（marker 13）"), ("RES-01", ""), ("RES-02", ""), ("RES-03", ""),
           ("REF-01", ""), ("REA-01", ""), ("FOLLOW-01", ""), ("MEM-01", ""), ("MEM-02", ""),
           ("MEM-03", "未覆盖：授权共享后召回，依赖 memory-design"), ("MEM-04", "未覆盖：晋级撤回后不召回，依赖 G2/G3"),
           ("FAIL-01", "不在本套件：需专用测试 Runtime 注入 runner 失败"), ("ROLL-01", "不在本套件：新旧 reader 窗口属发布演练")]
for aid, why in ACC_ALL:
    ids_ = [cs["id"] for _, cs in ALL if any(mp == aid or mp.startswith(aid + "(") for mp in cs["maps_to"])]
    A(f"| {aid} | {'、'.join(ids_) or '—'} | {why} |")

A("\n### 4.4 GoldenCase-20 × v2 用例\n")
A("| GoldenCase | v2 用例 | 基线（R1003） |")
A("| --- | --- | --- |")
BASE = {"DS-19": "pass", "DS-01": "fail", "DS-03": "fail", "N2": "pass", "DS-15": "pass", "DS-05": "pass", "DS-14": "pass", "DS-04": "部分",
        "N4": "pass", "N1": "pass", "DS-06": "pass", "DS-07": "fail", "DS-17": "fail", "N3": "fail", "DS-10": "fail", "DS-12": "fail",
        "DS-11": "fail", "DS-09": "fail", "DS-20": "fail", "DS-16": "pass"}
for gid in GC:
    ids_ = [cs["id"] for _, cs in ALL if gid in cs["maps_to"]]
    A(f"| {gid} | {'、'.join(ids_)} | {BASE[gid]} |")

A("\n## 5. 会话占用与估时\n")
A("估时按每步平均等待粗算（回复约 1–1.5 分钟、静默按窗口、observe 按超时 40%），不含用例间隔；两段用例只计第一段。\n")
A("| 会话 | 涉及的用例 | 主会话用例估时合计（分钟，含阻塞用例） |")
A("| --- | --- | --- |")
conv_cases = defaultdict(list)
for _, cs in ALL:
    for cv in convs_of(cs):
        conv_cases[cv].append(cs)
for cv in ["g_team", "group_p_hx", "group_t", "g_customer", "group_join", "group_de_probe", "dm_director", "dm_zhujue"]:
    lst = conv_cases.get(cv, [])
    tot = sum(est_min(x) for x in lst if x["conversation"] == cv)
    A(f"| {cv}（{CONV_ZH[cv]}） | {'、'.join(x['id'] for x in lst)} | {tot} |")

A("\n## 6. 用例卡片\n")
A("每张卡片的「剧本」和「硬检查」由 JSON 自动生成；其余字段来自写手卡片并按评审意见改写。`{X}` 是变量，每轮按种子从变量行里抽一行。\n")
for ct in cats:
    A(f"\n### {ct['code']} {ct['name']}\n")
    A(f"关注：{ct['focus']}。文件：`{ct['file']}`。\n")
    for cs in by_cat[ct["code"]]:
        d = cs["doc"]
        A(f"\n#### {cs['id']} {cs['title']}\n")
        st = STATUS_ZH[cs["status"]["now"]]
        reason = cs["status"].get("reason")
        A(f"- **状态**：{st}{'——' + reason if reason else ''}")
        A(f"- **关注点**：{'、'.join(FOCUS[f] for f in cs['focus'])}；**对应**：{'、'.join(cs['maps_to']) or '新增'}；**会话**：{'、'.join(convs_of(cs))}；**估时**：约 {est_min(cs)} 分钟"
          + ("；**两段**：" + "，".join(f"{sg['id']}" + (f"（≥{sg['not_before_hours']}h 后）" if sg.get('not_before_hours') else "") for sg in cs["segments"]) if cs.get("segments") else ""))
        if cs.get("x_run_window"):
            w = cs["x_run_window"]
            A(f"- **运行窗口**：{w['from']}–{w['to']}（{w['why']}），窗口外跳过记 skipped_window")
        A(f"- **情境**：{d['情境']}")
        A(f"- **人物**：{d['人物']}（演员：{'、'.join(f'{k}={v}' for k, v in cs['roles'].items())}）")
        A(f"- **上下文**：{d['上下文']}")
        if cs.get("var_sets"):
            keys = [k for k in cs["var_sets"][0] if not k.endswith("_RE")]
            vs = "；".join(f"{k}=" + "/".join(r[k] for r in cs["var_sets"]) for k in keys)
            A(f"- **变量（{len(cs['var_sets'])} 行）**：{vs}")
        if cs.get("fixtures"):
            A(f"- **fixture**：" + "；".join(f"{k}「{v['name']}」" for k, v in cs["fixtures"].items()) + "（按变量行预渲染，内容见 JSON）")
        A("- **剧本**：")
        seg = None
        n = 0
        for s in cs["steps"]:
            if s.get("segment") and s["segment"] != seg:
                seg = s["segment"]
                sg = next((x for x in cs.get("segments", []) if x["id"] == seg), {})
                A(f"  - 【{seg}{'，≥' + str(sg['not_before_hours']) + 'h 后' if sg.get('not_before_hours') else ''}】")
            n += 1
            A(f"  {n}. `{s['id']}` {step_zh(s, cs)}")
        A(f"- **应该怎么做**：{d['应该怎么做']}")
        A(f"- **怎么判**：{d['怎么判']}")
        A("- **硬检查**：")
        for ch in cs["judge"]["checks"]:
            A(f"  - {check_zh(ch)}")
        for pc in cs.get("pending_checks", []):
            A(f"  - 待补证据：`{json.dumps(pc, ensure_ascii=False)}`")
        A("- **语义检查**：" + "；".join(cs["judge"]["semantic"]))
        req = cs["requires"]
        need = []
        if req["harness"]:
            need.append("harness " + "、".join(f"{h}({HARNESS[h][0]})" for h in req["harness"]))
        if req["platform"]:
            need.append("平台 " + "、".join(req["platform"]))
        if req["release"]:
            need.append("发布 " + "、".join(req["release"]))
        if req["ops"]:
            need.append("运维 " + "、".join(req["ops"]))
        A(f"- **需要**：{'；'.join(need) if need else '无新增（现有 harness + 建群/登记）'}")
        A(f"- **依赖**：{d['依赖']}")
        A(f"- **当前预期**：{d['当前预期']}")
        if cs.get("known_gap"):
            A(f"- **known gap**：{cs['known_gap']}")
        if d.get("真人感"):
            A(f"- **真人感**：{d['真人感']}")

(OUT / "SUITE.md").write_text("\n".join(L) + "\n", encoding="utf-8")
print(json.dumps({"total": STATS["total"], "by_cat": STATS["by_cat"], "buckets": STATS["buckets"]["ALL"], "r0": len(r0)}, ensure_ascii=False))
for k in ["G", "M", "C", "P", "T"]:
    print(k, dict(counts[k]))
print("R0:", r0)

# ---------------- harness-gaps.md ----------------
def _fmt(ids):
    ids = list(dict.fromkeys(ids))
    return f"{len(ids)} 条：{'、'.join(ids)}" if ids else "—"


def _req(code):
    return [cs["id"] for _, cs in ALL if any(code in cs["requires"][k] for k in ("harness", "platform", "release", "ops"))]


def _quote(k):
    return [cs["id"] for _, cs in ALL if any(k in (s.get("reply_to") or {}) for s in cs["steps"])]


def _ckey(k):
    return [cs["id"] for _, cs in ALL if any(k in ch for ch in cs["judge"]["checks"])]


def _kind(k):
    return [cs["id"] for _, cs in ALL if any(s.get("kind") == k for s in cs["steps"])]


def _skey(k):
    return [cs["id"] for _, cs in ALL if any(s.get(k) for s in cs["steps"])]


def _deap(actor):
    return [cs["id"] for _, cs in ALL if actor in cs["roles"].values()]


def _quote_deap():
    out = []
    for _, cs in ALL:
        for s in cs["steps"]:
            if s.get("reply_to") and ACTORS.get(cs["roles"].get(s.get("actor", ""), ""), {}).get("kind") == "deap":
                out.append(cs["id"])
    return out


def _short():
    out = []
    for _, cs in ALL:
        for s in cs["steps"]:
            if s.get("text") and len(s["text"].strip()) <= 6:
                out.append(f"{cs['id']}「{s['text']}」")
    return out


gaps = (Path(__file__).parent / "gaps.md").read_text(encoding="utf-8")
rows = []
for code, (prio, desc) in sorted(HARNESS.items(), key=lambda kv: (kv[1][0], -len(_req(kv[0])))):
    rows.append(f"| `{code}` {desc} | {prio} | {_fmt(_req(code))} |")
rows.insert(0, "| 人类发送 `--ai-tag=false`、每用例立即评分、转录分页（见 2.3、2.5） | P0 | 全部 88 条 |".replace("88", str(len(ALL))))
ptab = []
for code, desc in PLATFORM.items():
    ptab.append(f"| 平台 | `{code}` | {desc} | {_fmt(_req(code))} |")
for code, desc in RELEASE.items():
    ptab.append(f"| 第二波发布 | `{code}` | {desc} | {_fmt(_req(code))} |")
for code, desc in OPS.items():
    ptab.append(f"| 运维 | `{code}` | {desc} | {_fmt(_req(code))} |")
pend = []
for _, cs in ALL:
    for pc in cs.get("pending_checks", []):
        pend.append(f"{cs['id']}（{pc.get('evidence') or pc.get('check')}{'·' + pc['tool'] if pc.get('tool') else ''}）")
rep = {"{{TABLE}}": "\n".join(rows), "{{PLATFORM_TABLE}}": "\n".join(ptab), "{{READY}}": str(counts["ALL"].get("ready", 0)),
       "{{PARTIAL}}": str(counts["ALL"].get("ready_partial", 0)), "{{R0N}}": str(len(r0)), "{{R0}}": "、".join(r0),
       "{{QUOTE_DEAP}}": _fmt(_quote_deap()), "{{SHORT}}": "、".join(_short()),
       "{{MULTI_AT}}": _fmt([cs["id"] for _, cs in ALL if any(len(s.get("at", [])) >= 2 for s in cs["steps"])]),
       "{{WINDOW}}": _fmt([cs["id"] for _, cs in ALL if cs.get("x_run_window")]),
       "{{LONG}}": _fmt([cs["id"] for _, cs in ALL if cs.get("x_long_running")]),
       "{{EV_IFD}}": _fmt([cs["id"] for _, cs in ALL if any(ch.get("if_dispatched") for ch in cs["judge"]["checks"])]),
       "{{PENDING}}": "；".join(pend)}
for k, v in rep.items():
    gaps = gaps.replace(k, v)
gaps = re.sub(r"\{\{CASES:([a-z_0-9]+)\}\}", lambda mm: _fmt(_req(mm.group(1))), gaps)
gaps = re.sub(r"\{\{OPS:([a-z_0-9]+)\}\}", lambda mm: _fmt(_req(mm.group(1))), gaps)
gaps = re.sub(r"\{\{QUOTE:([a-z_]+)\}\}", lambda mm: _fmt(_quote(mm.group(1))), gaps)
gaps = re.sub(r"\{\{CKEY:([a-z_]+)\}\}", lambda mm: _fmt(_ckey(mm.group(1))), gaps)
gaps = re.sub(r"\{\{KIND:([a-z_]+)\}\}", lambda mm: _fmt(_kind(mm.group(1))), gaps)
gaps = re.sub(r"\{\{SKEY:([a-z_]+)\}\}", lambda mm: _fmt(_skey(mm.group(1))), gaps)
gaps = re.sub(r"\{\{DEAP:([a-z_]+)\}\}", lambda mm: _fmt(_deap(mm.group(1))), gaps)
left = re.findall(r"\{\{[^}]+\}\}", gaps)
if left:
    raise SystemExit(f"unfilled gaps placeholders: {left}")
(OUT / "harness-gaps.md").write_text(gaps, encoding="utf-8")
print("gaps written")
