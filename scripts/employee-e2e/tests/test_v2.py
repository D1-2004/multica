"""Offline tests for cases-v2 support: var_sets, templating, validation, grader v2, paging.

Run: python3 -m unittest discover -s scripts/employee-e2e/tests -v
"""

from __future__ import annotations

import copy
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

from el2e import cases_v2, grader_v2, im  # noqa: E402
from el2e.common import registry, write_json  # noqa: E402

REG = registry()
EMP = REG["employee"]["open_ids"]["director"]
DIR_SELF = REG["actors"]["director"]["open_ids"]["self"]
SPEC = {"schema": cases_v2.SCHEMA, "suite": "X", "defaults": {
    "wait": {"reply": {"timeout_s": 10}, "none": {"pause_s": 1}},
    "pattern_sets": {"EN": r"(?i)\bI don't\b", "RESEND": r"(再|重新)发"},
    "aliases": {"FENGJIE": "(?:凤姐|王熙凤)", "DZONG": "(?:D总|Director)"}}}
CAPS = {"harness": {n: True for n in cases_v2.HARNESS_IMPLEMENTED} | {"segments": False},
        "platform": {"group_all_delivery": False, "vision": False}, "release": {"G_memory": False},
        "ops": {"routine_pause": False}}


def base_case(**over) -> dict:
    case = {"id": "X-01", "title": "t", "focus": [], "maps_to": [], "scene": "group", "conversation": "g_team",
            "roles": {"D总": "director", "凤姐": "wangxifeng"},
            "requires": {"harness": ["grader_v2"], "platform": [], "release": [], "ops": []},
            "status": {"now": "ready", "reason": ""}, "doc": "",
            "var_sets": [{"CITY": "杭州"}, {"CITY": "上海"}, {"CITY": "深圳"}],
            "steps": [{"id": "q1", "actor": "D总", "text": "去{CITY}的酒店谁订？", "at": ["employee"],
                       "wait": {"mode": "reply"}},
                      {"id": "q2", "actor": "凤姐", "text": "我来", "reply_to": {"employee_reply_of": "q1",
                                                                              "fallback": "employee_latest"},
                       "wait": {"mode": "reply"}}],
            "judge": {"criteria": "c", "semantic": [], "checks": [
                {"steps": ["q1"], "replies": [1, 1], "exclude_sets": ["EN"]},
                {"steps": ["q1"], "include_regex": ["{=FENGJIE}"], "tier": "target"},
                {"steps": ["q2"], "replies": [0, 0], "requires": "group_all_delivery"}]}}
    case.update(over)
    return case


def msg(mid: str, ts: str, sender: str, text: str, quote: str | None = None) -> dict:
    m = {"messageId": mid, "createTime": ts, "senderId": sender, "text": text,
         "sender": "Qwen-Real" if sender == EMP else "Director"}
    if sender == EMP:
        m["messageAiSendFlag"] = "DWS"
    if quote:
        m["quotedMessage"] = {"messageId": quote}
    return m


class VarSetTests(unittest.TestCase):
    def test_row_selection_is_seeded_and_merged(self) -> None:
        case = base_case()
        v1, i1 = cases_v2.select_vars(case, "R", 1)
        v1b, i1b = cases_v2.select_vars(case, "R", 1)
        self.assertEqual((v1, i1), (v1b, i1b))
        self.assertIn(v1["CITY"], {"杭州", "上海", "深圳"})
        self.assertIn("MISS", v1)  # driver codes stay available
        rows = {cases_v2.select_vars(case, "R", a)[1] for a in range(1, 30)}
        self.assertGreater(len(rows), 1)

    def test_name_clash_with_driver_codes_is_an_error(self) -> None:
        case = base_case(var_sets=[{"MISS": "B3"}])
        with self.assertRaises(cases_v2.CaseError):
            cases_v2.select_vars(case, "R", 1)
        self.assertTrue(any("collide" in e for e in cases_v2.validate_case(case, SPEC, CAPS, REG)))

    def test_variables_render_before_aliases(self) -> None:
        out = cases_v2.render("负责人 {OWNER_RE}", {"OWNER_RE": "{=FENGJIE}"}, SPEC["defaults"]["aliases"])
        self.assertEqual(out, "负责人 (?:凤姐|王熙凤)")
        self.assertEqual(cases_v2.unresolved(cases_v2.render("{NOPE} {=NOALIAS} .{0,8}", {}, {})), ["=NOALIAS", "NOPE"])


