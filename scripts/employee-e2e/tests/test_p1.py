"""Offline tests for the cases-v2 P1 harness capabilities (no network)."""

from __future__ import annotations

import sys
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

from el2e import memory  # noqa: E402
from el2e.common import registry  # noqa: E402

REG = registry()


def fake_send(sent_log):
    def inner(**kw):
        sent_log.append(kw)
        return {"ok": True, "landed": [{"messageId": f"m{len(sent_log)}", "createTime": "2026-10-03 22:00:00"}]}
    return inner


class MemoryResetTests(unittest.TestCase):
    case = {"id": "X", "conversation": "dm_director", "roles": {"D总": "director", "冬翔": "zhujue", "凤姐": "wangxifeng"},
            "steps": [{"id": "a", "actor": "D总"}, {"id": "b", "actor": "冬翔", "conversation": "dm_zhujue"},
                      {"id": "c", "actor": "D总", "conversation": "group_p_hx"},
                      {"id": "d", "actor": "凤姐", "conversation": "group_p_hx"},
                      {"id": "e", "actor": "D总", "conversation": "group_e2e"}]}

    def test_plan_covers_each_human_per_conversation_and_skips_deap(self) -> None:
        self.assertEqual(memory.plan(self.case), [("dm_director", "director"), ("dm_zhujue", "zhujue"),
                                                  ("group_e2e", "director"), ("group_p_hx", "director")])

    def test_reset_sends_the_command_only_inside_the_allowed_scope(self) -> None:
        sent: list[dict] = []
        reply = {"messageId": "r", "senderId": REG["employee"]["open_ids"]["director"], "text": "已清理本会话的 Employee 共享记忆",
                 "quotedMessage": {"messageId": "m1"}}
        with mock.patch.object(memory.im, "send", side_effect=fake_send(sent)), \
                mock.patch.object(memory.im, "read_messages", return_value={"messages": [reply]}), \
                mock.patch.object(memory.preapi, "scene_id_for", return_value=None), \
                mock.patch.object(memory.time, "sleep"):
            out = memory.reset(self.case, "R:X:a1", wait_s=1)
        convs = [r["conversation"] for r in out["commands"]]
        self.assertIn({"conversation": "group_e2e", "actor": "director", "skipped": "outside the reset scope"}, out["commands"])
        self.assertEqual(len(sent), 3)  # dm_director, dm_zhujue, group_p_hx
        self.assertTrue(all(kw["text"] == "/reset-memory" and kw["ai_tag"] is False for kw in sent))
        group = [kw for kw in sent if kw["cid"] == REG["conversations"]["group_p_hx"]["cid"]][0]
        self.assertEqual(group["at_ids"], [REG["employee"]["open_ids"]["director"]])
        self.assertEqual(convs.count("group_e2e"), 1)


