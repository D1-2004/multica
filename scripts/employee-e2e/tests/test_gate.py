"""Offline gate failures must stop every mutation before a case starts."""
from __future__ import annotations

import json
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))
from el2e import cases_v2, driver, driver_v2, envguard, lease, memory
from el2e.common import iso, now, write_json


class GateCaseTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.rd = Path(self.tmp.name)
        self.case = {"id": "X", "title": "gate", "conversation": "g_team",
                     "roles": {"D总": "director", "凤姐": "wangxifeng"},
                     "requires": {"harness": ["memory_reset"]},
                     "steps": [{"id": "s", "actor": "D总", "text": "停止这个", "wait": {"mode": "none"}}]}
        self.spec = {"suite": "X", "defaults": {}, "cases": [self.case]}

    def tearDown(self):
        self.tmp.cleanup()

    def gate_cases(self, version):
        for failure in ({"ok": False, "error": "admission timeout"}, None, RuntimeError("read failed")):
            with self.subTest(failure=failure), \
                    mock.patch.object(envguard, "gate", side_effect=failure if isinstance(failure, Exception) else None,
                                      return_value=failure), \
                    mock.patch.object(driver, "precondition", return_value=None), \
                    mock.patch.object(cases_v2, "classify", return_value={"state": "runnable"}), \
                    mock.patch.object(driver_v2.CaseRun, "run_steps") as steps, \
                    mock.patch.object(driver.im, "send") as send, mock.patch.object(driver.im, "reply") as reply, \
                    mock.patch.object(driver, "ensure_dm") as dm, \
                    mock.patch.object(lease, "acquire") as acquire, mock.patch.object(memory, "reset") as reset, \
                    mock.patch.object(driver_v2, "renew_access") as renewal:
                if version == 1:
                    rec = driver.run_case(self.case, self.spec, "R", self.rd, skip_gate=False)
                else:
                    rec = driver_v2.run_case_v2(self.case, self.spec, "R", self.rd,
                                              cases_v2.load_capabilities(), skip_gate=False)
                self.assertEqual(rec["status"], "invalid_env")
                self.assertFalse((rec["gate"] or {}).get("ok"))
                self.assertEqual(rec["steps"], [])
                saved = json.loads((self.rd / "cases" / f"X.a{rec['attempt']}.driver.json").read_text())
                self.assertEqual(saved["gate"], rec["gate"])
                for action in (steps, send, reply, dm, acquire, reset, renewal):
                    action.assert_not_called()

    def test_v1_failure_or_exception_never_sends(self):
        self.gate_cases(1)

    def test_v2_failure_or_exception_precedes_lease_actions_and_cleanup(self):
        self.gate_cases(2)

    def test_segment_failure_preserves_checkpoint_before_renewal_or_redo(self):
        self.case["segments"] = [{"id": "d1"}, {"id": "d2"}]
        old_step = {"id": "saved", "segment": "d2"}
        previous = {"case_id": "X", "attempt": 1, "vars": {}, "steps": [old_step],
                    "started_at": "2026-10-01T00:00:00+08:00", "segments": {
                        "d1": {"status": "done", "ended_at": "2026-10-01T00:00:00+08:00"},
                        "d2": {"status": "done", "ended_at": "2026-10-02T00:00:00+08:00"}}}
        (self.rd / "cases").mkdir()
        write_json(self.rd / "cases" / "X.a1.driver.json", previous)
        with mock.patch.object(envguard, "gate", return_value={"ok": False}), \
                mock.patch.object(driver_v2, "renew_access") as renewal, \
                mock.patch.object(lease, "acquire") as acquire:
            rec = driver_v2.run_case_v2(self.case, self.spec, "R", self.rd, cases_v2.load_capabilities(),
                                       segment="d2", redo=True)
        self.assertEqual(rec["status"], "invalid_env")
        self.assertEqual(rec["steps"], [old_step])
        self.assertNotIn("discarded_steps", rec)
        renewal.assert_not_called()
        acquire.assert_not_called()

    def test_both_run_entrypoints_return_nonzero_and_manifest_declares_idle_limit(self):
        path = self.rd / "suite.json"
        write_json(path, self.spec)
        with mock.patch.object(envguard, "gate", return_value={"ok": False}), \
                mock.patch.object(driver, "precondition", return_value=None), \
                mock.patch.object(cases_v2, "classify", return_value={"state": "runnable"}), \
                mock.patch.object(driver.dwsgw, "prepare"), \
                mock.patch.object(driver, "run_dir", return_value=self.rd), \
                mock.patch.object(driver_v2, "run_dir", return_value=self.rd), \
                mock.patch.object(cases_v2, "load_all", return_value=[(self.spec, self.case)]), \
                mock.patch.object(driver.im, "send") as send:
            self.assertEqual(driver.run(path, "R", only=[]), 1)
            self.assertEqual(driver_v2.run_v2([], "R", only=[], caps=cases_v2.load_capabilities()), 1)
        send.assert_not_called()
        manifest = json.loads((self.rd / "manifest.json").read_text())
        self.assertIs(manifest["idle_contract_v2"]["automatic_idle_before_min"], False)

    def test_latest_failed_snapshot_cannot_fall_back_to_old_green(self):
        rows = [{"kind": "pipeline", "ok": True, "at": iso(now()), "status": "SUCCESS"},
                {"kind": "pipeline", "ok": False, "at": iso(now()), "error": "read failed"}]
        (self.rd / "env_timeline.jsonl").write_text("\n".join(json.dumps(x) for x in rows))
        result = envguard.gate(self.rd, max_wait_s=0, log=lambda *_: None)
        self.assertFalse(result["ok"])
        self.assertEqual(result["pipeline"]["error"], "read failed")

    def test_missing_deploy_completion_is_not_ready(self):
        snap = {"ok": True, "at": iso(now()), "status": "SUCCESS", "deploy": {"status": "SUCCESS"}}
        with mock.patch.object(envguard, "latest", return_value=snap):
            result = envguard.gate(self.rd, max_wait_s=0, log=lambda *_: None)
        self.assertFalse(result["ok"])
        self.assertTrue(result["state"]["missing_completed_at"])

    def test_pipeline_empty_or_failed_json_response_is_not_ready(self):
        for rc in (0, 1):
            with self.subTest(rc=rc), mock.patch.object(envguard, "run_cmd", return_value={"rc": rc, "stdout": "{}", "stderr": ""}):
                self.assertFalse(envguard.pipeline_snapshot()["ok"])
