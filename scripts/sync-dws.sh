#!/usr/bin/env bash
# Copies the DWS module from a dws-for-tag checkout into server/pkg/dws as
# source. dws-for-tag (github.com/xdxer/dws-for-tag) is the only place to
# edit it; rerun this script to pick changes up.
#
#   scripts/sync-dws.sh <dws-for-tag checkout> <commit>
#
# The whole dws/ tree is copied: dws (the gateway client, credentials and
# the token store interface), dws/events (backend event connections),
# dws/redisstore (Redis stores for events and tokens) and dws/clicompat (the
# dws CLI's requests and outputs, built from dingtalk-workspace-cli source
# under Apache-2.0; see dws/clicompat/NOTICE).
set -euo pipefail

src=${1:?usage: scripts/sync-dws.sh <dws-for-tag checkout> <commit>}
commit=${2:?usage: scripts/sync-dws.sh <dws-for-tag checkout> <commit>}
root=$(cd "$(dirname "$0")/.." && pwd)
dst="$root/server/pkg/dws"

rm -rf "$dst"
cp -R "$src/dws" "$dst"
find "$dst" -name '*.go' | while read -r f; do
  sed -i.bak \
    -e "s#github.com/xdxer/dws-for-tag/dws#github.com/multica-ai/multica/server/pkg/dws#g" \
    -e '/^\/\/go:generate /d' \
    "$f"
  rm -f "$f.bak"
done

cat >"$dst/SYNC.md" <<EOF
# Synced source

\`server/pkg/dws\` is a copy of the \`dws\` tree of
github.com/xdxer/dws-for-tag at commit \`$commit\`, made by
\`scripts/sync-dws.sh\`. Edit it there and resync; local edits are
overwritten. \`clicompat\` carries dingtalk-workspace-cli code under
Apache-2.0; see \`clicompat/NOTICE\`.
EOF
gofmt -w "$dst"
echo "synced dws-for-tag@$commit into $dst"
