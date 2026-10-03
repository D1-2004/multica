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


if __name__ == "__main__":
    unittest.main()