class ValidationTests(unittest.TestCase):
    def test_clean_case_validates(self) -> None:
        self.assertEqual(cases_v2.validate_case(base_case(), SPEC, CAPS, REG), [])

    def test_unknown_keys_at_every_level(self) -> None:
        case = base_case(x_new=1)
        case["steps"][0]["typo"] = 1
        case["judge"]["checks"][0]["includ_all"] = ["a"]
        errors = cases_v2.validate_case(case, SPEC, CAPS, REG)
        self.assertTrue(any("X-01: unknown keys ['x_new']" in e for e in errors))
        self.assertTrue(any("X-01.q1: unknown keys ['typo']" in e for e in errors))
        self.assertTrue(any("includ_all" in e for e in errors))

    def test_deap_actor_cannot_at_and_reply_to_must_point_back(self) -> None:
        case = base_case()
        case["steps"][1]["at"] = ["D总"]
        case["steps"][1]["reply_to"] = {"employee_reply_of": "later"}
        errors = cases_v2.validate_case(case, SPEC, CAPS, REG)
        self.assertTrue(any("cannot @" in e for e in errors))
        self.assertTrue(any("not an earlier step" in e for e in errors))

    def test_requires_expressions(self) -> None:
        caps = copy.deepcopy(CAPS)
        caps["platform"]["group_all_delivery"] = True
        self.assertTrue(cases_v2.requires_ok("group_all_delivery", caps))
        self.assertFalse(cases_v2.requires_ok("!group_all_delivery", caps))
        self.assertFalse(cases_v2.requires_ok("group_all_delivery+vision", caps))
        self.assertTrue(cases_v2.requires_ok("group_all_delivery+!vision", caps))

    def test_all_88_cases_dry_run_without_validation_errors(self) -> None:
        res = cases_v2.dry_run(None, cases_v2.load_capabilities())
        self.assertEqual(res["total"], 88)
        self.assertEqual(res["suite_errors"], [])
        self.assertNotIn("blocked_validation", res["counts"])
        self.assertEqual(sum(res["counts"].values()), 88)


class TextCheckTests(unittest.TestCase):
    ctx = {"pattern_sets": SPEC["defaults"]["pattern_sets"], "step_message_ids": {"a2": "src"}}

    def run_check(self, check: dict, texts: list[str], quotes: str | None = None) -> dict:
        msgs = [{"messageId": f"m{i}", "text": t, "quotes": quotes} for i, t in enumerate(texts)]
        return grader_v2.text_check(dict(check, steps=["s"]), msgs, self.ctx)

    def test_empty_content_check_is_vacuous_not_pass(self) -> None:
        self.assertEqual(self.run_check({"exclude_sets": ["EN"], "max_chars": 20}, [])["status"], "vacuous")
        self.assertEqual(self.run_check({"replies": [0, 1], "max_chars": 20}, [])["status"], "pass")
        self.assertEqual(self.run_check({"include_all": ["B3"]}, [])["status"], "fail")

    def test_include_regex_every_message_and_any(self) -> None:
        self.assertEqual(self.run_check({"include_regex": ["凤姐"]}, ["找凤姐", "好的"])["status"], "fail")
        self.assertEqual(self.run_check({"include_any_regex": ["凤姐|王熙凤"]}, ["好的", "找王熙凤"])["status"], "pass")
        self.assertEqual(self.run_check({"exclude_sets": ["EN"]}, ["I don't know"])["status"], "fail")

    def test_quotes_step_and_match_count(self) -> None:
        self.assertEqual(self.run_check({"quotes_step": "a2"}, ["x"], quotes="src")["status"], "pass")
        self.assertEqual(self.run_check({"quotes_step": "a2"}, ["x"], quotes="other")["status"], "fail")
        mc = {"match_count": {"regex": "没有新的", "range": [1, 1]}}
        self.assertEqual(self.run_check(mc, ["没有新的输出", "没有新的输出"])["status"], "fail")


