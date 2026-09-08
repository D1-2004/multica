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


if __name__ == "__main__":
    unittest.main()
