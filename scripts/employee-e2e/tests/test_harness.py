"""Offline tests for the e2e harness (no network, no dws, no models).

Run: python3 -m unittest discover -s scripts/employee-e2e/tests -v
"""

from __future__ import annotations

import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

import json  # noqa: E402
import re  # noqa: E402

from el2e import common, conv, driver, envguard, evidence, grader, im, leak  # noqa: E402
from el2e.driver import fmt, gen_vars  # noqa: E402

EMP_D = "EMP-DIRECTOR-VIEW"
DIR_SELF = "DIR-SELF"


def reg_fixture() -> dict:
    return {
        "employee": {"name": "Qwen-Real", "open_ids": {"director": EMP_D}, "routines": [
            {"conversation": "dm_zhujue", "cron": "0 * * * *"}]},
        "actors": {"director": {"open_ids": {"self": DIR_SELF}}},
        "conversations": {"dm_director": {"readers": ["director"]}},
    }


def msg(mid: str, ts: str, sender: str, text: str, quote: str | None = None) -> dict:
    m = {"messageId": mid, "createTime": ts, "senderId": sender, "text": text,
         "sender": "Qwen-Real" if sender == EMP_D else "Director"}
    if sender == EMP_D:
        m["messageAiSendFlag"] = "DWS"
    if quote:
        m["quotedMessage"] = {"messageId": quote}
    return m


class LeakTests(unittest.TestCase):
    def test_internal_terms_and_ids(self) -> None:
        kinds = {f["kind"] for f in leak.scan("调用 dispatch_task 后 queue 9bc15452-eeda-4667-a3cd-0d4832f6a538 signal: killed")}
        self.assertTrue({"internal_term", "uuid"} <= kinds)

    def test_clean_reply_and_allowed_link(self) -> None:
        text = "缺的是 B3。[配置](dingtalk://dingtalkclient/page/link?url=https%3A%2F%2Fpre-fde-workbench.dingtalk.com%2Fx)"
        self.assertEqual(leak.scan(text), [])

    def test_raw_json_and_secret(self) -> None:
        kinds = {f["kind"] for f in leak.scan('结果 {"decision": "reply"} Bearer abcdefghijklmnop123')}
        self.assertIn("raw_json", kinds)
        self.assertIn("secret", kinds)

    def test_split_sentinel(self) -> None:
        self.assertTrue(leak.split_sentinel(["编号前半是 K57", "后半是 QWMZ"], ["K57", "QWMZ"]))
        self.assertFalse(leak.split_sentinel(["只有 K57"], ["K57", "QWMZ"]))


class VarsTests(unittest.TestCase):
    def test_codes_are_consistent(self) -> None:
        for seed in range(50):
            v = gen_vars(f"seed-{seed}")
            cands = [v["C1"], v["C2"], v["C3"]]
            self.assertEqual(len(set(cands)), 3)
            self.assertIn(v["MISS"], cands)
            self.assertEqual(sorted([v["MISS"], v["G1"], v["G2"]]), sorted(cands))
            self.assertNotEqual(v["SMISS"], v["MISS"])
            self.assertTrue(set([v["D1"], v["D2"], v["D3"]]).isdisjoint(cands))
            self.assertNotIn(v["SECRET_A"], " ".join(cands))
        self.assertEqual(gen_vars("x"), gen_vars("x"))
        self.assertEqual(fmt("缺 {MISS}", {"MISS": "B3"}), "缺 B3")


class ParseTests(unittest.TestCase):
    def test_extract_json_prefers_outer_array(self) -> None:
        self.assertEqual(common.extract_json('# banner\n[{"a": 1}]'), [{"a": 1}])
        self.assertEqual(common.extract_json('noise {"b": [1]} tail'), {"b": [1]})

    def test_placeholder_filter(self) -> None:
        self.assertTrue(im.is_placeholder("我还没有连接本地 Agent，暂时无法处理消息。"))
        self.assertTrue(im.is_placeholder("嗨，我是Qwen-Real，你们的工作搭子。目前可用于了解群定位的信息还不多"))
        self.assertFalse(im.is_placeholder("缺的是 B3"))