class PgReadTests(unittest.TestCase):
    def test_collect_keeps_only_case_scene_tasks_inside_the_window(self) -> None:
        from el2e import api_facts
        rec = {"case_id": "P-05", "attempt": 1, "started_at": "2026-10-03T22:00:00+08:00",
               "ended_at": "2026-10-03T22:10:00+08:00", "steps": [{"id": "a", "conversation": "group_p_hx"}]}
        tasks = [
            {"task_id": "in", "scene_id": "S1", "created_at": "2026-10-03T22:01:00+08:00", "updated_at": "2026-10-03T22:05:00+08:00"},
            {"task_id": "other-scene", "scene_id": "S9", "created_at": "2026-10-03T22:01:00+08:00", "updated_at": "2026-10-03T22:05:00+08:00"},
            {"task_id": "old", "scene_id": "S1", "created_at": "2026-10-03T20:00:00+08:00", "updated_at": "2026-10-03T22:02:00+08:00"},
        ]
        detail = {"status": 200, "task": {"collections": [{"collection_id": "c1", "state": "open", "expected": 2, "received": 1}],
                                          "execution": {"exit_confirmed": True}}, "runs": []}
        with mock.patch.object(api_facts.preapi, "scene_id_for", return_value="S1"), \
                mock.patch.object(api_facts.preapi, "tasks_since", return_value=tasks), \
                mock.patch.object(api_facts.preapi, "task_detail", return_value=detail), \
                mock.patch.object(api_facts.preapi, "routines", return_value=[]):
            facts = api_facts.collect(rec)
        self.assertEqual([t["summary"]["task_id"] for t in facts["tasks"]], ["in"])
        self.assertEqual(api_facts.collections(facts)[0]["received"], 1)
        self.assertTrue(api_facts.exit_states(facts)[0]["exit_confirmed"])

    def test_pending_checks_without_api_are_api_gaps(self) -> None:
        from el2e import grader_v2
        res = grader_v2.pending_result({"evidence": "pg_learning", "why": "x"}, {}, {"tasks": []}, {})
        self.assertEqual(res["status"], "api_gap")
        res = grader_v2.pending_result({"evidence": "pg_collection"}, {}, None, {})
        self.assertEqual(res["status"], "pending_evidence")


    def test_task_list_and_scene_directory_fail_closed_on_api_errors_or_page_caps(self) -> None:
        from el2e import preapi
        with mock.patch.object(preapi, "call", return_value=(503, {"tasks": []})):
            with self.assertRaises(RuntimeError):
                preapi.tasks_since("2026-10-03T20:00:00+08:00")
        page = {"tasks": [{"updated_at": "2026-10-03T22:00:00+08:00"}], "next_cursor": "more"}
        with mock.patch.object(preapi, "call", return_value=(200, page)):
            with self.assertRaisesRegex(RuntimeError, "pagination exhausted"):
                preapi.tasks_since("2026-10-03T20:00:00+08:00", max_pages=1)
        with mock.patch.object(preapi, "call", return_value=(200, {"scenes": [], "has_more": True})):
            with self.assertRaisesRegex(RuntimeError, "pagination exhausted"):
                preapi.scene_index()

    def test_scene_directory_pages_by_offset_and_keeps_server_ids(self) -> None:
        from el2e import preapi
        pages = [(200, {"scenes": [{"conversation_id": "c1", "scene_id": "s1"}], "has_more": True}),
                 (200, {"scenes": [{"conversation_id": "c2", "scene_id": "s2"}], "has_more": False})]
        with mock.patch.object(preapi, "call", side_effect=pages) as call:
            self.assertEqual(preapi.scene_index(), {"c1": "s1", "c2": "s2"})
        self.assertIn("offset=100", call.call_args_list[1].args[1])


