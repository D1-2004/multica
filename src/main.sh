#!/bin/bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
APP_ROOT="$(cd -- "$SCRIPT_DIR/.." && pwd)"
LOG_DIR="/home/admin/${APP_NAME}/logs"
ANTX_PACKAGE="/home/admin/${APP_NAME}/target/${APP_NAME}.tgz"

mkdir -p "$LOG_DIR" "$APP_ROOT/data/uploads"
cd "$APP_ROOT"

RUNTIME_CONFIG_KEYS=(
  APP_ENV
  DATABASE_URL
  JWT_SECRET
  FRONTEND_ORIGIN
  MULTICA_APP_URL
  MULTICA_PUBLIC_URL
  CORS_ALLOWED_ORIGINS
  COOKIE_DOMAIN
  LOGIN_DINGTALK_ONLY
  LOGIN_PROVIDERS
  DINGTALK_CLIENT_ID
  DINGTALK_CLIENT_SECRET
  DINGTALK_AGENT_BASE_URL
  MULTICA_DINGTALK_SECRET_KEY
  MULTICA_LARK_SECRET_KEY
  MULTICA_SLACK_SECRET_KEY
  MULTICA_DWS_SECRET_KEY
  MULTICA_DWS_CLI_PATH
  RESEND_API_KEY
  RESEND_FROM_EMAIL
  SMTP_HOST
  SMTP_PORT
  SMTP_USERNAME
  SMTP_PASSWORD
  SMTP_TLS
  SMTP_TLS_INSECURE
  SMTP_EHLO_NAME
  S3_BUCKET
  S3_REGION
  AWS_ACCESS_KEY_ID
  AWS_SECRET_ACCESS_KEY
  AWS_ENDPOINT_URL
  ATTACHMENT_DOWNLOAD_MODE
  ATTACHMENT_DOWNLOAD_URL_TTL
  CLOUDFRONT_KEY_PAIR_ID
  CLOUDFRONT_PRIVATE_KEY_SECRET
  CLOUDFRONT_PRIVATE_KEY
  CLOUDFRONT_DOMAIN
  LOCAL_UPLOAD_DIR
  LOCAL_UPLOAD_BASE_URL
  REDIS_URL
  REDIS_DISABLE_CLIENT_NAME
  RATE_LIMIT_AUTH
  RATE_LIMIT_AUTH_VERIFY
  RATE_LIMIT_TRUSTED_PROXIES
  REALTIME_METRICS_TOKEN
  MULTICA_TRUSTED_PROXIES
  GITHUB_APP_SLUG
  GITHUB_WEBHOOK_SECRET
  ALLOW_SIGNUP
  ALLOWED_EMAILS
  ALLOWED_EMAIL_DOMAINS
)

is_runtime_config_key() {
  local candidate="$1"
  local config_key
  for config_key in "${RUNTIME_CONFIG_KEYS[@]}"; do
    if [[ "$candidate" == "$config_key" ]]; then
      return 0
    fi
  done
  return 1
}

load_antx_runtime_config() {
  local antx_candidates=(
    "$APP_ROOT/antx.properties"
    "$APP_ROOT/conf/antx.properties"
    "/home/admin/${APP_NAME}/antx.properties"
    "/home/admin/${APP_NAME}/conf/antx.properties"
    "/home/admin/antx.properties"
  )
  local antx_file
  local antx_candidate
  local found_antx_path=""
  local loaded_keys=()

  antx_file="$(mktemp)"

  for antx_candidate in "${antx_candidates[@]}"; do
    if [[ -f "$antx_candidate" ]]; then
      found_antx_path="$antx_candidate"
      break
    fi
  done

  if [[ -n "$found_antx_path" ]]; then
    cp "$found_antx_path" "$antx_file"
  elif [[ -f "$ANTX_PACKAGE" ]]; then
    local antx_entry=""
    antx_entry="$(
      tar -tf "$ANTX_PACKAGE" 2>/dev/null | awk '
        $0 == "antx.properties" || $0 == "./antx.properties" || $0 ~ /\/antx\.properties$/ {
          print
          exit
        }
      '
    )"
    if [[ -n "$antx_entry" ]]; then
      if ! tar -xOf "$ANTX_PACKAGE" "$antx_entry" >"$antx_file" 2>/dev/null; then
        : >"$antx_file"
      fi
    fi
  fi

  while IFS= read -r line || [[ -n "$line" ]]; do
    [[ -z "$line" || "$line" == \#* || "$line" != *=* ]] && continue
    local key="${line%%=*}"
    local value="${line#*=}"
    key="${key%$'\r'}"
    value="${value%$'\r'}"
    if is_runtime_config_key "$key" && [[ -z "${!key:-}" ]]; then
      export "$key=$value"
      loaded_keys+=("$key")
    fi
  done < "$antx_file"

  rm -f "$antx_file"
  if [[ ${#loaded_keys[@]} -gt 0 ]]; then
    echo "[multica][runtime] loaded Aone config keys: ${loaded_keys[*]}"
  else
    echo "[multica][runtime] no Aone runtime config keys loaded"
  fi
}

load_antx_runtime_config

: "${DATABASE_URL:?DATABASE_URL is required}"
: "${JWT_SECRET:?JWT_SECRET is required}"
: "${FRONTEND_ORIGIN:?FRONTEND_ORIGIN is required}"
: "${MULTICA_APP_URL:?MULTICA_APP_URL is required}"

export APP_ENV="${APP_ENV:-production}"
export BACKEND_PORT="${BACKEND_PORT:-8080}"
export FRONTEND_PORT="${FRONTEND_PORT:-3000}"
export PORT="$BACKEND_PORT"
export NODE_ENV=production
export HOSTNAME=0.0.0.0
export LOCAL_UPLOAD_DIR="${LOCAL_UPLOAD_DIR:-$APP_ROOT/data/uploads}"
export MULTICA_DWS_CLI_PATH="${MULTICA_DWS_CLI_PATH:-/usr/local/bin/dws}"
export PATH="/home/admin/${APP_NAME}/node/bin:$APP_ROOT/bin:$PATH"

"$APP_ROOT/bin/migrate" up

"$APP_ROOT/bin/server" >"$LOG_DIR/backend.log" 2>&1 &
backend_pid=$!

(
  cd "$APP_ROOT/web"
  PORT="$FRONTEND_PORT" HOSTNAME=0.0.0.0 node apps/web/server.js
) >"$LOG_DIR/frontend.log" 2>&1 &
frontend_pid=$!

terminate_children() {
  kill "$backend_pid" "$frontend_pid" 2>/dev/null || true
}

trap terminate_children TERM INT

while kill -0 "$backend_pid" 2>/dev/null && kill -0 "$frontend_pid" 2>/dev/null; do
  sleep 2
done

if ! kill -0 "$backend_pid" 2>/dev/null; then
  wait "$backend_pid"
  exit_code=$?
else
  wait "$frontend_pid"
  exit_code=$?
fi

terminate_children
exit "$exit_code"
