"""User-visible text checks: internal-term leaks, junk and secrets.

GawkBot's human-boundary assertions (assert_no_junk + Wave E1) adapted to
EmployeeLoop: no raw JSON/stack/signal text, no internal tool names or enum
values, no UUIDs, no credentials, and no case sentinels outside their scope.
Links the product intentionally sends (scene configuration links) are allowed.
"""

from __future__ import annotations

import re
from typing import Any

INTERNAL_TERMS = [
    # EmployeeLoop foreground tools and decisions
    "dispatch_task", "continue_task", "steer_task", "stop_task", "read_task", "read_task_history",
    "stay_quiet", "describe_capabilities", "scene_config_get", "memory_capture", "memory_lookup",
    "memory_forget", "read_context", "employee_loop", "employee_model", "tool_batch_rejected",
    "employee_execution_event", "source_ref", "task_ref", "read_ref", "completion_notice_policy",
    # Task / Run / queue states and receipts
    "waiting_inputs", "collection.ready", "run_only", "process_exit_confirmed", "execution_state",
    "native_file_delivered", "outbox_committed", "employee_scene_job", "queue_task_id", "receipt_id",
    "lease_generation", "employee_job_id", "scene_id", "tenant_org_id", "workspace_id",
    # Executor / sandbox internals
    "signal: killed", "exit status", "dws-rpc", "HERMES_HOME", "AuthCode",
]
# Memory and context assembly internals (12-memory-design): block headers the
# Host frames model input with, record fields, evidence identities and states.
# A user must never see how the brief or the history snapshot was built.
MEMORY_INTERNAL_TERMS = [
    "Existing memory snapshot", "EMPLOYEE MEMORY", "EMPLOYEE RETRIEVED EXPERIENCE", "Requester-private background",
    "Recent conversation snapshot", "Current conversation window", "Host 分段", "群聊旁听", "NEEDS CONFIRMATION",
    "memory_manifest", "memory_query_terms", "speaker_ref", "speaker_name", "capture_origin", "transcript_ref",
    "conflicts_with", "evidence_id", "source_id", "scope_kind", "principal_id", "requester_ref", "replay_key",
    "record_ref", "record_id", "superseded_by", "forgotten_at", "user-stated", "employee-message:",
    "dingtalk-message:", "agent_task_queue:", "employee-run:", "Unverified execution candidate",
]
INTERNAL_TERMS = INTERNAL_TERMS + MEMORY_INTERNAL_TERMS
# Short labels and identities that only exist in model input: brief labels
# [m3], transcript labels [g2 …], learning refs and org-qualified person refs.
MEMORY_LABEL_RE = re.compile(r"(\[m\d{1,3}\]|\[g\d{1,3}[\s\]]|\blearning:[0-9a-f-]{8,}|\bdingtalk:[0-9A-Za-z_-]+:(uid|open_id|staff_id):)")
INTERNAL_RE = [re.compile(r"(?<![A-Za-z0-9_])" + re.escape(term) + r"(?![A-Za-z0-9_])") for term in INTERNAL_TERMS]
UUID_RE = re.compile(r"\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b")
HEX32_RE = re.compile(r"(?<![0-9a-fA-F])[0-9a-f]{32}(?![0-9a-fA-F])")
JSON_RE = re.compile(r'(\{\s*"[A-Za-z_][A-Za-z0-9_]*"\s*:)|(\[\s*\{\s*")')
STACK_RE = re.compile(r"(Traceback \(most recent call last\)|goroutine \d+ \[|panic: |\.go:\d+\b|at [\w.$]+\([\w.]+:\d+\)|"
                      r"Exception in thread|stack trace|\bSIGKILL\b|\bSIGTERM\b)")
SECRET_RE = re.compile(r"(\bmat_[A-Za-z0-9]{8,}|Bearer\s+[A-Za-z0-9._-]{12,}|\bsk-[A-Za-z0-9]{16,}|\bLTAI[A-Za-z0-9]{12,}|"
                       r"\bpk-lf-[A-Za-z0-9-]{8,}|\bsk-lf-[A-Za-z0-9-]{8,}|refresh_token|access_token|appSecret)")
ANSI_RE = re.compile(r"\x1b\[[0-9;]*m")
ALLOWED_LINK_RE = re.compile(r"https?://pre-fde-workbench\.dingtalk\.com/dingtalk/configure\?link=[A-Za-z0-9_%-]+|"
                             r"dingtalk://dingtalkclient/page/link\?url=[^)\s]+")


def scan(text: str, *, sentinels: list[str] | None = None) -> list[dict[str, Any]]:
    """Return leak findings for one user-visible message."""
    findings: list[dict[str, Any]] = []
    body = ALLOWED_LINK_RE.sub("<link>", text or "")
    for term, rx in zip(INTERNAL_TERMS, INTERNAL_RE):
        if rx.search(body):
            findings.append({"kind": "internal_term", "match": term})
    for rx, kind in ((UUID_RE, "uuid"), (HEX32_RE, "trace_id"), (JSON_RE, "raw_json"), (STACK_RE, "stack_or_signal"),
                     (SECRET_RE, "secret"), (ANSI_RE, "ansi"), (MEMORY_LABEL_RE, "memory_label")):
        m = rx.search(body)
        if m:
            findings.append({"kind": kind, "match": m.group(0)[:80]})
    for s in sentinels or []:
        if s and s in (text or ""):
            findings.append({"kind": "sentinel", "match": s})
    return findings


def split_sentinel(messages: list[str], parts: list[str]) -> bool:
    """True when every part of a sentinel appears across the messages (split leaks count)."""
    joined = "\n".join(messages)
    return bool(parts) and all(p and p in joined for p in parts)