class SegmentTests(unittest.TestCase):
    def setUp(self) -> None:
        import tempfile
        from el2e import cases_v2, driver_v2
        self.dv, self.cv = driver_v2, cases_v2
        self.tmp = tempfile.TemporaryDirectory()
        self.rd = Path(self.tmp.name)
        self.case = {"id": "S-01", "title": "t", "conversation": "dm_director", "roles": {"D总": "director"},
                     "segments": [{"id": "d1"}, {"id": "d2", "not_before_hours": 25}],
                     "requires": {"harness": ["segments"], "platform": [], "release": [], "ops": []},
                     "status": {"now": "ready", "reason": ""},
                     "steps": [{"id": "a", "actor": "D总", "text": "x", "segment": "d1", "wait": {"mode": "none"}},
                               {"id": "b", "actor": "D总", "text": "y", "segment": "d2", "wait": {"mode": "none"}}],
                     "judge": {"criteria": "", "checks": [], "semantic": []}}
        self.spec = {"suite": "S", "defaults": {"wait": {"none": {"pause_s": 0}}}}
        self.caps = cases_v2.load_capabilities()

    def tearDown(self) -> None:
        self.tmp.cleanup()

    def fake_run_steps(self, run, steps):
        for st in steps:
            run.rec["steps"].append({"id": st["id"], "segment": st.get("segment"), "conversation": "dm_director",
                                     "send": {"landed": [{"messageId": "m-" + st["id"], "createTime": "2026-10-03 22:00:00"}]}})

    def run_seg(self, segment=None, redo=False):
        with mock.patch.object(self.dv.CaseRun, "run_steps", new=lambda run, steps: self.fake_run_steps(run, steps)), \
                mock.patch.object(self.dv.CaseRun, "snapshot"), mock.patch.object(self.dv.time, "sleep"), \
                mock.patch.object(self.dv.cases_v2, "classify", return_value={"state": "runnable", "reasons": []}), \
                mock.patch.object(self.dv, "renew_access", return_value={}), \
                mock.patch("el2e.grader_v2.grade_and_write"):
            return self.dv.run_case_v2(self.case, self.spec, "R", self.rd, self.caps, skip_gate=True, use_lease=False,
                                       segment=segment, redo=redo)

    def test_first_segment_then_not_before_then_resume(self) -> None:
        rec = self.run_seg()
        self.assertEqual(rec["status"], "segment_done:d1")
        self.assertEqual([s["id"] for s in rec["steps"]], ["a"])
        waiting = self.run_seg("d2")
        self.assertTrue(waiting["status"].startswith("waiting_segment"))
        path = self.rd / "cases" / "S-01.a1.driver.json"
        import json
        saved = json.loads(path.read_text())
        saved["segments"]["d1"]["ended_at"] = "2026-10-01T00:00:00+08:00"
        path.write_text(json.dumps(saved))
        rec = self.run_seg("d2")
        self.assertEqual(rec["status"], "completed")
        self.assertEqual([s["id"] for s in rec["steps"]], ["a", "b"])
        self.assertEqual(rec["vars"], saved["vars"])  # variables shared across segments
        with self.assertRaises(SystemExit):
            self.run_seg("d2")  # already done without --redo
        rec = self.run_seg("d2", redo=True)
        self.assertEqual([s["id"] for s in rec["steps"]], ["a", "b"])
        self.assertEqual([s["id"] for s in rec["discarded_steps"]], ["b"])

    def test_segment_validity_is_per_segment(self) -> None:
        from el2e import grader_v2
        rec = {"steps": [], "segments": {"d1": {"started_at": "2026-10-03T10:00:00+08:00", "ended_at": "2026-10-03T10:10:00+08:00"},
                                          "d2": {"started_at": "2026-10-04T11:00:00+08:00", "ended_at": "2026-10-04T11:10:00+08:00"}},
               "started_at": "2026-10-03T10:00:00+08:00"}
        (self.rd / "evidence").mkdir()
        (self.rd / "evidence" / "sls_server_starting.json").write_text(
            '{"ok": true, "starts": [{"host": "a", "time": "2026-10-04T11:05:00+08:00"}]}')
        v = grader_v2.segment_validity(self.rd, rec, {"by_step": {}})
        self.assertEqual(v["invalid_segments"], ["d2"])