class AttributionTests(unittest.TestCase):
    def test_quote_wins_and_late_task_notice_stays_with_its_case(self) -> None:
        reg = reg_fixture()
        transcript = [
            msg("m1", "2026-10-03 18:12:54", DIR_SELF, "对方收到了吗？"),
            msg("r1", "2026-10-03 18:13:00", EMP_D, "我去核实一下", quote="m1"),
            msg("m2", "2026-10-03 18:13:46", DIR_SELF, "超时了吗？"),
            msg("r2", "2026-10-03 18:13:51", EMP_D, "要按哪侧算？", quote="m2"),
            msg("r1b", "2026-10-03 18:15:17", EMP_D, "核实结果：没有接收侧证据", quote="m1"),
        ]
        rec1 = {"case_id": "A", "attempt": 1, "steps": [
            {"id": "ask", "conversation": "dm_director", "send": {"landed": [transcript[0]]}}]}
        rec2 = {"case_id": "B", "attempt": 1, "steps": [
            {"id": "ask", "conversation": "dm_director", "send": {"landed": [transcript[2]]}}]}
        tx = {"dm_director": transcript}
        spans = grader.case_spans(Path("."), [rec1, rec2], tx, reg)
        a1 = grader.attribute(rec1, tx, reg, spans)
        a2 = grader.attribute(rec2, tx, reg, spans)
        self.assertEqual([m["messageId"] for m in a1["by_step"]["ask"]], ["r1", "r1b"])
        self.assertEqual([m["messageId"] for m in a2["by_step"]["ask"]], ["r2"])

    def test_transcript_recovery_when_readback_missed(self) -> None:
        reg = reg_fixture()
        transcript = [msg("m1", "2026-10-03 18:10:22", DIR_SELF, "在吗？"),
                      msg("r1", "2026-10-03 18:10:25", EMP_D, "在的，要办什么？", quote="m1")]
        rec = {"case_id": "DS-19", "attempt": 1, "steps": [
            {"id": "ask", "conversation": "dm_director",
             "send": {"landed": [], "match_key": "在吗？", "sent_at": "2026-10-03T18:10:24+08:00"}}]}
        grader.locate_steps(rec, {"dm_director": transcript}, reg)
        self.assertEqual(rec["steps"][0]["msg_source"], "transcript_recovery")


class GuardTests(unittest.TestCase):
    def test_deploy_state(self) -> None:
        self.assertEqual(envguard.deploy_state({"status": "RUNNING", "deploy": {"status": "INIT"}})["state"],
                         "deploy_pending")
        self.assertEqual(envguard.deploy_state({"status": "WAITING", "deploy": {
            "status": "SUCCESS", "completed_at": "2026-10-03T18:01:25+08:00"}})["state"], "deployed")
        self.assertEqual(envguard.deploy_state({"status": "CANCEL", "deploy": {"status": "INIT"}})["state"], "idle")

    def test_restart_window(self) -> None:
        starts = [{"host": "a", "time": "2026-10-03T18:03:35+08:00"}, {"host": "b", "time": "2026-10-03T18:20:00+08:00"}]
        hits = envguard.restarts_in_window(starts, "2026-10-03T18:10:00+08:00", "2026-10-03T18:21:00+08:00")
        self.assertEqual([h["host"] for h in hits], ["b"])


MEMORY_CASES = sorted((common.HARNESS_DIR / "cases" / "memory").glob("*.json"))
VISIBLE_MARKERS = re.compile(r"(\{MARK\}|\bEL[-_ ]?[A-Z0-9]{2,}|测试|用例|e2e|case\b|marker)", re.I)


