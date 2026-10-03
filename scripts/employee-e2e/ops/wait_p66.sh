#!/bin/bash
# Usage: scripts/employee-e2e/ops/wait_p66.sh <runId> <outfile>
# Polls pipeline-66 run <runId> until 预发部署 settles (SUCCESS/FAILED/CANCEL) or the run ends; max 40 min.
# Writes the final `a1 cd-pipeline run get` JSON to <outfile>. Run it in the background; do not chain sleeps.
unset HTTP_PROXY HTTPS_PROXY ALL_PROXY http_proxy https_proxy all_proxy
run=$1; out=$2
[ -n "$run" ] && [ -n "$out" ] || { echo "usage: $0 <runId> <outfile>"; exit 2; }
for i in $(seq 1 80); do
  perl -e 'alarm 170; exec @ARGV' a1 cd-pipeline run get "$run" --format json > "$out.tmp" 2>&1
  verdict=$(python3 - "$out.tmp" <<'PY'
import json,sys
try:
    d=json.load(open(sys.argv[1])); r=d.get('data',d)
except Exception as e:
    print('retry'); sys.exit()
st={s.get('name'):s.get('status') for s in (r.get('stages') or [])}
dep=st.get('预发部署'); top=r.get('status')
if top in ('CANCEL','CANCELED','FAILED','FAIL'): print('done:'+str(top)); sys.exit()
if any(v in ('FAILED','FAIL','CANCEL','CANCELED') for v in st.values()): print('done:stage-failed'); sys.exit()
if dep=='SUCCESS': print('done:deployed'); sys.exit()
print('wait:'+','.join(f"{k}={v}" for k,v in st.items()))
PY
)
  echo "$(date +%H:%M:%S) $verdict"
  case "$verdict" in done:*) cp "$out.tmp" "$out"; exit 0;; esac
  sleep 30
done
exit 1
