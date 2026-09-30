#!/bin/bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
APP_ROOT="$(cd -- "$SCRIPT_DIR/.." && pwd)"
LOG_DIR="/home/admin/${APP_NAME}/logs"
RUN_DIR="/home/admin/${APP_NAME}/run"

mkdir -p "$LOG_DIR" "$RUN_DIR" "$APP_ROOT/data/uploads"
touch "$LOG_DIR/bootstrap.log"
echo "[multica][runtime] entering main.sh pid=$$ app_root=$APP_ROOT" >>"$LOG_DIR/bootstrap.log"
exec >>"$LOG_DIR/bootstrap.log" 2>&1
echo "[multica][runtime] bootstrap log redirected"
cd "$APP_ROOT"

RUNTIME_CONFIG_KEYS=(
  APP_ENV
  DATABASE_URL
  MULTICA_USER_DECISION_VERIFY_ON_BOOT
  JWT_SECRET
  MULTICA_RUNTIME_CONFIG_SOURCE
  MULTICA_RUNTIME_LLM_API_KEY
  FRONTEND_ORIGIN
  MULTICA_APP_URL
  MULTICA_PUBLIC_URL
  MULTICA_SITE_PUBLIC_URL
  MULTICA_A2A_PUSH_SECRET_KEY
  MULTICA_A2A_OPERATOR_EMAILS
  MULTICA_A2A_FORWARD_ALLOWED_ORIGINS
  MULTICA_A2A_FORWARD_REGISTRY_URLS
  MULTICA_A2A_FORWARD_REGISTRATION_SECRET
  CORS_ALLOWED_ORIGINS
  COOKIE_DOMAIN
  LOGIN_DINGTALK_ONLY
  LOGIN_PROVIDERS
  DINGTALK_CLIENT_ID
  DINGTALK_CLIENT_SECRET
  DINGTALK_AGENT_BASE_URL
  DINGTALK_DBASE_BINDING_PAGE_URL
  DINGTALK_DBASE_BINDING_ORIGIN
  DINGTALK_H5_CORP_ID
  DINGTALK_H5_AGENT_ID
  AGENT_MESSAGE_ROUTER_INTERNAL_URL
  AGENT_MESSAGE_ROUTER_SERVICE_CREDENTIAL
  MULTICA_FC_E2B_DWS_MESSAGE_POLICY_FINGERPRINTS
  MULTICA_AGENT_DISPATCH_CURRENT_KEY_ID
  MULTICA_AGENT_DISPATCH_KEYS
  MULTICA_MODEL_PROVIDER_SECRET_KEY
  MULTICA_MODEL_GATEWAY_ENABLED
  MULTICA_DINGTALK_SECRET_KEY
  MULTICA_DINGTALK_STREAM_CONNECTION_TARGET
  MULTICA_DINGTALK_REGISTRATION_BASE_URL
  MULTICA_DINGTALK_REGISTRATION_OUTGOING_URL
  MULTICA_LARK_SECRET_KEY
  MULTICA_SLACK_SECRET_KEY
  MULTICA_FC_E2B_ENABLED
  MULTICA_DSH_STORAGE_CONFIG
  MULTICA_DSH_SCHEDULE_DISPATCH_ENABLED
  MULTICA_FC_E2B_STABLE_PUBLISHER_USER_IDS
  MULTICA_FC_E2B_TEMPLATE
  MULTICA_FC_E2B_SERVER_URL
  MULTICA_FC_E2B_API_KEY
  MULTICA_FC_E2B_API_URL
  MULTICA_FC_E2B_DOMAIN
  MULTICA_FC_E2B_OPENAI_BASE_URL
  MULTICA_FC_E2B_OPENAI_API_KEY
  MULTICA_FC_E2B_OPENAI_MODELS
  MULTICA_FC_E2B_CLI_PATH
  MULTICA_PROCESS_MEMORY_SAMPLE
  MULTICA_FC_E2B_TIMEOUT_SECONDS
  MULTICA_FC_E2B_SANDBOX_READY_TIMEOUT
  MULTICA_ASB_ENABLED
  MULTICA_ASB_NETWORK_ALLOWLIST
  MULTICA_ASB_API_URL
  MULTICA_ASB_SERVER_URL
  MULTICA_ASB_OPENAI_BASE_URL
  MULTICA_ASB_OPENAI_API_KEY
  MULTICA_ASB_OPENAI_MODELS
  MULTICA_ASB_TIMEOUT_SECONDS
  MULTICA_ASB_READY_TIMEOUT
  MULTICA_ASB_COMMAND_READY_TIMEOUT
  MULTICA_ASB_WIREGUARD_READY_TIMEOUT
  MULTICA_ASB_RESOURCE_CPU
  MULTICA_ASB_RESOURCE_MEMORY
  MULTICA_ASB_WG_CLIENT_CREDENTIALS
  MULTICA_ASB_IDENTITY_SECRET_KEY
  MULTICA_ENTERPRISE_IDENTITY_ENABLED
  MULTICA_BUC_AUTHORIZE_URL
  MULTICA_BUC_TOKEN_URL
  MULTICA_BUC_ISSUER
  MULTICA_BUC_JWKS_URL
  MULTICA_BUC_CLIENT_ID
  MULTICA_BUC_CLIENT_SECRET
  MULTICA_BUC_AGENT_ID
  MULTICA_BUC_REDIRECT_URL
  MULTICA_BUC_AUTHORIZE_APPS
  MULTICA_ENTERPRISE_IDENTITY_OAUTH_ATTEMPT_TTL
  MULTICA_AUTHX_SERVICE_ID
  MULTICA_AUTHX_AUDIENCE
  MULTICA_AUTHX_TTL_SECONDS
  MULTICA_AUTHX_ENVIRONMENT
  MULTICA_IDEM_BASE_URL
  MULTICA_IDEM_TIMEOUT
  MULTICA_IDEM_OPERATOR_TRUST_DOMAIN
  MULTICA_IDEM_AGENT_TRUST_DOMAIN
  MULTICA_IDEM_AGENT_NAMESPACE
  MULTICA_IDEM_AIT_TTL_SECONDS
  MULTICA_SANDBOX_RELAY_SIGNING_KEY_ID
  MULTICA_SANDBOX_RELAY_SIGNING_PRIVATE_KEY
  MULTICA_SANDBOX_RELAY_VERIFY_KEYS
  MULTICA_SANDBOX_RELAY_MULTICA_UPSTREAM_URL
  MULTICA_SANDBOX_RELAY_AGENT_IDENTITY_UPSTREAM_URL
  MULTICA_SANDBOX_RELAY_MAX_INFLIGHT
  MULTICA_FDE_AGENT_REPOSITORY_URL
  MULTICA_FDE_AGENT_REPOSITORY_REF
  MULTICA_FDE_AGENT_SYNC_INTERVAL
  MULTICA_FDE_AGENT_ROLLOUT_BATCH_SIZE
  MULTICA_AGENT_IDENTITY_BASE_URL
  MULTICA_AGENT_IDENTITY_CONTROL_BASE_URL
  MULTICA_AGENT_IDENTITY_SANDBOX_BASE_URL
  MULTICA_AGENT_IDENTITY_TIMEOUT_SECONDS
  MULTICA_AGENT_IDENTITY_DEBUG_LOG_CONTEXT_TOKEN
  MULTICA_AGENT_IDENTITY_DEBUG_CONTEXT_TOKEN_AGENT_IDS
  MULTICA_AGENT_IDENTITY_DWS_CLIENT_SECRET
  MULTICA_DWS_HISTORY_MCP_URL
  MULTICA_DWS_HISTORY_CROSS_ORG_RENEW_AGENT_IDS
  MULTICA_DIAMOND_DATA_ID
  MULTICA_DIAMOND_GROUP
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
  S3_USE_PATH_STYLE
  OSS_AUTHZ_BUCKET
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
  MULTICA_SEMANTICA_MCP_URL
  MULTICA_SEMANTICA_MCP_BEARER_TOKEN
  MULTICA_SEMANTICA_MCP_TARGET_AGENT_ID
  MULTICA_INTERNAL_MCP_ALLOWED_HOST_SUFFIXES
  MULTICA_INTERNAL_MCP_SECRET_KEY
  MULTICA_INTERNAL_MCP_KEY_SOURCE
  MULTICA_CONNECTOR_OAUTH_CLIENT_NAME
  MULTICA_CONNECTOR_OAUTH_CALLBACK_ORIGIN
  MULTICA_CONNECTOR_OAUTH_FORWARD_ORIGINS
  REDIS_URL
  REDIS_AUTHZ_INSTANCE_ID
  REDIS_AUTHZ_ENDPOINT
  REDIS_DISABLE_CLIENT_NAME
  RATE_LIMIT_AUTH
  RATE_LIMIT_AUTH_VERIFY
  RATE_LIMIT_DSH_NATIVE_ACCESS
  RATE_LIMIT_TRUSTED_PROXIES
  REALTIME_METRICS_TOKEN
  MULTICA_LOG_TAIL_TOKEN
  MULTICA_TRUSTED_PROXIES
  GITHUB_APP_ID
  GITHUB_APP_PRIVATE_KEY
  GITHUB_APP_CLIENT_ID
  GITHUB_APP_CLIENT_SECRET
  GITHUB_API_BASE_URL
  GITHUB_APP_SLUG
  GITHUB_WEBHOOK_SECRET
  ALLOW_SIGNUP
  ALLOWED_EMAILS
  ALLOWED_EMAIL_DOMAINS
  FF_WORKSPACE_ACCESS_TOKENS
  FF_WORKSPACE_MCP_ENDPOINT_ENABLED
  LANGFUSE_PUBLIC_KEY
  LANGFUSE_SECRET_KEY
  LANGFUSE_BASE_URL
  LANGFUSE_HOST
  LANGFUSE_ENVIRONMENT
  LANGFUSE_TRACING_ENABLED
)