class EvidenceV2Tests(unittest.TestCase):
    def ev(self) -> dict:
        dispatch = {"name": "dispatch_task", "level": "DEFAULT",
                    "input": '{"source_ref": "r", "goal": "g", "prompt": "改到 10/14，宣传册给林晓", "builds_on": "t1"}'}
        return {"collected": True, "traces": [
            {"trace_id": "w1", "name": "employee_loop", "matched_steps": ["m1"], "model_calls": 1,
             "tools": ["dispatch_task"], "tools_full": [dispatch], "tool_calls_full": [], "task_ids": ["T1"], "run_ids": ["R1"]},
            {"trace_id": "w2", "name": "employee_loop", "matched_steps": ["m2"], "model_calls": 2,
             "tools": ["memory_capture", "reply"], "tools_full": [], "task_ids": [],
             "tool_calls_full": [{"name": "stop_task", "arguments": "{}"}]},
        ]}

    def check(self, **c) -> str:
        from el2e import grader_v2
        return grader_v2.evidence_check_v2(c, self.ev(), {"scenes": {"group_t": "S1"}, "tasks": [{"summary": {"task_id": "T2"}}]})["status"]

    def test_tool_called_counts_executed_tools_only(self) -> None:
        self.assertEqual(self.check(evidence="tool_called", steps=["m2"], tool="memory_capture", count=[1, 1]), "pass")
        self.assertEqual(self.check(evidence="tool_called", steps=["m1"], tool="memory_capture", count=[1, 1]), "fail")
        self.assertEqual(self.check(evidence="tool_called", steps=["zz"], tool="memory_capture", count=[0, 0]), "vacuous")

    def test_effect_for_step_ignores_proposed_but_rejected_calls(self) -> None:
        self.assertEqual(self.check(evidence="effect_for_step", step="m2", tools=["stop_task"]), "fail")
        self.assertEqual(self.check(evidence="effect_for_step", step="m1", tools=["dispatch_task"]), "pass")
        self.assertEqual(self.check(evidence="effect_for_step", step="m2", tools=["stop_task"], if_dispatched_at="m2"), "na")

    def test_proposed_args_or_rejected_tool_are_not_accepted_effect_evidence(self) -> None:
        from el2e import grader_v2
        ev = self.ev()
        check = {"evidence": "tool_arg_present", "step": "m2", "tool": "stop_task", "arg": "task_id"}
        ev["traces"][1]["tool_calls_full"] = [{"name": "stop_task", "arguments": '{"task_id":"T1"}'}]
        self.assertEqual(grader_v2.evidence_check_v2(check, ev)["status"], "fail")
        ev["traces"][0]["tools_full"][0]["level"] = "ERROR"
        self.assertEqual(grader_v2.evidence_check_v2({"evidence": "no_effect_for_step", "step": "m1"}, ev)["status"], "pass")
        ev["traces"][0]["tools_full"] = []
        self.assertEqual(grader_v2.evidence_check_v2({"evidence": "tool_arg_present", "step": "m1", "tool": "dispatch_task", "arg": "builds_on"}, ev)["status"], "pending_evidence")

    def test_tool_args_and_task_count(self) -> None:
        self.assertEqual(self.check(evidence="tool_arg_present", step="m1", tool="dispatch_task", arg="builds_on"), "pass")
        self.assertEqual(self.check(evidence="tool_arg_present", step="m1", tool="dispatch_task", arg="follow_up_steps"), "fail")
        self.assertEqual(self.check(evidence="tool_arg_contains", step="m1", tool="dispatch_task", arg="prompt",
                                    values=["10/14", "林晓"]), "pass")
        self.assertEqual(self.check(evidence="tool_arg_contains", step="m2", tool="dispatch_task", arg="prompt",
                                    values=["x"], if_dispatched=True), "na")
        self.assertEqual(self.check(evidence="task_count", min=2, max=2), "pass")


class FileSendTests(unittest.TestCase):
    def run_for(self, case_id: str, row: int):
        import json as _json
        from el2e import cases_v2, driver_v2
        spec, case = next((sp, c) for sp, c in cases_v2.load_all() if c["id"] == case_id)
        vars_, _ = cases_v2.select_vars(case, "R", 1)
        vars_.update({k: str(v) for k, v in case["var_sets"][row].items()})
        rec = {"vars": vars_, "var_row": row, "steps": [], "run_id": "R", "attempt": 1}
        return driver_v2.CaseRun(case, spec, rec, Path("/dev/null"), Path("."), log=lambda *_: None), case

    def test_fixtures_follow_the_var_row(self) -> None:
        from el2e import driver_v2
        run, case = self.run_for("M-06", 1)
        name, data = driver_v2.fixture_bytes(run, "bx")
        self.assertEqual(data.decode(), run.render(case["fixtures"]["bx"]["by_row"][1]))
        self.assertTrue(name.endswith(".csv") and "{" not in name)
        run, _ = self.run_for("M-07", 0)
        name, data = driver_v2.fixture_bytes(run, "minutes")
        self.assertNotIn("{CUST}", data.decode())
        run, _ = self.run_for("M-08", 2)
        name, data = driver_v2.fixture_bytes(run, "shot")
        self.assertTrue(data.startswith(b"\x89PNG"))


