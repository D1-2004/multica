#!/usr/bin/env bash
# Query Langfuse v3 public API for Coordinator / sandbox traces.
# Credentials: ~/.grok/langfuse.env (never commit). Host is self-hosted
# unify-aipilot; use /api/public/traces not the Cloud v2 observations CLI.
set -euo pipefail
unset ALL_PROXY all_proxy HTTP_PROXY http_proxy HTTPS_PROXY https_proxy

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
exec python3 "$ROOT/scripts/query-langfuse.py" "$@"
