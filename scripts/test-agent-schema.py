"""Validate the portable Agent contract with JSON Schema Draft 2020-12.

Install scripts/agent-schema-requirements.txt, then run this file from any cwd.
"""

import copy
import json
from pathlib import Path
import re
import unittest

from jsonschema import Draft202012Validator, FormatChecker


ROOT = Path(__file__).resolve().parents[1]
SCHEMA = json.loads((ROOT / "server/internal/agentsource/agent.schema.json").read_text())
EXAMPLE = ROOT / "docs/examples/agent-manifest-v2.json"


def minimal(version="multica.agent/v2"):
    return {"$schema": "agent.schema.json", "version": version, "name": "Reviewer",
            "instructions": "AGENTS.md", "skills": []}


class AgentSchemaTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        Draft202012Validator.check_schema(SCHEMA)
        cls.validator = Draft202012Validator(SCHEMA, format_checker=FormatChecker())

    def assert_valid(self, value):
        errors = list(self.validator.iter_errors(value))
        self.assertFalse(errors, [(list(error.path), error.message) for error in errors])

    def assert_invalid(self, value):
        self.assertFalse(self.validator.is_valid(value), value)

    def test_minimal_versions(self):
        for version in ("multica.agent/v1", "multica.agent/v2"):
            with self.subTest(version=version):
                self.assert_valid(minimal(version))

    def test_full_example(self):
        self.assert_valid(json.loads(EXAMPLE.read_text()))

    def test_version_boundary(self):
        for version in ("multica.agent/v1", "multica.agent/v3"):
            value = minimal(version)
            value["configuration"] = {"persona": "Helpful teammate"}
            self.assert_invalid(value)

    def test_unknown_and_instance_fields(self):
        for field in ("id", "workspace_id", "owner_id", "status", "system_key",
                      "system_instructions", "runtime_id", "created_at", "credentials"):
            value = minimal()
            value[field] = "instance-owned"
            self.assert_invalid(value)
        value = minimal()
        value["configuration"] = {"inbound_coordinater": True}
        self.assert_invalid(value)

    def test_existing_agent_write_fields_are_accounted_for(self):
        source = (ROOT / "server/internal/handler/agent.go").read_text()
        update = source.split("type UpdateAgentRequest struct {", 1)[1].split("\n}", 1)[0]
        fields = set(re.findall(r'`json:"([^",]+)', update))
        configuration = set(SCHEMA["$defs"]["configuration"]["properties"])
        # These fields have a dedicated portable representation or are instance state.
        separate = {"name", "description", "instructions", "runtime_id", "permission_mode",
                    "invocation_targets", "visibility", "status"}
        self.assertEqual(fields - configuration - separate, set())

    def test_configuration_limits(self):
        for key, good, bad in (("persona", "人" * 400, "人" * 401),
                               ("reply_tone", "语" * 200, "语" * 201),
                               ("max_concurrent_tasks", 50, 51),
                               ("max_concurrent_tasks", 1, 0),
                               ("inbound_coordinator", False, "false")):
            with self.subTest(field=key, bad=bad):
                value = minimal()
                value["configuration"] = {key: good}
                self.assert_valid(value)
                value["configuration"][key] = bad
                self.assert_invalid(value)

    def test_omitted_false_empty_and_null_are_distinct(self):
        value = minimal()
        value["configuration"] = {"inbound_coordinator": False, "persona": "",
                                  "custom_args": [], "custom_env": {}, "mcp_config": None,
                                  "runtime_config": {}, "composio_toolkit_allowlist": []}
        self.assert_valid(value)
        for key in ("inbound_coordinator", "persona", "custom_env", "custom_args", "runtime_config"):
            bad = copy.deepcopy(value)
            bad["configuration"][key] = None
            self.assert_invalid(bad)

    def test_secret_references(self):
        value = minimal()
        value["configuration"] = {"custom_env": {"API_TOKEN": {"secret_ref": "api-token"}},
                                  "runtime_config": {"mode": "gateway", "gateway": {
                                      "host": "localhost", "port": 18789,
                                      "token": {"secret_ref": "gateway-token"}}}}
        self.assert_valid(value)
        for token in ("plaintext", "***", {"secret_ref": ""},
                      {"secret_ref": "gateway-token", "value": "plaintext"}):
            bad = copy.deepcopy(value)
            bad["configuration"]["runtime_config"]["gateway"]["token"] = token
            self.assert_invalid(bad)

    def test_paths_and_skill_state(self):
        for path in ("../escape", "/absolute", "./AGENTS.md", "a//b", "a\\b", "a/../b", "a/"):
            value = minimal()
            value["instructions"] = path
            self.assert_invalid(value)
        value = minimal()
        value["skills"] = [{"path": "skills/review", "name": "review", "enabled": False}]
        self.assert_valid(value)
        del value["skills"][0]["enabled"]
        self.assert_invalid(value)

    def test_access_modes(self):
        value = minimal()
        value["access"] = {"permission_mode": "public_to", "invocation_targets": [{"target_type": "workspace"}]}
        self.assert_valid(value)
        value["access"]["permission_mode"] = "private"
        self.assert_invalid(value)
        value["access"]["invocation_targets"] = []
        self.assert_valid(value)
        value["access"]["permission_mode"] = "public_to"
        self.assert_invalid(value)

    def test_okr_limits_and_no_runtime_spend(self):
        value = minimal()
        value["okrs"] = [{"objective": "Review changes", "key_results": ["Review one change"]}]
        self.assert_valid(value)
        value["okrs"][0]["spend"] = {"total_tokens": 1}
        self.assert_invalid(value)
        del value["okrs"][0]["spend"]
        value["okrs"][0]["key_results"] = ["x"] * 11
        self.assert_invalid(value)

    def test_endpoint_scopes_and_no_credentials(self):
        value = minimal()
        value["a2a"] = {"enabled": True, "clients": [{"key": "reviewer", "name": "Reviewer",
                         "status": "active", "scopes": ["send", "read"]}]}
        self.assert_valid(value)
        for key, bad in (("scopes", ["admin"]), ("scopes", ["send", "send"]),
                         ("rate_limit_per_minute", 0), ("credentials", [])):
            bad_value = copy.deepcopy(value)
            bad_value["a2a"]["clients"][0][key] = bad
            self.assert_invalid(bad_value)

    def test_binding_ids_are_not_portable(self):
        value = minimal()
        value["bindings"] = {"runtime": {"ref": "execution-runtime", "provider": "codex"}}
        self.assert_valid(value)
        value["bindings"]["runtime"]["runtime_id"] = "instance-id"
        self.assert_invalid(value)

    def test_dispatch_segments_follow_current_contract(self):
        value = minimal()
        value["configuration"] = {"dispatch_prompt_overrides": {"policy": "Review carefully"}}
        self.assert_valid(value)
        for key in ("context", "dingtalk_conversation", "unknown"):
            value["configuration"]["dispatch_prompt_overrides"] = {key: "Override"}
            self.assert_invalid(value)

    def test_runtime_skill_and_plugin_requirements(self):
        value = minimal()
        value["dsh_plugins"] = [{"ref": "review-plugin", "enabled": False}]
        value["disabled_runtime_skills"] = [{"runtime_ref": "review-runtime", "provider": "codex",
                                            "root": "plugin", "key": "review", "plugin": "review-tools"}]
        self.assert_valid(value)
        del value["disabled_runtime_skills"][0]["plugin"]
        self.assert_invalid(value)

    def test_card_skill_constraints(self):
        value = minimal()
        value["a2a"] = {"card_skills": [{"id": "review", "name": "Reviewer",
                                        "description": "Review changes", "tags": []}]}
        self.assert_valid(value)
        for key, bad in (("description", ""), ("id", "x" * 129),
                         ("inputModes", ["application/unsupported"]), ("securityRequirements", [])):
            bad_value = copy.deepcopy(value)
            bad_value["a2a"]["card_skills"][0][key] = bad
            self.assert_invalid(bad_value)

    def test_nested_provider_extensions_preserve_secret_refs(self):
        value = minimal()
        value["configuration"] = {"mcp_config": {"mcp": {"local": {
            "type": "local", "command": ["node", "server.js"],
            "environment": {"API_TOKEN": {"secret_ref": "api-token"}},
            "provider_extension": {"feature": [True, 1, None]}}}}}
        self.assert_valid(value)
        value["configuration"]["mcp_config"]["mcp"]["local"]["provider_extension"] = {"secret_ref": "api-token", "value": "bad"}
        self.assert_invalid(value)


if __name__ == "__main__":
    unittest.main()
