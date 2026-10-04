#!/bin/bash
# Usage: scripts/employee-e2e/ops/verify_deploy.sh <sha> <since-ISO, e.g. 2026-10-03T18:30:00+08:00>
# Proves a 预发 deploy (pipeline 66): the latest app-342160 release branch contains <sha>;
# both 预发 pods logged 'server starting' after <since> (SLS); deployment-fence replicas + marker.
# Run from any checkout of this repo that has the `aone` remote. The fence token is read from the
# 预发 env trait at run time and never printed.
unset HTTP_PROXY HTTPS_PROXY ALL_PROXY http_proxy https_proxy all_proxy
sha=$1; since=$2
[ -n "$sha" ] && [ -n "$since" ] || { echo "usage: $0 <sha> <since-ISO>"; exit 2; }
cd "$(git -C "$(dirname "$0")" rev-parse --show-toplevel)" || exit 2
rel=$(git ls-remote aone 'refs/heads/releases/*342160*' | sort -k2 | tail -1)
relsha=$(echo "$rel" | awk '{print $1}'); relname=$(echo "$rel" | awk '{print $2}' | sed 's#refs/heads/##')
git fetch -q aone "$relname" 2>/dev/null
if git merge-base --is-ancestor "$sha" "$relsha"; then echo "release $relname @ ${relsha:0:10} CONTAINS $sha"; else echo "release $relname @ ${relsha:0:10} does NOT contain $sha"; fi
echo "--- server starting since $since"
normandy log list --source sls --project dt-fde-multica-sls --logstore application-log \
  --query '__tag__:__user_defined_id__: acni_ag_dt-fde-multica_default_prehost and "server starting"' \
  --from "$since" -o json </dev/null 2>/dev/null | python3 -c '
import json,sys
raw=sys.stdin.read()
try: d=json.loads(raw)
except Exception: print(raw[:600]); sys.exit()
rows=d if isinstance(d,list) else d.get("logs") or d.get("data") or []
seen={}
for r in rows:
    if "server starting" not in str(r.get("content","")): continue
    host=r.get("__tag__:__hostname__") or r.get("__hostname__") or r.get("hostname") or r.get("__source__") or "?"
    t=r.get("__time__") or r.get("time") or ""
    import datetime
    seen.setdefault(host,[]).append(datetime.datetime.fromtimestamp(int(t)).strftime("%H:%M:%S") if str(t).isdigit() else str(t))
for h,ts in seen.items(): print(h, sorted(ts))
print("pods restarted:",len(seen))
'
tok=$(a1 env trait get --env-id 6721850 --trait-key env-vars --format json 2>/dev/null | python3 -c '
import json,sys,re
raw=sys.stdin.read()
txt=raw.replace(chr(92)+chr(34), chr(34))
m=re.search(r"\{\s*\"value\"\s*:\s*\"([^\"]+)\"\s*,\s*\"key\"\s*:\s*\"MULTICA_LOG_TAIL_TOKEN\"", txt) or re.search(r"\"key\"\s*:\s*\"MULTICA_LOG_TAIL_TOKEN\"\s*,\s*\"value\"\s*:\s*\"([^\"]+)\"", txt)
print(m.group(1) if m else "")')
[ -z "$tok" ] && { echo "fence: token not found"; exit 0; }
curl -s --noproxy '*' -H "Authorization: Bearer $tok" https://pre-fde-workbench.dingtalk.com/api/internal/deployment-fence | python3 -c '
import json,sys
d=json.load(sys.stdin)
print("fence state:",d.get("state"),"rev",d.get("revision"))
for r in d.get("replicas",[]) or d.get("live_replicas",[]) or []:
    print(" replica", r.get("instance_id") or r.get("id"), r.get("build_id"), r.get("last_seen_at") or r.get("heartbeat_at"))
' 2>&1 | head -10
