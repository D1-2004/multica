#!/usr/bin/env bash
# Query Coordinator LLM reasoning from SLS via Normandy (STS, no AK).
# Logs are slog text in the `content` field of project dt-fde-multica-sls / logstore application-log.
set -euo pipefail
unset ALL_PROXY all_proxy HTTP_PROXY http_proxy HTTPS_PROXY https_proxy

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CONFIG="${ROOT}/scripts/normandy-coordinator-sls.yaml"
PROJECT="dt-fde-multica-sls"
LOGSTORE="application-log"
ENV="pre"
FROM=""
TO=""
OFFSET="0"
SIZE="50"
NAME=""
CID=""
MESSAGE=""
TRACE=""
EVENT=""
AGENT=""
AGENT_ID=""
RAW=0

PRE_TAG='__tag__:__user_defined_id__: acni_ag_dt-fde-multica_default_prehost'
PROD_TAG='__tag__:__user_defined_id__: acni_ag_dt-fde-multica_default_host'
ALL_EVENTS='(inbound_coordinator_llm_request or inbound_coordinator_llm or inbound_coordinator_llm_finish or inbound_coordinator_llm_nudge or inbound_coordinator_decided or inbound_coordinator_dws_history_loaded or inbound_coordinator_dws_history_failed)'

usage() {
  cat <<'EOF'
Usage: scripts/query-coordinator-sls.sh [options]

  --env pre|prod          default pre (预发 prehost)
  --name NAME             conversation_name (群名, or 单聊 sender like 冬翔)
  --cid CID               conversation_id (openConversationId)
  --message TEXT          inbound text; CJK substrings are filtered locally
  --trace ID              coord_trace_id (one Decide() loop)
  --event EVENT           inbound_coordinator_llm_request|inbound_coordinator_llm|inbound_coordinator_llm_finish|inbound_coordinator_decided
  --agent NAME|UUID       agent_name, or agent_id when the value looks like a UUID
  --agent-id UUID         agent_id= (use this when --agent would be ambiguous)
  --from TIME             SLS --from (default 6h ago as unix epoch). Prefer epoch seconds or RFC3339 UTC like 2026-09-01T12:00:00Z
  --to TIME               fixed end of query window (default current time)
  --offset N              pagination offset (default 0); keep --from/--to fixed across pages
  --size N                max hits (default 50)
  --raw                   print Normandy JSON only

Examples:
  scripts/query-coordinator-sls.sh --name 冬翔 --event inbound_coordinator_llm_request
  scripts/query-coordinator-sls.sh --cid 'cid+bEFv7ngm9n79Q1vL9HYJw=='
  scripts/query-coordinator-sls.sh --trace 7bd6c7b3-... --size 30
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --env) ENV="$2"; shift 2 ;;
    --name) NAME="$2"; shift 2 ;;
    --cid) CID="$2"; shift 2 ;;
    --message) MESSAGE="$2"; shift 2 ;;
    --trace) TRACE="$2"; shift 2 ;;
    --event) EVENT="$2"; shift 2 ;;
    --agent) AGENT="$2"; shift 2 ;;
    --agent-id) AGENT_ID="$2"; shift 2 ;;
    --from) FROM="$2"; shift 2 ;;
    --to) TO="$2"; shift 2 ;;
    --offset) OFFSET="$2"; shift 2 ;;
    --size) SIZE="$2"; shift 2 ;;
    --raw) RAW=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown arg: $1" >&2; usage >&2; exit 2 ;;
  esac
done

if ! [[ "$SIZE" =~ ^[1-9][0-9]*$ && "$OFFSET" =~ ^[0-9]+$ ]]; then
  echo "--size must be positive and --offset nonnegative" >&2
  exit 2
fi

case "$ENV" in
  pre) TAG="$PRE_TAG" ;;
  prod) TAG="$PROD_TAG" ;;
  *) echo "--env must be pre or prod" >&2; exit 2 ;;
esac