is_runtime_config_key() {
  local candidate="$1"
  if [[ "$candidate" =~ ^MULTICA_INTERNAL_MCP_BEARER_[A-F0-9]{32}$ ]]; then
    return 0
  fi
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
    "/home/admin/vmcommon/antx.properties"
    "/home/admin/conf/antx.properties"
    "/home/admin/cai/conf/antx.properties"
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
    echo "[multica][runtime] found Aone config file: $found_antx_path"
    cp "$found_antx_path" "$antx_file"
  else
    echo "[multica][runtime] no Aone config file found; using process environment"
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

required_runtime_keys=(DATABASE_URL JWT_SECRET)
if [[ "${MULTICA_RUNTIME_CONFIG_SOURCE:-}" == "diamond" ]]; then
  required_runtime_keys+=(MULTICA_RUNTIME_LLM_API_KEY)
else
  required_runtime_keys+=(FRONTEND_ORIGIN MULTICA_APP_URL)
fi

for required_key in "${required_runtime_keys[@]}"; do
  if [[ -n "${!required_key:-}" ]]; then
    echo "[multica][runtime] required config present: $required_key"
  else
    echo "[multica][runtime] required config missing: $required_key"
  fi
done

: "${DATABASE_URL:?DATABASE_URL is required}"
: "${JWT_SECRET:?JWT_SECRET is required}"
if [[ "${MULTICA_RUNTIME_CONFIG_SOURCE:-}" == "diamond" ]]; then
  : "${MULTICA_RUNTIME_LLM_API_KEY:?MULTICA_RUNTIME_LLM_API_KEY is required when MULTICA_RUNTIME_CONFIG_SOURCE=diamond}"
else
  : "${FRONTEND_ORIGIN:?FRONTEND_ORIGIN is required}"
  : "${MULTICA_APP_URL:?MULTICA_APP_URL is required}"
fi

export APP_ENV="${APP_ENV:-production}"
export BACKEND_PORT="${BACKEND_PORT:-8080}"
export FRONTEND_PORT="${FRONTEND_PORT:-3000}"
export PORT="$BACKEND_PORT"
export NODE_ENV=production
export HOSTNAME=0.0.0.0
export LOCAL_UPLOAD_DIR="${LOCAL_UPLOAD_DIR:-$APP_ROOT/data/uploads}"
export MULTICA_LOG_DIR="$LOG_DIR"
export AONE_HEALTH_PORT="${AONE_HEALTH_PORT:-6001}"
export PATH="/home/admin/${APP_NAME}/node/bin:$APP_ROOT/bin:$PATH"

dump_log_tail() {
  local label="$1"
  local file="$2"
  echo "[multica][runtime] ---- ${label} tail ----"
  if [[ -f "$file" ]]; then
    tail -n 120 "$file" || true
  else
    echo "[multica][runtime] ${file} does not exist"
  fi
}

process_patterns=(
  "$APP_ROOT/src/frontend-supervisor[.]cjs"
  "$APP_ROOT/bin/server"
  "node apps/web/server[.]js"
  "$RUN_DIR/health-server[.]js"
)

# The backend's bounded graceful-shutdown path can spend up to 53 seconds in
# sequence: HTTP drain (10s), Stream handoff (15s), webhook worker (5s),
# DingTalk inbox worker (5s), channel supervisor (15s), and metrics drain
# (3s). Keep a small scheduling margin so a rolling deploy does not SIGKILL
# the old backend before those durability and connection-handoff steps finish.
existing_process_shutdown_budget_seconds=60

processes_running() {
  local pattern
  for pattern in "${process_patterns[@]}"; do
    if pgrep -f "$pattern" >/dev/null 2>&1; then
      return 0
    fi
  done
  return 1
}

stop_existing_processes() {
  local pattern
  local attempt

  echo "[multica][runtime] stopping existing application processes"
  # Stop the restart owner first, otherwise a deploy can race with a respawn.
  pkill -TERM -f "$APP_ROOT/src/frontend-supervisor[.]cjs" >/dev/null 2>&1 || true
  for attempt in $(seq 1 8); do
    pgrep -f "$APP_ROOT/src/frontend-supervisor[.]cjs" >/dev/null 2>&1 || break
    sleep 1
  done
  pkill -KILL -f "$APP_ROOT/src/frontend-supervisor[.]cjs" >/dev/null 2>&1 || true
  for pattern in "${process_patterns[@]}"; do
    pkill -TERM -f "$pattern" >/dev/null 2>&1 || true
  done

  for attempt in $(seq 1 "$existing_process_shutdown_budget_seconds"); do
    if ! processes_running; then
      break
    fi
    sleep 1
  done

  if processes_running; then
    echo "[multica][runtime] force stopping remaining application processes"
    for pattern in "${process_patterns[@]}"; do
      pkill -KILL -f "$pattern" >/dev/null 2>&1 || true
    done
  fi

  rm -f "$RUN_DIR/backend.pid" "$RUN_DIR/frontend.pid" "$RUN_DIR/health.pid"
}

stop_current_processes() {
  local pid
  local attempt
  local running

  echo "[multica][runtime] stopping current application processes"
  for pid in "$health_pid" "$backend_pid"; do
    if kill -0 "$pid" 2>/dev/null; then
      kill -TERM "$pid" 2>/dev/null || true
    fi
  done

  for attempt in $(seq 1 10); do
    running=false
    for pid in "$health_pid" "$backend_pid"; do
      if kill -0 "$pid" 2>/dev/null; then
        running=true
        break
      fi
    done
    if [[ "$running" == false ]]; then
      return 0
    fi
    sleep 1
  done

  echo "[multica][runtime] force stopping current application processes"
  for pid in "$health_pid" "$backend_pid"; do
    if kill -0 "$pid" 2>/dev/null; then
      kill -KILL "$pid" 2>/dev/null || true
    fi
  done
}

current_release_is_healthy() {
  local release_id="${AONE_MIX_FLOW_INST_ID:-}"

  [[ -n "$release_id" ]] || return 1
  [[ -f "$RUN_DIR/release-id" ]] || return 1
  [[ "$(cat "$RUN_DIR/release-id")" == "$release_id" ]] || return 1
  curl -fsS "http://127.0.0.1:${BACKEND_PORT}/healthz" >/dev/null 2>&1 || return 1
  curl -fsS "http://127.0.0.1:${FRONTEND_PORT}/" >/dev/null 2>&1 || return 1
  curl -fsS "http://127.0.0.1:${AONE_HEALTH_PORT}/check.node" >/dev/null 2>&1 || return 1
}

start_processes() {
  echo "[multica][runtime] starting backend"
  nohup setsid "$APP_ROOT/bin/server" </dev/null >>"$LOG_DIR/backend.log" 2>&1 &
  backend_pid=$!
  echo "$backend_pid" >"$RUN_DIR/backend.pid"

  echo "[multica][runtime] starting frontend supervisor and health server"
  nohup setsid node "$APP_ROOT/src/frontend-supervisor.cjs" </dev/null >>"$LOG_DIR/health.log" 2>&1 &
  health_pid=$!
  echo "$health_pid" >"$RUN_DIR/health.pid"

}

wait_for_startup() {
  local attempt
  for attempt in $(seq 1 60); do
    if ! kill -0 "$backend_pid" 2>/dev/null; then
      echo "[multica][runtime] backend exited during startup"
      dump_log_tail "backend" "$LOG_DIR/backend.log"
      return 1
    fi
    if ! kill -0 "$health_pid" 2>/dev/null; then
      echo "[multica][runtime] health server exited during startup"
      dump_log_tail "health" "$LOG_DIR/health.log"
      return 1
    fi

    if curl -fsS "http://127.0.0.1:${AONE_HEALTH_PORT}/check.node" >/dev/null 2>&1; then
      echo "[multica][runtime] startup checks passed"
      return 0
    fi

    sleep 1
  done

  echo "[multica][runtime] startup checks timed out"
  dump_log_tail "backend" "$LOG_DIR/backend.log"
  dump_log_tail "frontend" "$LOG_DIR/frontend.log"
  dump_log_tail "health" "$LOG_DIR/health.log"
  return 1
}

exec 9>"$RUN_DIR/start.lock"
flock -x 9

if current_release_is_healthy; then
  echo "[multica][runtime] release ${AONE_MIX_FLOW_INST_ID} already healthy; skipping duplicate start"
  exit 0
fi

stop_existing_processes

# The Aone release order owns this automatic pre-start migration step.
echo "[multica][runtime] running migrations"
"$APP_ROOT/bin/migrate" up
echo "[multica][runtime] migrations completed"

# Explicit preproduction-only verification uses the migrated database.
bash "$APP_ROOT/src/verify-user-decisions.sh" "$APP_ROOT/bin/userdecision-verify"

start_processes
if ! wait_for_startup; then
  stop_current_processes
  exit 1
fi

if [[ -n "${AONE_MIX_FLOW_INST_ID:-}" ]]; then
  printf '%s' "$AONE_MIX_FLOW_INST_ID" >"$RUN_DIR/release-id"
fi

flock -u 9
exec 9>&-
echo "[multica][runtime] detached application processes are healthy"
