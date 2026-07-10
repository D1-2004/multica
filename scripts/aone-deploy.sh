#!/usr/bin/env bash
set -euo pipefail

# ==========================================================================
# Submit the current branch to the Aone publish pipeline and wait for it.
# Usage: bash scripts/aone-deploy.sh [cr-id]
#
# Flow: ensure an open CR exists for the current branch -> submit it into
# the publish pipeline -> trigger the pipeline -> poll until settled.
# Pipeline 66 = 预发, shown on the web as
# https://cd.aone.alibaba-inc.com/unite/micro/publish/app/342160?flowId=1005452
# (65 = 日常, 67 = 正式; override with AONE_PIPELINE_ID).
#
# Requires the a1 CLI (logged in via `a1 auth login`) and intranet/VPN access.
# ==========================================================================

# a1 only talks to intranet hosts; an external HTTP proxy breaks it with 502s.
unset ALL_PROXY all_proxy HTTP_PROXY http_proxy HTTPS_PROXY https_proxy

APP_ID="${AONE_APP_ID:-342160}"
PIPELINE_ID="${AONE_PIPELINE_ID:-66}"
CR_ID="${1:-}"

if ! command -v a1 >/dev/null 2>&1; then
  echo "a1 CLI not found. Install it and run 'a1 auth login' first."
  exit 1
fi

BRANCH="$(git branch --show-current)"
if [ -z "$BRANCH" ]; then
  echo "Detached HEAD; check out a branch before deploying."
  exit 1
fi

# The pipeline builds from the remote branch, so unpushed commits would be
# silently left out of the release.
git fetch origin "$BRANCH" --quiet || true
if [ -n "$(git log "origin/$BRANCH..$BRANCH" --oneline 2>/dev/null)" ]; then
  echo "Local '$BRANCH' has commits not pushed to origin. Push first:"
  git log "origin/$BRANCH..$BRANCH" --oneline
  exit 1
fi

if [ -z "$CR_ID" ]; then
  echo "Looking for an open CR on branch '$BRANCH' (app $APP_ID)..."
  CR_JSON="$(a1 app cr list --app "$APP_ID" --all --format json 2>/dev/null || true)"
  if command -v jq >/dev/null 2>&1 && [ -n "$CR_JSON" ]; then
    CR_ID="$(printf '%s' "$CR_JSON" \
      | jq -r --arg b "$BRANCH" \
        '[.[] | select((.branchName? // []) | index($b))] | (first.crId // empty)' \
      2>/dev/null || true)"
  fi

  if [ -z "$CR_ID" ]; then
    echo "No open CR found; creating one on existing branch '$BRANCH'..."
    LAST_COMMIT_SUBJECT="$(git log -1 --pretty=%s)"
    CR_ID="$(a1 app cr create "deploy: $LAST_COMMIT_SUBJECT" \
      --existing-branch "$BRANCH" --app "$APP_ID" --quiet)"
  fi
fi

echo "Using CR $CR_ID; submitting into pipeline $PIPELINE_ID..."
# Submitting a CR that is already in the flow fails; that is fine, we still
# want to (re)trigger the pipeline below.
if ! a1 app cr submit "$CR_ID" --app "$APP_ID" --pipeline-id "$PIPELINE_ID"; then
  echo "Submit failed (CR may already be in the flow); continuing to trigger."
fi

echo "Triggering pipeline $PIPELINE_ID and waiting until settled..."
a1 app pipeline run --app "$APP_ID" --pipeline-id "$PIPELINE_ID" --wait-until-settled

echo "Done. Inspect details with:"
echo "  a1 app pipeline status --app $APP_ID --pipeline-id $PIPELINE_ID"
