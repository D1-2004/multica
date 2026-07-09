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
    res.end("ok");
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

start_processes() {
  echo "[multica][runtime] starting backend"
  nohup "$APP_ROOT/bin/server" >"$LOG_DIR/backend.log" 2>&1 &
  backend_pid=$!
  echo "$backend_pid" >"$RUN_DIR/backend.pid"

  echo "[multica][runtime] starting frontend"
  (
    cd "$APP_ROOT/web"
    PORT="$FRONTEND_PORT" HOSTNAME=0.0.0.0 nohup node apps/web/server.js >"$LOG_DIR/frontend.log" 2>&1 &
    echo $! >"$RUN_DIR/frontend.pid"
  )
  frontend_pid="$(cat "$RUN_DIR/frontend.pid")"

  write_health_server
  echo "[multica][runtime] starting Aone health server"
  nohup node "$RUN_DIR/health-server.js" >"$LOG_DIR/health.log" 2>&1 &
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

echo "[multica][runtime] running migrations"
"$APP_ROOT/bin/migrate" up
echo "[multica][runtime] migrations completed"

start_processes
wait_for_startup