class GradeCaseTests(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.TemporaryDirectory()
        self.rd = Path(self.tmp.name)
        (self.rd / "cases").mkdir()

    def tearDown(self) -> None:
        self.tmp.cleanup()

    def record(self, replies: list[dict], extra_steps: list[dict] | None = None, covered: bool = True) -> dict:
        q1 = msg("h1", "2026-10-03 20:00:00", DIR_SELF, "@Qwen-Real 去杭州的酒店谁订？")
        rec = {"schema": "el2e.driver.v2", "case_id": "X-01", "attempt": 1, "run_id": "R", "status": "completed",
               "roles": {"D总": "director"}, "vars": {"CITY": "杭州"}, "var_row": 0,
               "started_at": "2026-10-03T19:59:58+08:00", "ended_at": "2026-10-03T20:02:00+08:00",
               "steps": [{"id": "q1", "actor": "director", "conversation": "g_team",
                          "send": {"landed": [q1], "match_key": "去杭州的酒店谁订？", "sent_at": "2026-10-03T20:00:00+08:00"}}]
               + (extra_steps or []),
               "transcripts": {"g_team@director": {"covered": covered, "messages": [q1] + replies}}}
        write_json(self.rd / "cases" / "X-01.a1.driver.json", rec)
        return rec

    def case(self) -> dict:
        case = base_case()
        case["steps"] = case["steps"][:1]
        case["judge"]["checks"] = case["judge"]["checks"][:2] + [
            {"steps": ["q1"], "replies": [0, 0], "requires": "group_all_delivery"},
            {"sentinel": "K83-CIOT", "conversation": "g_team", "scope": "case_span"}]
        return case

    def grade(self, rec: dict, case: dict | None = None) -> dict:
        return grader_v2.grade_case_v2(self.rd, rec, case or self.case(), SPEC, CAPS)

    def test_target_miss_with_hard_pass_is_degraded_and_requires_off_is_vacuous(self) -> None:
        rec = self.record([msg("e1", "2026-10-03 20:00:05", EMP, "林晓还没订，找行政订", quote="h1")])
        res = self.grade(rec)
        self.assertEqual(res["verdict"], "degraded")
        statuses = [c["status"] for c in res["checks"]]
        self.assertIn("vacuous", statuses)
        self.assertEqual(res["check_counts"].get("fail"), 1)

    def test_hard_failure_and_target_hit(self) -> None:
        rec = self.record([msg("e1", "2026-10-03 20:00:05", EMP, "I don't know, 问凤姐", quote="h1")])
        self.assertEqual(self.grade(rec)["verdict"], "fail")
        rec = self.record([msg("e1", "2026-10-03 20:00:05", EMP, "林晓那间找凤姐订", quote="h1")])
        self.assertEqual(self.grade(rec)["verdict"], "needs_review")

    def test_sentinel_scans_only_the_case_span(self) -> None:
        # A later case's reply (quoting that case's line) after this case ended.
        late = msg("e9", "2026-10-03 20:30:00", EMP, "编号 K83-CIOT", quote="next-case-line")
        rec = self.record([msg("e1", "2026-10-03 20:00:05", EMP, "找凤姐", quote="h1"), late])
        self.assertEqual(self.grade(rec)["verdict"], "needs_review")
        inside = msg("e2", "2026-10-03 20:01:00", EMP, "编号 K83-CIOT")
        rec = self.record([msg("e1", "2026-10-03 20:00:05", EMP, "找凤姐", quote="h1"), inside])
        self.assertEqual(self.grade(rec)["verdict"], "fail")

    def test_unmatched_observe_fails_and_uncovered_window_is_harness_error(self) -> None:
        obs = {"id": "o1", "conversation": "g_team", "observe": {"until_regex": "55"}, "poll": {"matched": False, "covered": True}}
        rec = self.record([msg("e1", "2026-10-03 20:00:05", EMP, "找凤姐", quote="h1")], extra_steps=[obs])
        self.assertEqual(self.grade(rec)["verdict"], "fail")
        rec = self.record([msg("e1", "2026-10-03 20:00:05", EMP, "找凤姐", quote="h1")], covered=False)
        self.assertEqual(self.grade(rec)["verdict"], "harness_error")


    def test_judgement_cannot_hide_pending_evidence_or_vacuous_checks(self) -> None:
        rec = self.record([msg("e1", "2026-10-03 20:00:05", EMP, "找凤姐", quote="h1")])
        judge = {"X-01.a1": {"verdict": "pass", "rationale": "looks good"}}
        res = grader_v2.grade_case_v2(self.rd, rec, self.case(), SPEC, CAPS, judgements=judge)
        self.assertEqual(res["verdict"], "partial")
        case = self.case()
        case["judge"]["checks"].append({"evidence": "max_calls_per_wake", "max": 3})
        res = grader_v2.grade_case_v2(self.rd, rec, case, SPEC, CAPS, judgements=judge)
        self.assertEqual(res["verdict"], "incomplete")

    def test_judgement_cannot_override_a_hard_failure(self) -> None:
        rec = self.record([msg("e1", "2026-10-03 20:00:05", EMP, "I don't know, 找凤姐", quote="h1")])
        for wanted in ("pass", "degraded", "needs_review"):
            with self.subTest(wanted=wanted):
                res = grader_v2.grade_case_v2(self.rd, rec, self.case(), SPEC, CAPS,
                                            judgements={"X-01.a1": {"verdict": wanted}})
                self.assertEqual(res["verdict"], "fail")

    def test_favorable_judge_cannot_pass_a_failed_hard_conversation_check(self) -> None:
        rec = self.record([msg("e1", "2026-10-03 20:00:05", EMP, "I don't know", quote="h1")])
        res = grader_v2.grade_case_v2(
            self.rd, rec, self.case(), SPEC, CAPS,
            judgements={"X-01.a1": {"verdict": "pass", "rationale": "the reply looks fine"}},
        )
        self.assertEqual(res["verdict"], "fail")
        self.assertNotEqual(res["verdict"], "pass")

    def test_empty_trace_listing_is_missing_evidence_not_zero_calls(self) -> None:
        res = grader_v2.evidence_check_v2({"evidence": "max_calls_per_wake", "max": 3},
                                        {"collected": True, "traces": []})
        self.assertEqual(res["status"], "pending_evidence")

    def test_negative_observe_without_coverage_is_incomplete(self) -> None:
        obs = {"id": "o1", "conversation": "g_team", "observe": {"until_regex": "执行", "negative": True},
               "poll": {"matched": False, "covered": False}}
        rec = self.record([msg("e1", "2026-10-03 20:00:05", EMP, "找凤姐", quote="h1")], extra_steps=[obs])
        self.assertEqual(self.grade(rec)["verdict"], "incomplete")


class SpeakTests(unittest.TestCase):
    """Driver step semantics without the network: who sends, how it is read back, what is quoted."""

    def setUp(self) -> None:
        from el2e import driver_v2
        self.dv = driver_v2
        self.conv = REG["conversations"]["g_team"]
        self.case = base_case()
        self.spec = dict(SPEC, defaults=dict(SPEC["defaults"], human_send={"ai_tag": False}))
        self.calls: list[tuple[str, dict]] = []

    def fake(self, kind: str):
        def inner(**kw):
            self.calls.append((kind, kw))
            return {"ok": True, "landing_count": 1, "text": kw["text"],
                    "landed": [{"messageId": "new", "createTime": "2026-10-03 20:00:00", "text": "@Qwen-Real " + kw["text"]}]}
        return inner

    def speak(self, step: dict, rec: dict | None = None, landed: dict | None = None) -> dict:
        with mock.patch.object(self.dv.im, "send", side_effect=self.fake("send")), \
                mock.patch.object(self.dv.im, "reply", side_effect=self.fake("reply")):
            return self.dv.speak(step, self.case, self.spec, rec or {"steps": [], "started_at": "2026-10-03T20:00:00+08:00"},
                                 REG, "g_team", self.conv, {"CITY": "杭州"}, landed or {}, set(), "m")

    def test_human_send_has_ai_tag_off_and_reads_back_by_own_id(self) -> None:
        self.speak(self.case["steps"][0])
        kind, kw = self.calls[0]
        self.assertEqual(kind, "send")
        self.assertIs(kw["ai_tag"], False)
        self.assertEqual(kw["sender_id"], REG["actors"]["director"]["open_ids"]["self"])
        self.assertEqual(kw["at_ids"], [REG["employee"]["open_ids"]["director"]])
        self.assertEqual(kw["text"], "去杭州的酒店谁订？")

    def test_deap_quote_reply_is_read_back_by_a_human_reader(self) -> None:
        rec = {"steps": [{"id": "q1", "poll": {"matched_message": {"messageId": "emp1"}}}],
               "started_at": "2026-10-03T20:00:00+08:00"}
        step = dict(self.case["steps"][1], reply_to={"observed": "q1"})
        self.speak(step, rec)
        kind, kw = self.calls[0]
        self.assertEqual(kind, "reply")
        self.assertEqual(kw["quoted_message_id"], "emp1")
        self.assertIsNone(kw["ai_tag"])
        self.assertEqual(kw["reader_profile"], REG["actors"]["director"]["profile"])
        self.assertEqual(kw["sender_id"], REG["actors"]["wangxifeng"]["open_ids"]["director"])
        self.assertEqual(kw["at_ids"], [])

    def test_quoting_the_employee_drops_the_explicit_employee_at(self) -> None:
        rec = {"steps": [{"id": "q1", "poll": {"matched_message": {"messageId": "emp1"}}}],
               "started_at": "2026-10-03T20:00:00+08:00"}
        step = {"id": "q3", "actor": "D总", "text": "再看下", "at": ["employee"], "reply_to": {"observed": "q1"},
                "wait": {"mode": "reply"}}
        sent = self.speak(step, rec)
        self.assertEqual(self.calls[0][1]["at_ids"], [])
        self.assertEqual(sent["at_render"]["expected"], 1)

    def test_missing_quote_target_is_a_harness_error_not_a_plain_send(self) -> None:
        step = dict(self.case["steps"][1], reply_to={"step": "q1"})
        with self.assertRaises(self.dv.StepError):
            self.speak(step)
        self.assertEqual(self.calls, [])

    def test_missing_employee_latest_still_sends_the_question_and_records_the_miss(self) -> None:
        question = "你刚才那条结论还作数吗？"
        step = dict(self.case["steps"][1], text=question, reply_to={"employee_latest": True})
        human = {"messageId": "h1", "createTime": "2026-10-03 20:00:01", "senderId": DIR_SELF,
                 "text": "我先说一句", "sender": "Director"}
        with mock.patch.object(self.dv, "transcript", return_value=[human]), \
                mock.patch.object(self.dv.im, "read_messages", return_value={"messages": [human]}):
            sent = self.speak(step)
        self.assertEqual(self.calls[0][0], "send")
        self.assertEqual(self.calls[0][1]["text"], question)
        self.assertEqual([kind for kind, _ in self.calls], ["send"])
        self.assertEqual(sent["quote_miss"], {"missing": "employee_latest", "by_employee": False, "via": "employee_latest"})


class PagingTests(unittest.TestCase):
    def test_read_window_pages_back_with_time_boundary(self) -> None:
        pages = [
            {"messages": [{"messageId": "c", "createTime": "2026-10-03 20:10:00"},
                          {"messageId": "b", "createTime": "2026-10-03 20:05:00"}], "hasMore": True, "complete": False, "failures": [], "nextPage": {"direction": "older", "time": "2026-10-03T20:05:00+08:00"}},
            {"messages": [{"messageId": "b", "createTime": "2026-10-03 20:05:00"},
                          {"messageId": "a", "createTime": "2026-10-03 19:50:00"}], "hasMore": True, "complete": False, "failures": []},
        ]
        calls = []

        def fake(profile, args, **_):
            calls.append(args)
            return {"rc": 0, "json": pages[len(calls) - 1]}

        with mock.patch.object(im.dwsgw, "dws", side_effect=fake):
            out = im.read_window("p", "cid", im.parse_dws_time("2026-10-03 20:00:00"), limit=2)
        self.assertTrue(out["covered"])
        self.assertEqual([m["messageId"] for m in out["messages"]], ["b", "c"])
        self.assertIn("--time", calls[1])
        self.assertNotIn("--start", sum(calls, []))

    def test_read_failure_or_incomplete_page_never_covers_a_negative_window(self) -> None:
        for res in ({"rc": 1, "json": {"messages": []}},
                    {"rc": 0, "json": {}},
                    {"rc": 0, "json": {"messages": [], "complete": False}},
                    {"rc": 0, "json": {"messages": [], "failures": ["permission denied"]}}):
            with self.subTest(res=res), mock.patch.object(im.dwsgw, "dws", return_value=res):
                out = im.read_window("p", "c", im.parse_dws_time("2026-10-03 20:00:00"))
                self.assertFalse(out["covered"])
                self.assertTrue(out["failures"])

    def test_normal_partial_history_page_crossing_window_is_covered(self) -> None:
        page = {"messages": [{"messageId": "old", "createTime": "2026-10-03 19:59:59"},
                             {"messageId": "new", "createTime": "2026-10-03 20:00:01"}],
                "complete": False, "hasMore": True, "failures": [], "partial": False, "truncated": False}
        with mock.patch.object(im.dwsgw, "dws", return_value={"rc": 0, "json": page}):
            result = im.read_window("p", "c", im.parse_dws_time("2026-10-03 20:00:00"))
        self.assertTrue(result["covered"])
        self.assertEqual(result["stop_reason"], "lower_bound_crossed")

    def test_page_failure_missing_metadata_or_truncation_is_not_coverage(self) -> None:
        page = {"messages": [{"messageId": "old", "createTime": "2026-10-03 19:59:59"}],
                "complete": False, "hasMore": True, "failures": []}
        for patch in ({"hasMore": None}, {"complete": None}, {"failures": ["read failed"]},
                      {"partial": True}, {"truncated": True}, {"paginationKnown": False},
                      {"hasMore": False}, {"complete": True}, {"decryptFailedCount": 1}, {"stopReason": "page_limit"}):
            with self.subTest(patch=patch), mock.patch.object(im.dwsgw, "dws", return_value={"rc": 0, "json": page | patch}):
                result = im.read_window("p", "c", im.parse_dws_time("2026-10-03 20:00:00"))
            self.assertFalse(result["covered"])
            self.assertTrue(result["failures"])

    def test_second_page_failure_or_page_cap_never_proves_absence(self) -> None:
        page = {"messages": [{"messageId": "new", "createTime": "2026-10-03 20:00:10"}],
                "complete": False, "hasMore": True, "failures": [],
                "nextPage": {"direction": "older", "time": "2026-10-03T20:00:10+08:00"}}
        with mock.patch.object(im.dwsgw, "dws", side_effect=[{"rc": 0, "json": page}, {"rc": 1, "json": {}}]):
            result = im.read_window("p", "c", im.parse_dws_time("2026-10-03 20:00:00"))
        self.assertFalse(result["covered"])
        self.assertEqual(result["pages"], 2)
        with mock.patch.object(im.dwsgw, "dws", return_value={"rc": 0, "json": page}):
            capped = im.read_window("p", "c", im.parse_dws_time("2026-10-03 20:00:00"), max_pages=1)
        self.assertFalse(capped["covered"])
        self.assertEqual(capped["stop_reason"], "page_cap")

    def test_cursor_cannot_jump_past_an_unread_lower_boundary(self) -> None:
        page = {"messages": [{"messageId": "new", "createTime": "2026-10-03 20:00:10"}],
                "complete": False, "hasMore": True, "failures": [],
                "nextPage": {"direction": "older", "time": "2026-10-03T19:55:00+08:00"}}
        with mock.patch.object(im.dwsgw, "dws", return_value={"rc": 0, "json": page}) as call:
            result = im.read_window("p", "c", im.parse_dws_time("2026-10-03 20:00:00"))
        self.assertFalse(result["covered"])
        self.assertEqual(result["stop_reason"], "cursor_crossed_unread_boundary")
        self.assertEqual(call.call_count, 1)

    def test_equal_lower_bucket_or_stagnating_cursor_stays_partial(self) -> None:
        page = {"messages": [{"messageId": "same", "createTime": "2026-10-03 20:00:00"}],
                "complete": False, "hasMore": True, "failures": [],
                "nextPage": {"direction": "older", "time": "2026-10-03T20:00:00.100+08:00"}}
        with mock.patch.object(im.dwsgw, "dws", return_value={"rc": 0, "json": page}):
            result = im.read_window("p", "c", im.parse_dws_time("2026-10-03 20:00:00"), limit=1)
        self.assertFalse(result["covered"])
        self.assertEqual(result["stop_reason"], "saturated_time_bucket")
        page2 = page | {"messages": [{"messageId": "another", "createTime": "2026-10-03 20:00:00"}]}
        with mock.patch.object(im.dwsgw, "dws", side_effect=[{"rc": 0, "json": page}, {"rc": 0, "json": page2}]):
            stuck = im.read_window("p", "c", im.parse_dws_time("2026-10-03 19:59:00"))
        self.assertFalse(stuck["covered"])
        self.assertEqual(stuck["stop_reason"], "cursor_not_advancing")

    def test_prefix_mentions(self) -> None:
        self.assertEqual(im.prefix_mentions("@冬翔  @Qwen-Real  请核对"), ["冬翔", "Qwen-Real"])
        self.assertEqual(im.prefix_mentions("请 @小Q 看下"), [])


if __name__ == "__main__":
    unittest.main()