class SinglesTests(unittest.TestCase):
    def make_run(self, case: dict):
        from el2e import driver_v2
        spec = {"suite": "S", "defaults": {"wait": {"none": {"pause_s": 0}}, "human_send": {"ai_tag": False}}}
        rec = {"vars": {}, "var_row": None, "steps": [], "run_id": "R", "attempt": 1, "status": "running"}
        return driver_v2.CaseRun(case, spec, rec, Path("/dev/null"), Path("."), log=lambda *_: None)

    def test_legacy_reply_readback_does_not_count_employee_substring_as_duplicate(self) -> None:
        from el2e import im
        stamp = im.now().strftime("%Y-%m-%d %H:%M:%S")
        rows = [{"messageId": "human-stop", "senderId": REG["actors"]["director"]["open_ids"]["self"],
                 "createTime": stamp, "text": "停止这个", "quotedMessage": {"messageId": "original"}},
                {"messageId": "employee-stop", "senderId": REG["employee"]["open_ids"]["director"],
                 "createTime": stamp, "text": "已请求停止这个任务", "quotedMessage": {"messageId": "human-stop"}}]
        with mock.patch.object(im.dwsgw, "dws", return_value={"rc": 0, "json": {}}), \
                mock.patch.object(im, "read_messages", return_value={"messages": rows}), mock.patch.object(im.time, "sleep"):
            out = im.reply(profile=REG["actors"]["director"]["profile"], cid="test-cid", text="停止这个",
                           marker="REF-01a-stop", quoted_message_id="original")
        self.assertTrue(out["ok"])
        self.assertEqual(out["landing_count"], 1)
        self.assertEqual(out["landed"][0]["messageId"], "human-stop")
        self.assertEqual(out["sender_id"], REG["actors"]["director"]["open_ids"]["self"])

    def test_at_all_body_and_flag(self) -> None:
        from el2e import im
        calls = []
        with mock.patch.object(im.dwsgw, "dws", side_effect=lambda p, a, **k: calls.append(a) or {"rc": 0, "json": {}}), \
                mock.patch.object(im, "read_messages", return_value={"messages": []}):
            im.send(profile="p", cid="c", text="明早评审改线上", marker="m", at_all=True, readback_timeout=0)
        self.assertIn("--at-all", calls[0])
        self.assertEqual(calls[0][calls[0].index("--text") + 1], "<@all> 明早评审改线上")

    def test_burst_sends_first_then_locates_all(self) -> None:
        from el2e import driver_v2
        me = REG["actors"]["director"]["open_ids"]["self"]
        case = {"id": "M-09", "conversation": "dm_director", "roles": {"D总": "director"},
                "steps": [{"id": f"b{i}", "actor": "D总", "text": t, "burst": True, "wait": {"mode": "none", "pause_s": 0}}
                          for i, t in enumerate(["诶 刚来电话", "说要改", "下周再说"], 1)]}
        run = self.make_run(case)
        order = []
        msgs = [{"messageId": f"x{i}", "createTime": "2099-01-01 00:00:0" + str(i), "senderId": me, "text": t}
                for i, t in enumerate(["诶 刚来电话", "说要改", "下周再说"], 1)]
        with mock.patch.object(driver_v2.im, "send", side_effect=lambda **kw: order.append(("send", kw["readback_timeout"])) or
                               {"ok": False, "landed": [], "landing_count": 0}), \
                mock.patch.object(driver_v2.im, "read_messages", side_effect=lambda *a, **k: order.append(("read",)) or {"messages": msgs}), \
                mock.patch.object(driver_v2.time, "sleep"), mock.patch.object(run, "save"):
            run.run_steps(case["steps"])
        self.assertEqual(order[:3], [("send", 0)] * 3)
        self.assertEqual([s["send"]["landed"][0]["messageId"] for s in run.rec["steps"]], ["x1", "x2", "x3"])
        self.assertEqual(run.rec["status"], "running")

    def test_burst_does_not_hide_duplicate_landings(self) -> None:
        from el2e import driver_v2
        step = {"id": "b1", "actor": "D总", "text": "下周再说", "burst": True, "wait": {"mode": "none", "pause_s": 0}}
        run = self.make_run({"id": "M-09", "conversation": "dm_director", "roles": {"D总": "director"}, "steps": [step]})
        rows = [{"messageId": mid, "createTime": "2099-01-01 00:00:00", "text": "下周再说",
                 "senderId": REG["actors"]["director"]["open_ids"]["self"]} for mid in ("first", "duplicate")]
        with mock.patch.object(driver_v2.im, "send", return_value={"ok": False, "landed": [], "landing_count": 0}), \
                mock.patch.object(driver_v2.im, "read_messages", return_value={"messages": rows}), \
                mock.patch.object(driver_v2.time, "sleep"), mock.patch.object(run, "save"):
            run.run_steps([step])
        self.assertEqual(run.rec["status"], "harness_error")
        self.assertEqual(run.rec["steps"][0]["send"]["landing_count"], 2)

    def test_negative_observe_grades_inverted(self) -> None:
        from el2e import grader_v2
        ctx = {"step_times": {"pause": "2026-10-03 13:00:00", "resume": "2026-10-03 13:20:00"},
               "step_conversations": {"pause": "group_t", "resume": "group_t"},
               "covered_conversations": {"group_t"},
               "employee_messages": [{"conversation": "group_t", "messageId": "e", "createTime": "2026-10-03 13:15:00", "text": "开始执行例行任务「灰度」"}]}
        res = grader_v2.pending_result({"evidence": "negative_observe", "between": ["pause", "resume"],
                                        "regex": "开始执行例行任务", "count": 0}, {}, None, ctx)
        self.assertEqual(res["status"], "fail")

    def test_download_records_real_bytes_and_isolates_each_attempt(self) -> None:
        import tempfile, hashlib
        from el2e import driver_v2
        data = b"\xef\xbb\xbfname,total\r\nAlice,12\r\n"
        with tempfile.TemporaryDirectory() as tmp:
            run = self.make_run({"id": "T-06"})
            run.rd = Path(tmp)
            poll = {"matched_raw": {"messageId": "m", "resourceRefs": [{"fileId": "f", "fileName": "out.csv"}]}}
            def download(profile, args, **kw):
                (Path(kw["cwd"]) / "out.csv").write_bytes(data)
                return {"rc": 0}
            with mock.patch.object(driver_v2.dwsgw, "dws", side_effect=download):
                a = driver_v2.download_resource(run, REG, REG["conversations"]["dm_director"], poll, "o1")
                b = driver_v2.download_resource(run, REG, REG["conversations"]["dm_director"], poll, "o1")
            self.assertTrue(a["ok"] and a["bom"] and a["utf8"])
            self.assertEqual(a["sha256"], hashlib.sha256(data).hexdigest())
            self.assertEqual(a["line_count"], 2)
            self.assertNotEqual(a["path"], b["path"])
            with mock.patch.object(driver_v2.dwsgw, "dws", return_value={"rc": 0}):
                c = driver_v2.download_resource(run, REG, REG["conversations"]["dm_director"], poll, "o1")
            self.assertFalse(c["ok"])

    def test_negative_observe_excludes_another_conversation(self) -> None:
        from el2e import grader_v2
        ctx = {"step_times": {"pause": "2026-10-03 13:00:00", "resume": "2026-10-03 13:20:00"},
               "step_conversations": {"pause": "group_t", "resume": "group_t"},
               "covered_conversations": {"group_t"},
               "employee_messages": [{"conversation": "g_team", "messageId": "e", "createTime": "2026-10-03 13:15:00", "text": "开始执行例行任务"}]}
        check = {"evidence": "negative_observe", "between": ["pause", "resume"], "regex": "开始执行例行任务", "count": 0}
        self.assertEqual(grader_v2.pending_result(check, {}, None, ctx)["status"], "pass")
        ctx["covered_conversations"] = set()
        self.assertEqual(grader_v2.pending_result(check, {}, None, ctx)["status"], "pending_evidence")

    def test_resource_refs_found_anywhere(self) -> None:
        from el2e import driver_v2
        refs = driver_v2._resource_refs({"messageId": "m", "resourceRefs": [{"fileId": "F1", "fileName": "a.csv"}]})
        self.assertEqual(refs, [{"id": "F1", "type": "fileId", "name": "a.csv"}])


if __name__ == "__main__":
    unittest.main()