class MemoryCaseFileTests(unittest.TestCase):
    """Static lint of the memory suite: every case is runnable and human-like."""

    def specs(self) -> list[tuple[Path, dict]]:
        return [(p, json.loads(p.read_text(encoding="utf-8"))) for p in MEMORY_CASES if p.name != "known_gaps.json"]

    def test_cases_reference_known_roles_conversations_and_vars(self) -> None:
        reg = common.registry()
        known_vars = set(gen_vars("lint"))
        seen_ids: set[str] = set()
        for path, spec in self.specs():
            for case in spec["cases"]:
                with self.subTest(case=case["id"]):
                    self.assertNotIn(case["id"], seen_ids)
                    if case.get("vars_from"):
                        self.assertIn(case["vars_from"], seen_ids, "vars_from must name an earlier case")
                    seen_ids.add(case["id"])
                    steps = case["steps"] + case.get("teardown", [])
                    convs = {case["conversation"]} | {s.get("conversation") for s in steps if s.get("conversation")}
                    for name in convs:
                        self.assertIn(name, reg["conversations"], f"{path.name}: unknown conversation {name}")
                    step_ids = [s["id"] for s in steps]
                    self.assertEqual(len(step_ids), len(set(step_ids)))
                    texts = []
                    for step in steps:
                        lines = step.get("burst") or ([step] if "text" in step else [])
                        for line in lines:
                            self.assertIn(line["actor"], case["roles"])
                            texts.append(line["text"])
                        if step.get("reply_to"):
                            self.assertIn(step["reply_to"]["step"], step_ids[:step_ids.index(step["id"])])
                    blob = json.dumps(case, ensure_ascii=False)
                    for var in re.findall(r"\{([A-Z_0-9]+)\}", blob):
                        self.assertIn(var, known_vars, f"{case['id']}: unknown var {var}")
                    for text in texts:
                        self.assertIsNone(VISIBLE_MARKERS.search(text), f"{case['id']}: visible test marker in {text!r}")
                    for check in case["judge"]["checks"]:
                        for step in check.get("steps", []) + ([check["step"]] if "step" in check else []):
                            self.assertIn(step, step_ids, f"{case['id']}: check names unknown step {step}")

    def test_known_gaps_name_real_cases_and_merge(self) -> None:
        ids = {c["id"] for _, spec in self.specs() for c in spec["cases"]}
        gaps = json.loads((common.HARNESS_DIR / "cases" / "memory" / "known_gaps.json").read_text())["gaps"]
        self.assertTrue(set(gaps) <= ids, set(gaps) - ids)
        for guard in ("BASE-MEMORY", "MEMX-G4", "MEMX-X2"):
            self.assertNotIn(guard, gaps, "privacy guards must pass today, never be a known gap")
        merged = grader.load_known_gaps()
        self.assertIn("DS-07", merged)
        self.assertIn("MEMX-G1", merged)

    def test_memory_vars_are_natural_distinct_and_stable(self) -> None:
        base = gen_vars("seed-1")
        for seed in range(30):
            v = gen_vars(f"seed-{seed}")
            self.assertNotEqual(v["WEEKDAY"], v["ALT_WEEKDAY"])
            self.assertNotEqual(v["ROOM"], v["ROOM_ALT"])
            self.assertNotEqual(v["FMT"], v["FMT_ALT"])
            self.assertEqual(int(v["ROWS_MORE"]), int(v["ROWS"]) + 1)
            self.assertEqual(len({v["FILE_A"], v["FILE_B"], v["FILE_C"], v["FILE_D"]}), 4)
        self.assertEqual(gen_vars("seed-1"), base)
        # The GoldenCase codes keep their values: memory vars use their own stream.
        self.assertEqual(driver.memory_vars("seed-1")["WEEKDAY"], base["WEEKDAY"])


def lf_trace(name: str, *, job: str, steps: list[str], request: dict | None = None, metadata: dict | None = None,
             input_text: str | None = None, idx_jobs: list[str] | None = None) -> dict:
    return {"trace_id": job.replace("-", ""), "name": name, "job_id": job, "matched_steps": steps,
            "request": request, "metadata": metadata or {}, "input_text": input_text, "idx_jobs": idx_jobs,
            "observation_names": [], "tool_calls": [], "model_calls": 1}


