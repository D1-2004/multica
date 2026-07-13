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
  REDIS_AUTHZ_INSTANCE_ID
  REDIS_DISABLE_CLIENT_NAME
  RATE_LIMIT_AUTH
  RATE_LIMIT_AUTH_VERIFY
  RATE_LIMIT_TRUSTED_PROXIES
  REALTIME_METRICS_TOKEN
  MULTICA_LOG_TAIL_TOKEN
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

for required_key in DATABASE_URL JWT_SECRET FRONTEND_ORIGIN MULTICA_APP_URL; do
  if [[ -n "${!required_key:-}" ]]; then
    echo "[multica][runtime] required config present: $required_key"
  else
    echo "[multica][runtime] required config missing: $required_key"
  fi
done

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

write_health_server() {
  cat >"$RUN_DIR/health-server.js" <<'NODE'
const http = require("http");

const backendPort = process.env.BACKEND_PORT || "8080";
const frontendPort = process.env.FRONTEND_PORT || "3000";
const healthPort = Number(process.env.AONE_HEALTH_PORT || "6001");

function probe(port, path) {
  return new Promise((resolve) => {
    const req = http.get({ hostname: "127.0.0.1", port, path, timeout: 1500 }, (res) => {
      res.resume();
      res.on("end", () => resolve(res.statusCode >= 200 && res.statusCode < 500));
    });
    req.on("timeout", () => {
      req.destroy();
      resolve(false);
    });
    req.on("error", () => resolve(false));
  });
}

const server = http.createServer(async (req, res) => {
  const path = new URL(req.url, "http://127.0.0.1").pathname;
  if (path !== "/check.node" && path !== "/healthz") {
    res.writeHead(404, { "content-type": "text/plain" });
    res.end("not found");
    return;
  }

  const [backendReady, frontendReady] = await Promise.all([
    probe(backendPort, "/healthz"),
    probe(frontendPort, "/"),
  ]);
  if (backendReady && frontendReady) {
    res.writeHead(200, { "content-type": "text/plain" });
    res.end("success");
    return;
  }

  res.writeHead(503, { "content-type": "text/plain" });
  res.end(`backend=${backendReady} frontend=${frontendReady}`);
});

server.listen(healthPort, "0.0.0.0", () => {
  console.log(`multica health server listening on ${healthPort}`);
});

process.on("SIGTERM", () => server.close(() => process.exit(0)));
process.on("SIGINT", () => server.close(() => process.exit(0)));
NODE
}

process_patterns=(
  "$APP_ROOT/bin/server"
  "node apps/web/server[.]js"
  "$RUN_DIR/health-server[.]js"
)

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
  for pattern in "${process_patterns[@]}"; do
    pkill -TERM -f "$pattern" >/dev/null 2>&1 || true
  done

  for attempt in $(seq 1 10); do
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
  for pid in "$backend_pid" "$frontend_pid" "$health_pid"; do
    if kill -0 "$pid" 2>/dev/null; then
      kill -TERM "$pid" 2>/dev/null || true
    fi
  done

  for attempt in $(seq 1 10); do
    running=false
    for pid in "$backend_pid" "$frontend_pid" "$health_pid"; do
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
  for pid in "$backend_pid" "$frontend_pid" "$health_pid"; do
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
  nohup setsid "$APP_ROOT/bin/server" </dev/null >"$LOG_DIR/backend.log" 2>&1 &
  backend_pid=$!
  echo "$backend_pid" >"$RUN_DIR/backend.pid"

  echo "[multica][runtime] starting frontend"
  (
    cd "$APP_ROOT/web"
    PORT="$FRONTEND_PORT" HOSTNAME=0.0.0.0 nohup setsid node apps/web/server.js </dev/null >"$LOG_DIR/frontend.log" 2>&1 &
    echo $! >"$RUN_DIR/frontend.pid"
  )
  frontend_pid="$(cat "$RUN_DIR/frontend.pid")"

  write_health_server
  echo "[multica][runtime] starting Aone health server"
  nohup setsid node "$RUN_DIR/health-server.js" </dev/null >"$LOG_DIR/health.log" 2>&1 &
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
    if ! kill -0 "$frontend_pid" 2>/dev/null; then
      echo "[multica][runtime] frontend exited during startup"
      dump_log_tail "frontend" "$LOG_DIR/frontend.log"
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

echo "[multica][runtime] running migrations"
"$APP_ROOT/bin/migrate" up
echo "[multica][runtime] migrations completed"

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
