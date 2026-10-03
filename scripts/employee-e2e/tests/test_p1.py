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
        return grader_v2.evidence_check_v2(c, self.ev(), {"tasks": [{"summary": {"task_id": "T2"}}]})["status"]

    def test_tool_called_counts_executed_tools_only(self) -> None:
        self.assertEqual(self.check(evidence="tool_called", steps=["m2"], tool="memory_capture", count=[1, 1]), "pass")
        self.assertEqual(self.check(evidence="tool_called", steps=["m1"], tool="memory_capture", count=[1, 1]), "fail")
        self.assertEqual(self.check(evidence="tool_called", steps=["zz"], tool="memory_capture", count=[0, 0]), "vacuous")

    def test_effect_for_step_ignores_proposed_but_rejected_calls(self) -> None:
        self.assertEqual(self.check(evidence="effect_for_step", step="m2", tools=["stop_task"]), "fail")
        self.assertEqual(self.check(evidence="effect_for_step", step="m1", tools=["dispatch_task"]), "pass")
        self.assertEqual(self.check(evidence="effect_for_step", step="m2", tools=["stop_task"], if_dispatched_at="m2"), "na")

    def test_tool_args_and_task_count(self) -> None:
        self.assertEqual(self.check(evidence="tool_arg_present", step="m1", tool="dispatch_task", arg="builds_on"), "pass")
        self.assertEqual(self.check(evidence="tool_arg_present", step="m1", tool="dispatch_task", arg="follow_up_steps"), "fail")
        self.assertEqual(self.check(evidence="tool_arg_contains", step="m1", tool="dispatch_task", arg="prompt",
                                    values=["10/14", "林晓"]), "pass")
        self.assertEqual(self.check(evidence="tool_arg_contains", step="m2", tool="dispatch_task", arg="prompt",
                                    values=["x"], if_dispatched=True), "na")
        self.assertEqual(self.check(evidence="task_count", min=2, max=2), "pass")


if __name__ == "__main__":
    unittest.main()
