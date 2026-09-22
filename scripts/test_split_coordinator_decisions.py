"""Regression coverage for exported decision grouping and null snapshots."""
import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location("splitter", Path(__file__).with_name("split-coordinator-decisions.py"))
splitter = importlib.util.module_from_spec(spec)
spec.loader.exec_module(splitter)


class SplitSamplesTest(unittest.TestCase):
    def test_nullable_export_fields(self):
        samples = [
            {"split_group": "a", "snapshot": None, "execution_result": None},
            {"split_group": "a", "snapshot": {"recalled_ids": None}, "execution_result": {"tasks": None}},
        ]
        result = splitter.split_samples(samples)
        partitions = [rows for rows in result.values() if rows]
        self.assertEqual(len(partitions), 1)
        self.assertEqual(len(partitions[0]), 2)

    def test_transitive_links_and_order_independence(self):
        samples = [
            {"split_group": "a", "snapshot": {"recalled_ids": ["x"]}},
            {"split_group": "b", "snapshot": {"recalled_ids": ["x", "y"]}},
            {"split_group": "c", "execution_result": {"tasks": [{"issue_id": "y"}]}},
        ]
        def membership(rows):
            return {s["split_group"]: (partition, s["split_component"])
                    for partition, group in splitter.split_samples(rows).items() for s in group}
        actual = membership(samples)
        self.assertEqual(len(set(actual.values())), 1)
        self.assertEqual(actual, membership(list(reversed(samples))))


if __name__ == "__main__":
    unittest.main()
