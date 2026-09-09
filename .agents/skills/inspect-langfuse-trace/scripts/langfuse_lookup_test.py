#!/usr/bin/env python3
"""Unit tests for langfuse_lookup helpers. No network, no credentials."""
from __future__ import annotations

import importlib.util
import unittest
from pathlib import Path

spec = importlib.util.spec_from_file_location(
    "langfuse_lookup", Path(__file__).with_name("langfuse_lookup.py")
)
mod = importlib.util.module_from_spec(spec)
assert spec.loader is not None
spec.loader.exec_module(mod)


class ExpandTagTests(unittest.TestCase):
    def test_bare_uuid_becomes_agent_tag(self):
        uid = "3141dfdb-d567-46ca-93d4-a754292fc16e"
        self.assertEqual(mod.expand_tag(uid), f"agent-{uid}")

    def test_already_prefixed_uuid_unchanged(self):
        raw = "agent-3141dfdb-d567-46ca-93d4-a754292fc16e"
        self.assertEqual(mod.expand_tag(raw), raw)

    def test_name_becomes_agent_name_tag(self):
        self.assertEqual(mod.expand_tag("金龙"), "agent_name-金龙")

    def test_known_loop_tag_unchanged(self):
        self.assertEqual(mod.expand_tag("inbound_coordinator"), "inbound_coordinator")

    def test_source_tag_unchanged(self):
        self.assertEqual(mod.expand_tag("source-digital_employee"), "source-digital_employee")


class GlobalFlagsTests(unittest.TestCase):
    def test_limit_is_not_left_as_positional_tag(self):
        rest, flags = mod.parse_global_flags(
            ["tag", "agent-3141dfdb-d567-46ca-93d4-a754292fc16e", "--limit", "30"]
        )
        self.assertEqual(rest, ["tag", "agent-3141dfdb-d567-46ca-93d4-a754292fc16e"])
        self.assertEqual(flags["limit"], 30)
        self.assertFalse(flags["json"])

    def test_json_and_from_anywhere(self):
        rest, flags = mod.parse_global_flags(
            ["--json", "search", "--from", "7d", "--agent", "金龙", "--text", "练货"]
        )
        self.assertEqual(rest, ["search", "--agent", "金龙", "--text", "练货"])
        self.assertTrue(flags["json"])
        self.assertEqual(flags["from"], "7d")


class LoopParamsTests(unittest.TestCase):
    def test_default_search_pairs_agent_and_loop_tags(self):
        params = mod.loop_list_params("agent-3141dfdb-d567-46ca-93d4-a754292fc16e", "inbound_coordinator", None)
        self.assertEqual(
            params["tags"],
            ["agent-3141dfdb-d567-46ca-93d4-a754292fc16e", "inbound_coordinator"],
        )
        self.assertNotIn("name", params)

    def test_all_does_not_add_loop_tag(self):
        params = mod.loop_list_params("agent-abc", None, None)
        self.assertEqual(params["tags"], ["agent-abc"])


class SearchArgsTests(unittest.TestCase):
    def test_positional_uuid_then_text(self):
        spec = mod.parse_search_args(["3141dfdb-d567-46ca-93d4-a754292fc16e", "练货"])
        self.assertEqual(spec["agent"], "3141dfdb-d567-46ca-93d4-a754292fc16e")
        self.assertEqual(spec["text"], "练货")

    def test_flag_form(self):
        spec = mod.parse_search_args(["--agent", "金龙", "--text", "VOC"])
        self.assertEqual(spec["agent"], "金龙")
        self.assertEqual(spec["text"], "VOC")

    def test_message_alias(self):
        spec = mod.parse_search_args(["--message", "兜底"])
        self.assertEqual(spec["text"], "兜底")


class TextMatchTests(unittest.TestCase):
    def test_cjk_substring_on_hydrated_input(self):
        tr = {"input": "平台方是否应对AI练货底层模型稳定性进行兜底及透出底层日志", "metadata": {"agent_name": "金龙"}}
        blob = mod.trace_search_blob(tr)
        self.assertTrue(mod.blob_has_text(blob, "练货"))
        self.assertTrue(mod.blob_has_text(blob, "兜底"))
        self.assertFalse(mod.blob_has_text(blob, "不存在的探针"))

    def test_empty_list_input_is_missing(self):
        self.assertTrue(mod.input_missing({"input": None}))
        self.assertTrue(mod.input_missing({"input": {}}))
        self.assertTrue(mod.input_missing({"input": "null"}))
        self.assertFalse(mod.input_missing({"input": "练货"}))


class FromTimestampTests(unittest.TestCase):
    def test_relative_window(self):
        ts = mod.parse_from_timestamp("7d")
        self.assertIsNotNone(ts)
        self.assertTrue(ts.endswith("Z"))


class QueryCoverageTests(unittest.TestCase):
    def test_scan_exceeds_old_eight_page_cap(self):
        class Fake:
            max_pages = 0
            def get(self, _path, params):
                start = (params["page"] - 1) * params["limit"]
                return {"data": [{"id": str(i)} for i in range(start, min(start + params["limit"], 450))],
                        "meta": {"totalItems": 450}}
        client = Fake()
        self.assertEqual(len(mod.list_traces(client, {}, 500)), 450)
        self.assertEqual(client.last_scan["pages"], 9)
        self.assertFalse(client.last_scan["truncated"])

    def test_duplicate_versions_keep_latest_and_report_rows(self):
        class Fake:
            max_pages = 0
            def get(self, _path, _params):
                return {"data": [{"id": "same", "timestamp": "2026-09-08T12:00:00Z"},
                                 {"id": "same", "timestamp": "2026-09-08T14:00:00Z"}],
                        "meta": {"totalItems": 2}}
        client = Fake()
        result = mod.list_traces(client, {}, 50)
        self.assertEqual(result[0]["timestamp"], "2026-09-08T14:00:00Z")
        self.assertEqual(client.last_scan["duplicate_count"], 1)
        self.assertEqual(client.last_scan["api_total_rows"], 2)

    def test_page_cap_is_explicitly_incomplete(self):
        class Fake:
            max_pages = 1
            def get(self, _path, _params):
                return {"data": [{"id": str(i)} for i in range(50)], "meta": {"totalItems": 300}}
        client = Fake()
        self.assertEqual(len(mod.list_traces(client, {}, 200)), 50)
        self.assertTrue(client.last_scan["truncated"])

    def test_trace_search_prefers_coordinator_observation(self):
        tr = {"input": "task overwrote this", "output": "task done",
              "observations": [{"name": "inbound_coordinator", "input": "原始问题探针", "output": {"action": "issue"}}]}
        self.assertIn("原始问题探针", mod.trace_search_blob(tr))
        self.assertNotIn("task overwrote this", mod.trace_search_blob(tr))

    def test_unhydrated_search_never_claims_complete(self):
        class Fake:
            workers = 2
            hydrate_limit = 0
            def in_scope(self, _trace):
                return True
        client = Fake()
        self.assertEqual(mod.search_text(client, [{"id": "a", "input": None}], "needle", 20), [])
        self.assertTrue(client.search_stats["truncated"])
        self.assertEqual(client.search_stats["unhydrated"], 1)

    def test_window_flags_are_not_search_arguments(self):
        rest, flags = mod.parse_global_flags(["search", "--to=2026-09-08T16:00:00Z", "--pages", "0", "--stats", "--scan", "500"])
        self.assertEqual(rest, ["search"])
        self.assertEqual(flags["to"], "2026-09-08T16:00:00Z")
        self.assertEqual(flags["scan"], 500)


if __name__ == "__main__":
    unittest.main()