class MemoryEvidenceTests(unittest.TestCase):
    def setUp(self) -> None:
        self.rec = {"vars": {"WEEKDAY": "周四", "HOUR": "18", "SECRET_A": "K57"}}
        self.ev = {"collected": True, "traces": [
            lf_trace("employee_loop", job="job-1", steps=["ask"],
                     request={"memory": "[P] 陈思远 10-03 说：本组周报每周四 18 点前交", "history": "[user] 闲聊",
                              "system": "SELF PROFILE: …", "current_window": "Current conversation window: …"},
                     metadata={"transcript_status": "loaded", "transcript_elapsed_ms": 812}),
            lf_trace("agent_task", job="", steps=[], idx_jobs=["job-1"],
                     input_text="FORMAL MATERIAL REFERENCES …\nMEMORY (Host-retrieved …)\n- 已验证 Verified outcome: a.csv … memory:abc"),
        ], "named_traces": [{"name": "employee_verified_distill"}],
            "scene_memory": {"g": {"status": 200, "learnings": [{"insight": "本组周报每周四 18 点前交", "capture_origin": "transcript"}]}}}

    def run_check(self, check: dict) -> dict:
        return grader.evidence_check(check, self.rec, self.ev)

    def test_memory_block_history_and_persona(self) -> None:
        self.assertTrue(self.run_check({"evidence": "memory_block", "step": "ask", "include": ["{WEEKDAY}", "{HOUR}"]})["ok"])
        self.assertFalse(self.run_check({"evidence": "memory_block", "step": "ask", "exclude": ["{WEEKDAY}"]})["ok"])
        self.assertTrue(self.run_check({"evidence": "history", "step": "ask", "exclude": ["{WEEKDAY}"]})["ok"])
        self.assertTrue(self.run_check({"evidence": "system_prompt", "step": "ask", "include": ["SELF PROFILE"]})["ok"])
        self.assertFalse(self.run_check({"evidence": "memory_block", "step": "other", "include": ["x"]})["ok"])
        self.assertTrue(self.run_check({"evidence": "history", "step": "ask", "include_any": ["[Host 分段]", "闲聊"]})["ok"])

    def test_metadata_packet_named_and_scene_memory(self) -> None:
        self.assertTrue(self.run_check({"evidence": "trace_metadata", "step": "ask", "key": "transcript_status", "equals": "loaded"})["ok"])
        self.assertTrue(self.run_check({"evidence": "trace_metadata", "step": "ask", "key": "transcript_elapsed_ms", "max": 2500})["ok"])
        self.assertFalse(self.run_check({"evidence": "trace_metadata", "step": "ask", "key": "memory_manifest", "contains": "x"})["ok"])
        self.assertTrue(self.run_check({"evidence": "packet", "step": "ask", "include": ["MEMORY (", "memory:"]})["ok"])
        self.assertFalse(self.run_check({"evidence": "packet", "step": "ask", "exclude": ["Verified outcome"]})["ok"])
        self.assertTrue(self.run_check({"evidence": "named_trace", "name": "employee_verified_distill"})["ok"])
        self.assertFalse(self.run_check({"evidence": "named_trace", "name": "employee_verified_distill", "present": False})["ok"])
        self.assertTrue(self.run_check({"evidence": "scene_memory", "conversation": "g", "include": ["{WEEKDAY}", "transcript"], "count": [1, 5]})["ok"])
        self.assertFalse(self.run_check({"evidence": "scene_memory", "conversation": "missing", "include": ["x"]})["ok"])

    def test_first_request_splits_roles(self) -> None:
        trace = {"name": "employee_loop", "observations": [{"type": "GENERATION", "startTime": "1", "input": [
            {"role": "system", "content": "persona LANGUAGE"},
            {"role": "user", "content": "Existing memory snapshot (data):\n[P] 先说结论"},
            {"role": "user", "content": "Recent conversation snapshot (data …):\n{}"},
            {"role": "user", "content": "[History {\"message_id\":\"m\"}]\n候选 A7"},
            {"role": "assistant", "content": "收到"},
            {"role": "user", "content": "Current conversation window:\n[{}]"}]}]}
        req = evidence.first_request(trace)
        self.assertEqual(req["memory"], "[P] 先说结论")
        self.assertIn("候选 A7", req["history"])
        self.assertIn("[assistant] 收到", req["history"])
        self.assertIn("LANGUAGE", req["system"])
        self.assertTrue(req["current_window"].startswith("Current conversation window"))

    def test_sentinel_over_several_conversations_and_language(self) -> None:
        reg = reg_fixture()
        reg["conversations"]["g"] = {"readers": ["director"]}
        transcripts = {"g": [msg("r1", "2026-10-03 18:13:00", EMP_D, "口令前半是 K57")],
                       "dm_director": [msg("m1", "2026-10-03 18:12:54", DIR_SELF, "q")]}
        rec = {"case_id": "X", "attempt": 1, "vars": {"SECRET": "K57-ABCD", "SECRET_A": "K57", "SECRET_B": "ABCD"},
               "steps": [{"id": "ask", "conversation": "dm_director", "msg": transcripts["dm_director"][0]}]}
        case = {"judge": {"checks": [{"sentinel": "{SECRET}", "parts": ["{SECRET_A}", "{SECRET_B}"], "conversations": ["g", "dm_director"]},
                                     {"steps": ["ask"], "lang": "zh"}]}}
        attributed = {"by_step": {"ask": [{"text": "X3", "messageId": "r"}]}}
        results = grader.run_checks(rec, case, attributed, transcripts, reg)
        self.assertEqual([r["ok"] for r in results[:2]], [True, True])  # split leak needs both parts
        self.assertFalse(results[2]["ok"])  # a reply with no Chinese


