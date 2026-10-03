"""Offline tests for the e2e harness (no network, no dws, no models).

Run: python3 -m unittest discover -s scripts/employee-e2e/tests -v
"""

from __future__ import annotations

import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

from el2e import common, envguard, grader, im, leak  # noqa: E402
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


if __name__ == "__main__":
    unittest.main()
