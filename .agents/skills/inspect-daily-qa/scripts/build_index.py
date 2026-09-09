#!/usr/bin/env python3
"""Join downloaded inspection evidence without network access or business writes."""
import argparse
from collections import Counter
from datetime import datetime
import json
import os
from pathlib import Path
import re


def read(path):
    return json.loads(Path(path).read_text())


def slog_fields(row):
    if "content" not in row:
        return row
    fields = {}
    for key, value in re.findall(r'(\w+)=("(?:\\.|[^"\\])*"|[^ ]+)', row["content"]):
        if value.startswith('"'):
            try:
                value = json.loads(value)
            except ValueError:
                value = value[1:-1]
        fields[key] = value
    fields["timestamp"] = row.get("__time__")
    fields["host_environment_tag"] = row.get("__tag__:__user_defined_id__")
    return fields


def dws_rows(data):
    if "messages" in data:
        return data["messages"], {
            key: data.get(key) for key in ("complete", "hasMore", "pagesFetched", "failedCount", "truncated")
        }
    if not isinstance(data.get("result"), dict):
        raise ValueError("DWS has neither messages nor a result object; inspect the error first")
    paging = data.get("paging", {})
    result = data["result"]
    rows = []
    for conversation in result.get("conversationMessagesList", []):
        for message in conversation.get("messages", []):
            rows.append({
                "messageId": message.get("openMessageId"),
                "conversationId": message.get("openConversationId") or conversation.get("openConversationId"),
                "conversationName": conversation.get("title"),
                "senderId": message.get("senderOpenDingTalkId"),
                "time": message.get("createTime"),
            })
    complete = (data.get("success") is True and paging.get("hasMore") is False
                and paging.get("truncated") is False and result.get("hasMore") is False)
    return rows, {"complete": complete, "pagesFetched": paging.get("pages"),
                  "hasMore": paging.get("hasMore"), "truncated": paging.get("truncated")}


def unique_by(rows, key, latest=False):
    unique = {}
    missing = 0
    for row in rows:
        identity = row.get(key)
        if not identity:
            missing += 1
            continue
        previous = unique.get(identity)
        if previous is None or not latest or str(row.get("timestamp", "")) > str(previous.get("timestamp", "")):
            unique[identity] = row
    return unique, {"raw_count": len(rows), "unique_count": len(unique),
                    "duplicate_rows": len(rows) - len(unique) - missing, "missing_id_rows": missing}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--dws", required=True, action="append", help="Repeat for by-ID supplements to the daily ledger")
    parser.add_argument("--sls", required=True, action="append", help="Repeat for fixed-window SLS pages/event sets")
    parser.add_argument("--langfuse", required=True, help="Trace array or a public API data/meta envelope")
    parser.add_argument("--self-id", required=True, help="Verified employee openDingTalkId")
    parser.add_argument("--from", dest="start", required=True)
    parser.add_argument("--to", dest="end", required=True)
    parser.add_argument("--output", required=True)
    args = parser.parse_args()
    start = datetime.fromisoformat(args.start.replace("Z", "+00:00"))
    end = datetime.fromisoformat(args.end.replace("Z", "+00:00"))
    if start.tzinfo is None or end.tzinfo is None or end <= start:
        parser.error("--from and --to must have offsets and form a positive window")

    messages, coverage = [], []
    for source in args.dws:
        rows, ledger = dws_rows(read(source))
        messages.extend(rows)
        coverage.append({"file": str(source), **ledger})
    messages, dws_counts = unique_by(messages, "messageId")
    fields = []
    for path in args.sls:
        rows = read(path)
        if not isinstance(rows, list):
            parser.error("SLS must be a raw log array, not an error response")
        fields.extend(slog_fields(row) for row in rows)
    # Repeated event reads are separate observations, not separate conversations.
    turns = {}
    for row in fields:
        trace = row.get("coord_trace_id")
        if not trace:
            continue
        turn = turns.setdefault(trace, {"coord_trace_id": trace, "events": Counter(), "actions": Counter()})
        turn["events"][row.get("event", "unknown")] += 1
        if row.get("action"):
            turn["actions"][row["action"]] += 1
        for key in ("evidence_id", "conversation_id", "conversation_name", "agent_id", "policy_version", "host_environment_tag"):
            if row.get(key):
                turn[key] = row[key]
    lf = read(args.langfuse)
    lf_rows = lf.get("data", []) if isinstance(lf, dict) else lf
    if not isinstance(lf_rows, list):
        parser.error("Langfuse must be a trace array or data/meta envelope")
    traces, lf_counts = unique_by(lf_rows, "id", latest=True)
    if isinstance(lf, dict):
        lf_counts["api_meta"] = lf.get("meta")
    lf_counts["coverage"] = "provided_files_only; check acquisition stats for truncation"
    for trace_id, turn in turns.items():
        turn["langfuse_in_provided_files"] = trace_id.replace("-", "") in traces
        turn["inbound_message_in_provided_dws"] = turn.get("evidence_id") in messages
    memory = []
    for trace in traces.values():
        meta = trace.get("metadata") or {}
        if trace.get("name") != "scene_memory_flush":
            continue
        memory.append({"trace_id": trace["id"], "timestamp": trace.get("timestamp"),
                       **{key: meta.get(key) for key in ("agent_id", "scene_key", "attempt", "status", "error_code", "committed")}})
    output = {
        "window": {"from": args.start, "to": args.end, "note": "source acquisition window; not inferred from trace root timestamps"},
        "dws": {**dws_counts, "coverage": coverage,
                "outbound_count": sum(row.get("senderId") == args.self_id for row in messages.values()),
                "conversation_count": len({row.get("conversationId") for row in messages.values()})},
        "sls": {"raw_count": len(fields), "unique_coordinator_traces": len(turns),
                "coverage": "provided_files_only; verify fixed-window pagination ledger"},
        "langfuse": lf_counts,
        "coordinator_turns": list(turns.values()),
        "memory_attempts": memory,
        "conclusion_guard": "Missing joins are investigation leads, not proof of lost execution or delivery. No automatic outbound matching or QA verdict.",
    }
    path = Path(args.output)
    path.parent.mkdir(parents=True, exist_ok=True)
    # Refuse overwrites so raw evidence cannot accidentally be replaced by an index.
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "w") as stream:
        json.dump(output, stream, ensure_ascii=False, indent=2)
        stream.write("\n")
    print(json.dumps({key: output[key] for key in ("dws", "sls", "langfuse")}, ensure_ascii=False))


if __name__ == "__main__":
    main()