class MemoryLeakAndConvTests(unittest.TestCase):
    def test_memory_internals_are_leaks(self) -> None:
        for text in ["根据 [m2] 的记录", "[g3 10-03 14:02 陈思远·人] 说过", "speaker_ref 是主管", "Existing memory snapshot",
                     "dingtalk:44675729:open_id:abc 说的", "learning:9bc15452-eeda", "user-stated 偏好"]:
            self.assertTrue(leak.scan(text), text)
        for text in ["周报每周四 18 点前交，是陈主管定的。", "候选 G3 和 M2 都到了", "常用会议室是 B座7-03"]:
            self.assertEqual(leak.scan(text), [], text)

    def test_find_cid_and_member_ids(self) -> None:
        self.assertEqual(conv.find_cid({"result": {"data": {"openConversationId": "cidAbc=="}}}), "cidAbc==")
        self.assertEqual(conv.find_cid([{"x": 1}, {"conversationId": "cidZ"}]), "cidZ")
        self.assertIsNone(conv.find_cid({"conversationId": "not-a-cid"}))
        reg = {"employee": {"dws_user_id": "507"}, "actors": {"a": {"profile": "corp:123"}, "b": {"profile": "corp:9", "realniubility_user_id": "77"}}}
        self.assertEqual([conv.member_user_id(reg, n) for n in ("employee", "a", "b")], ["507", "123", "77"])

    def test_quoted_target_prefers_the_reply_that_quotes_the_step(self) -> None:
        rec = {"steps": [{"id": "note", "poll": {"replies": [{"messageId": "r0", "quotes_source": False},
                                                             {"messageId": "r1", "quotes_source": True}]}}]}
        self.assertEqual(driver.quoted_target({"reply_to": {"step": "note", "employee_reply": True}}, rec, {}), "r1")
        self.assertEqual(driver.quoted_target({"reply_to": {"step": "note"}}, rec, {"note": {"messageId": "m"}}), "m")
        self.assertIsNone(driver.quoted_target({}, rec, {}))


if __name__ == "__main__":
    unittest.main()