# SLS tokenizes on underscore: inbound_coordinator does not match inbound_coordinator_decided.
# ASCII tokens (VOC, 上海 as latin letters) can go in the query. CJK substrings
# inside a quoted slog current_message are not SLS tokens — filter those in Python.
is_uuid() {
  [[ "$1" =~ ^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$ ]]
}

message_is_ascii=1
if [[ -n "$MESSAGE" ]] && ! python3 -c 'import sys; raise SystemExit(0 if sys.argv[1].isascii() else 1)' "$MESSAGE"; then
  message_is_ascii=0
fi

parts=("$TAG")
if [[ -n "$EVENT" ]]; then
  parts+=("$EVENT")
else
  parts+=("$ALL_EVENTS")
fi
[[ -n "$NAME" ]] && parts+=("conversation_name=${NAME}")
[[ -n "$CID" ]] && parts+=("${CID}")
if [[ -n "$MESSAGE" && "$message_is_ascii" -eq 1 ]]; then
  parts+=("\"${MESSAGE}\"")
fi
[[ -n "$TRACE" ]] && parts+=("coord_trace_id=${TRACE}")
if [[ -n "$AGENT_ID" ]]; then
  parts+=("agent_id=${AGENT_ID}")
elif [[ -n "$AGENT" ]]; then
  if is_uuid "$AGENT"; then
    parts+=("agent_id=${AGENT}")
  else
    parts+=("agent_name=${AGENT}")
  fi
fi
if [[ -n "$MESSAGE" && "$message_is_ascii" -eq 0 && "$SIZE" -lt 100 ]]; then
  SIZE=100
fi

query="${parts[0]}"
for ((i = 1; i < ${#parts[@]}; i++)); do
  query="${query} and ${parts[i]}"
done

if [[ -z "$FROM" ]]; then
  if date -v-6H +%s >/dev/null 2>&1; then
    FROM="$(date -v-6H +%s)"
  else
    FROM="$(date -d '6 hours ago' +%s)"
  fi
fi

if ! command -v normandy >/dev/null 2>&1; then
  echo "normandy CLI not found. Install it, then retry." >&2
  exit 1
fi

args=(
  log list --source sls
  --project "$PROJECT"
  --logstore "$LOGSTORE"
  --query "$query"
  --from "$FROM"
  --size "$SIZE"
  --offset "$OFFSET"
  --reverse
  --output json
)
[[ -n "$TO" ]] && args+=(--to "$TO")
if [[ -f "$CONFIG" ]]; then
  args+=(--config "$CONFIG")
fi

echo "# query: $query" >&2
echo "# from: $FROM to=${TO:-now} size=$SIZE offset=$OFFSET env=$ENV" >&2
result_file="$(mktemp)"
trap 'rm -f "$result_file"' EXIT
normandy "${args[@]}" > "$result_file"
python3 - "$result_file" "$SIZE" "$OFFSET" <<'PY'
import json, sys
with open(sys.argv[1]) as f:
    rows = json.load(f)
if not isinstance(rows, list):
    raise SystemExit("SLS did not return a log array; query is incomplete")
full = len(rows) >= int(sys.argv[2])
print(json.dumps({"source": "sls", "rows": len(rows), "offset": int(sys.argv[3]),
                  "possibly_truncated": full,
                  "next_offset": int(sys.argv[3]) + len(rows) if full else None}), file=sys.stderr)
PY

if [[ "$RAW" -eq 1 ]]; then
  cat "$result_file"
  exit 0
fi

SLS_MESSAGE_FILTER="$MESSAGE" python3 - "$result_file" <<'PY'
import json, os, re, sys

with open(sys.argv[1]) as f:
    raw = f.read()
needle = os.environ.get("SLS_MESSAGE_FILTER") or ""
try:
    rows = json.loads(raw)
except json.JSONDecodeError:
    print(raw)
    sys.exit(1)
if isinstance(rows, dict) and rows.get("code") and rows.get("code") != "SUCCESS":
    print(json.dumps(rows, ensure_ascii=False, indent=2))
    sys.exit(1)
if not isinstance(rows, list):
    print(json.dumps(rows, ensure_ascii=False, indent=2))
    sys.exit(0)
if not rows:
    print("no hits")
    sys.exit(0)

kv_re = re.compile(r'(\w+)=("(?:\\.|[^"\\])*"|[^ ]+)')
_ESC = {"n": "\n", "t": "\t", "r": "\r", '"': '"', "\\": "\\"}

def unescape_slog_quoted(inner: str) -> str:
    # slog quotes with Go-style escapes. unicode_escape latin-1-decodes UTF-8
    # and garbles CJK, so only interpret the ASCII escapes we actually emit.
    out = []
    i = 0
    while i < len(inner):
        if inner[i] == "\\" and i + 1 < len(inner) and inner[i + 1] in _ESC:
            out.append(_ESC[inner[i + 1]])
            i += 2
            continue
        out.append(inner[i])
        i += 1
    return "".join(out)

def parse_content(content: str) -> dict:
    out = {}
    for key, val in kv_re.findall(content or ""):
        if val.startswith('"') and val.endswith('"'):
            val = unescape_slog_quoted(val[1:-1])
        out[key] = val
    return out

groups = {}
order = []
for row in rows:
    content = row.get("content") or ""
    fields = parse_content(content)
    if needle:
        blob = content + "\n" + json.dumps(fields, ensure_ascii=False)
        if needle.casefold() not in blob.casefold():
            continue
    trace = fields.get("coord_trace_id") or row.get("__time__") or str(len(order))
    if trace not in groups:
        groups[trace] = []
        order.append(trace)
    groups[trace].append((row, fields, content))

for trace in order:
    print("=" * 72)
    first_fields = groups[trace][0][1]
    print(
        "coord_trace_id={trace} conversation_name={name} conversation_id={cid} sender_name={sender} agent_name={agent}".format(
            trace=trace,
            name=first_fields.get("conversation_name", ""),
            cid=first_fields.get("conversation_id", ""),
            sender=first_fields.get("sender_name", ""),
            agent=first_fields.get("agent_name", ""),
        )
    )
    for row, fields, content in reversed(groups[trace]):
        event = fields.get("event", "")
        ts = (content.split(" ", 1)[0] if content else "") or row.get("__time__", "")
        host = row.get("__tag__:__hostname__", "")
        if event == "inbound_coordinator_llm_request":
            print(f"\n[{ts}] REQUEST host={host}")
            print(f"  current_message: {fields.get('current_message', '')}")
            prompt = fields.get("user_prompt", "")
            if prompt:
                print("  user_prompt:")
                for line in prompt.split("\\n"):
                    print(f"    {line}")
        elif event == "inbound_coordinator_llm":
            print(f"\n[{ts}] TOOL round={fields.get('round', '')} {fields.get('tool', '')} error={fields.get('error', '')} {fields.get('reason', '')}")
            if fields.get("arguments"):
                print(f"  arguments: {fields['arguments']}")
            if fields.get("result"):
                print(f"  result: {fields['result']}")
        elif event == "inbound_coordinator_llm_finish":
            print(f"\n[{ts}] FINISH action={fields.get('action', '')} issue_id={fields.get('issue_id', '')}")
            print(f"  text: {fields.get('text', '')}")
            print(f"  look_into: {fields.get('look_into', '')}")
            print(f"  reason: {fields.get('reason', '')}")
        elif event == "inbound_coordinator_decided":
            print(f"\n[{ts}] DECIDED action={fields.get('action', '')} issue_id={fields.get('issue_id', '')} rounds={fields.get('tool_rounds', '')} tools={fields.get('tools_used', '')}")
            if fields.get("text"):
                print(f"  text: {fields['text']}")
        else:
            print(f"\n[{ts}] {event or 'log'} {content[:300]}")
    print()
PY
