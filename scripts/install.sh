#!/usr/bin/env bash
# Multica installer — installs the CLI and optionally provisions a self-host server.
#
# Install / upgrade the fork CLI from source (default):
#   curl -fsSL https://raw.githubusercontent.com/D1-2004/multica/develop/scripts/install.sh | bash
# Install the official CLI from source:
#   curl -fsSL https://raw.githubusercontent.com/D1-2004/multica/develop/scripts/install.sh | bash -s -- --source official
#
# Install CLI + provision self-host server:
#   curl -fsSL https://raw.githubusercontent.com/D1-2004/multica/develop/scripts/install.sh | bash -s -- --with-server
#
# After installation, run `multica setup` to configure your environment.
#
set -euo pipefail

# ---------------------------------------------------------------------------
# Configuration
# ---------------------------------------------------------------------------
SOURCE_NAME="${MULTICA_SOURCE:-fork}"
SOURCE_REF="${MULTICA_REF:-}"
SOURCE_ROOT="${MULTICA_SOURCE_DIR:-$HOME/.multica/source}"
SOURCE_DEFAULT_REF=""
REPO_URL=""
INSTALL_DIR="${MULTICA_INSTALL_DIR:-$HOME/.multica/server}"

# Colors (disabled when not a terminal)
if [ -t 1 ] || [ -t 2 ]; then
  BOLD='\033[1m'
  GREEN='\033[0;32m'
  YELLOW='\033[0;33m'
  RED='\033[0;31m'
  CYAN='\033[0;36m'
  RESET='\033[0m'
else
  BOLD='' GREEN='' YELLOW='' RED='' CYAN='' RESET=''
fi

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------
info()  { printf "${BOLD}${CYAN}==> %s${RESET}\n" "$*"; }
ok()    { printf "${BOLD}${GREEN}✓ %s${RESET}\n" "$*"; }
warn()  { printf "${BOLD}${YELLOW}⚠ %s${RESET}\n" "$*" >&2; }
fail()  { printf "${BOLD}${RED}✗ %s${RESET}\n" "$*" >&2; exit 1; }

command_exists() { command -v "$1" >/dev/null 2>&1; }

env_file_value() {
  local file="$1"
  local key="$2"
  local default="$3"
  local line value
  line="$(grep -E "^${key}=" "$file" 2>/dev/null | tail -n 1 || true)"
  if [ -z "$line" ]; then
    printf "%s" "$default"
    return
  fi
  value="${line#*=}"
  value="${value%$'\r'}"
  value="${value%\"}"
  value="${value#\"}"
  value="${value%\'}"
  value="${value#\'}"
  if [ -z "$value" ]; then
    printf "%s" "$default"
  else
    printf "%s" "$value"
  fi
}

selfhost_backend_port() {
  local file="${1:-.env}"
  local value
  for key in BACKEND_PORT API_PORT SERVER_PORT PORT; do
    value="$(env_file_value "$file" "$key" "")"
    if [ -n "$value" ]; then
      printf "%s" "$value"
      return
    fi
  done
  printf "8080"
}

selfhost_frontend_port() {
  env_file_value "${1:-.env}" "FRONTEND_PORT" "3000"
}

detect_os() {
  case "$(uname -s)" in
    Darwin) OS="darwin" ;;
    Linux)  OS="linux" ;;
    MINGW*|MSYS*|CYGWIN*)
            fail "This script does not support Windows. Use the PowerShell installer instead:
  irm https://raw.githubusercontent.com/multica-ai/multica/main/scripts/install.ps1 | iex" ;;
    *)      fail "Unsupported operating system: $(uname -s). Multica supports macOS, Linux, and Windows." ;;
  esac

  ARCH="$(uname -m)"
  case "$ARCH" in
    x86_64)  ARCH="amd64" ;;
    aarch64) ARCH="arm64" ;;
    arm64)   ARCH="arm64" ;;
    *)       fail "Unsupported architecture: $ARCH" ;;
  esac
}

# ---------------------------------------------------------------------------
# CLI Installation
# ---------------------------------------------------------------------------
resolve_source() {
  case "$1" in
    fork)
      SOURCE_NAME="fork"
      SOURCE_DEFAULT_REF="develop"
      REPO_URL="https://github.com/D1-2004/multica.git"
      ;;
    official)
      SOURCE_NAME="official"
      SOURCE_DEFAULT_REF="main"
      REPO_URL="https://github.com/multica-ai/multica.git"
      ;;
    *)
      fail "Unknown source '$1'. Use --source fork or --source official."
      ;;
  esac
}

checkout_cli_source() {
  local source_dir="$1"
  local ref="$2"

  mkdir -p "$SOURCE_ROOT"
  if [ -d "$source_dir/.git" ]; then
    git -C "$source_dir" remote set-url origin "$REPO_URL"
  else
    if [ -e "$source_dir" ]; then
      warn "Removing incomplete managed source checkout at $source_dir"
      rm -rf "$source_dir"
    fi
    git clone --filter=blob:none --no-checkout "$REPO_URL" "$source_dir"
  fi
  git -C "$source_dir" fetch --force --depth=1 origin "$ref"
  git -C "$source_dir" checkout --detach FETCH_HEAD
}

select_cli_bin_dir() {
  if [ -n "${MULTICA_BIN_DIR:-}" ]; then
    printf '%s' "$MULTICA_BIN_DIR"
  elif [ -d /usr/local/bin ] && [ -w /usr/local/bin ]; then
    printf '%s' "/usr/local/bin"
  else
    printf '%s' "$HOME/.local/bin"
  fi
}

save_update_source() {
  local config_dir="$HOME/.multica"
  local tmp
  mkdir -p "$config_dir"
  tmp=$(mktemp "$config_dir/.update-source.XXXXXX")
  chmod 600 "$tmp"
  printf '%s\n' "$SOURCE_NAME" > "$tmp"
  mv -f "$tmp" "$config_dir/update-source"
}

install_cli_source() {
  local ref="${SOURCE_REF:-$SOURCE_DEFAULT_REF}"
  local source_dir="$SOURCE_ROOT/$SOURCE_NAME"
  local bin_dir destination tmp_binary commit build_date

  command_exists git || fail "Git is required to install the Multica CLI from source. Install git and retry."
  command_exists go || fail "Go is required to install the Multica CLI from source. Install Go 1.26+ and retry."

  info "Preparing ${SOURCE_NAME} source (${ref})..."
  checkout_cli_source "$source_dir" "$ref"

  bin_dir=$(select_cli_bin_dir)
  mkdir -p "$bin_dir"
  destination="$bin_dir/multica"
  tmp_binary=$(mktemp "$bin_dir/.multica-build.XXXXXX")
  rm -f "$tmp_binary"
  commit=$(git -C "$source_dir" rev-parse --short HEAD 2>/dev/null || printf 'unknown')
  build_date=$(date -u +%Y-%m-%dT%H:%M:%SZ)

  info "Building Multica CLI from ${SOURCE_NAME}@${ref}..."
  if ! (
    cd "$source_dir/server"
    go build -trimpath \
      -ldflags "-X main.version=source-${SOURCE_NAME} -X main.commit=${commit} -X main.date=${build_date}" \
      -o "$tmp_binary" ./cmd/multica
  ); then
    rm -f "$tmp_binary"
    fail "Failed to build the Multica CLI from ${SOURCE_NAME}@${ref}. The existing CLI was not changed."
  fi

  chmod 755 "$tmp_binary"
  mv -f "$tmp_binary" "$destination"
  save_update_source

  if ! echo "$PATH" | tr ':' '\n' | grep -q "^$bin_dir$"; then
    export PATH="$bin_dir:$PATH"
    add_to_path "$bin_dir"
  fi
  ok "Multica CLI installed from ${SOURCE_NAME}@${ref} to $destination"
}

add_to_path() {
  local dir="$1"
  local line="export PATH=\"$dir:\$PATH\""
  for rc in "$HOME/.bashrc" "$HOME/.zshrc"; do
    if [ -f "$rc" ] && ! grep -qF "$dir" "$rc"; then
      printf '\n# Added by Multica installer\n%s\n' "$line" >> "$rc"
    fi
  done
}

get_selfhost_ref() {
  if [ -n "${MULTICA_SELFHOST_REF:-}" ]; then
    printf '%s' "$MULTICA_SELFHOST_REF"
    return
  fi

	if [ -n "${SOURCE_REF:-}" ]; then
	  printf '%s' "$SOURCE_REF"
    return
  fi

  printf '%s' "$SOURCE_DEFAULT_REF"
}

checkout_server_ref() {
  local ref="$1"

  if [ "$ref" = "main" ]; then
    git fetch origin main --depth 1 2>/dev/null || true
    git checkout --force main 2>/dev/null || true
    git reset --hard origin/main 2>/dev/null || true
    return
  fi

  git fetch origin --tags --force 2>/dev/null || true
  if git rev-parse --verify --quiet "refs/tags/$ref" >/dev/null; then
    git checkout --force "$ref" 2>/dev/null || git checkout --force "tags/$ref" 2>/dev/null || true
    return
  fi

  git fetch origin "$ref" --depth 1 2>/dev/null || true
  git checkout --force "$ref" 2>/dev/null || true
}

pull_official_selfhost_images() {
  if docker compose -f docker-compose.selfhost.yml pull; then
    return
  fi

  echo ""
  warn "Official images for the selected self-host channel are not published yet."
  echo "This can happen before the first GHCR release is available."
  echo "From $INSTALL_DIR, build from source instead:"
  echo "  docker compose -f docker-compose.selfhost.yml -f docker-compose.selfhost.build.yml up -d --build"
  exit 1
}

install_cli() {
  install_cli_source
}

# ---------------------------------------------------------------------------
# Docker check
# ---------------------------------------------------------------------------
check_docker() {
  if ! command_exists docker; then
    printf "\n"
    fail "Docker is not installed. Multica self-hosting requires Docker and Docker Compose.

Install Docker:
  macOS:  https://docs.docker.com/desktop/install/mac-install/
  Linux:  https://docs.docker.com/engine/install/

After installing Docker, re-run this script with --with-server."
  fi

  if ! docker info >/dev/null 2>&1; then
    fail "Docker is installed but not running. Please start Docker and re-run this script."
  fi

  ok "Docker is available"
}

# ---------------------------------------------------------------------------
# Server setup (self-host / --with-server)
# ---------------------------------------------------------------------------
setup_server() {
  info "Setting up Multica server..."
  local server_ref
  server_ref=$(get_selfhost_ref)
  info "Using self-host assets from ${server_ref}..."

  if [ -d "$INSTALL_DIR/.git" ]; then
    info "Updating existing installation at $INSTALL_DIR..."
    cd "$INSTALL_DIR"
	git remote set-url origin "$REPO_URL"
  else
    info "Cloning Multica repository..."
    if ! command_exists git; then
      fail "Git is not installed. Please install git and re-run."
    fi
    # Remove leftover directory from a previously interrupted clone
    if [ -d "$INSTALL_DIR" ]; then
      warn "Removing incomplete installation at $INSTALL_DIR..."
      rm -rf "$INSTALL_DIR"
    fi
    mkdir -p "$(dirname "$INSTALL_DIR")"
    git clone --depth 1 "$REPO_URL" "$INSTALL_DIR"
    cd "$INSTALL_DIR"
  fi

  checkout_server_ref "$server_ref"

  ok "Repository ready at $INSTALL_DIR ($server_ref)"

  # Generate .env if needed
  if [ ! -f .env ]; then
    info "Creating .env with random secrets..."
    cp .env.example .env
    local jwt pgpass
    jwt=$(openssl rand -hex 32)
    pgpass=$(openssl rand -hex 24)
    if [ "$(uname -s)" = "Darwin" ]; then
      sed -i '' "s/^JWT_SECRET=.*/JWT_SECRET=$jwt/" .env
      sed -i '' "s/^POSTGRES_PASSWORD=.*/POSTGRES_PASSWORD=$pgpass/" .env
      sed -i '' -E "s#^(DATABASE_URL=postgres://[^:]+:)[^@]*(@.*)#\1$pgpass\2#" .env
    else
      sed -i "s/^JWT_SECRET=.*/JWT_SECRET=$jwt/" .env
      sed -i "s/^POSTGRES_PASSWORD=.*/POSTGRES_PASSWORD=$pgpass/" .env
      sed -i -E "s#^(DATABASE_URL=postgres://[^:]+:)[^@]*(@.*)#\1$pgpass\2#" .env
    fi
    ok "Generated .env with random JWT_SECRET and POSTGRES_PASSWORD"
  else
    ok "Using existing .env"
  fi

  # Start Docker Compose
  info "Pulling official Multica images..."
  pull_official_selfhost_images
  info "Starting Multica services (this may take a few minutes on first run)..."
  docker compose -f docker-compose.selfhost.yml up -d

  # Wait for health check
  info "Waiting for backend to be ready..."
  local backend_port
  backend_port="$(selfhost_backend_port .env)"
  local ready=false
  for i in $(seq 1 45); do
    if curl -sf "http://localhost:${backend_port}/health" >/dev/null 2>&1; then
      ready=true
      break
    fi
    sleep 2
  done

  if [ "$ready" = true ]; then
    ok "Multica server is running"
  else
    warn "Server is still starting. You can check logs with:"
    echo "  cd $INSTALL_DIR && docker compose -f docker-compose.selfhost.yml logs"
    echo ""
  fi
}


# ---------------------------------------------------------------------------
# Main: Default mode (install / upgrade CLI only)
# ---------------------------------------------------------------------------
run_default() {
  printf "\n"
  printf "${BOLD}  Multica — Installer${RESET}\n"
  printf "\n"

  detect_os
  install_cli

  printf "\n"
  printf "${BOLD}${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${RESET}\n"
  printf "${BOLD}${GREEN}  ✓ Multica CLI is ready!${RESET}\n"
  printf "${BOLD}${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${RESET}\n"
  printf "\n"
  printf "  ${BOLD}Next: configure your environment${RESET}\n"
  printf "\n"
  printf "     ${CYAN}multica setup${RESET}                # Connect to Multica Cloud (multica.ai)\n"
  printf "     ${CYAN}multica setup self-host${RESET}       # Connect to a self-hosted server\n"
  printf "\n"
  printf "  ${BOLD}Self-hosting?${RESET} Install the server first:\n"
  printf "     curl -fsSL https://raw.githubusercontent.com/D1-2004/multica/develop/scripts/install.sh | bash -s -- --with-server\n"
  printf "\n"
}

# ---------------------------------------------------------------------------
# Main: With-server mode (provision self-host infrastructure + install CLI)
# ---------------------------------------------------------------------------
run_with_server() {
  printf "\n"
  printf "${BOLD}  Multica — Self-Host Installer${RESET}\n"
  printf "  Provisioning server infrastructure + installing CLI\n"
  printf "\n"

  detect_os
  check_docker
  setup_server
  install_cli

  printf "\n"
  printf "${BOLD}${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${RESET}\n"
  printf "${BOLD}${GREEN}  ✓ Multica server is running and CLI is ready!${RESET}\n"
  printf "${BOLD}${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${RESET}\n"
  printf "\n"
  local frontend_port backend_port
  frontend_port="$(selfhost_frontend_port "$INSTALL_DIR/.env")"
  backend_port="$(selfhost_backend_port "$INSTALL_DIR/.env")"
  printf "  ${BOLD}Frontend:${RESET}  http://localhost:%s\n" "$frontend_port"
  printf "  ${BOLD}Backend:${RESET}   http://localhost:%s\n" "$backend_port"
  printf "  ${BOLD}Server at:${RESET} %s\n" "$INSTALL_DIR"
  printf "\n"
  printf "  ${BOLD}Next: configure your CLI to connect${RESET}\n"
  printf "\n"
  printf "     ${CYAN}multica setup self-host${RESET}   # Configure + authenticate + start daemon\n"
  printf "\n"
  printf "  ${BOLD}Login:${RESET} configure ${CYAN}RESEND_API_KEY${RESET} in .env for email codes,\n"
  printf "  or read the generated code from backend logs when Resend is unset.\n"
  printf "\n"
  printf "  ${BOLD}To stop all services:${RESET}\n"
  printf "     curl -fsSL https://raw.githubusercontent.com/D1-2004/multica/develop/scripts/install.sh | bash -s -- --stop\n"
  printf "\n"
}

# ---------------------------------------------------------------------------
# Stop: shut down a self-hosted installation
# ---------------------------------------------------------------------------
run_stop() {
  printf "\n"
  info "Stopping Multica services..."

  if [ -d "$INSTALL_DIR" ]; then
    cd "$INSTALL_DIR"
    if [ -f docker-compose.selfhost.yml ]; then
      docker compose -f docker-compose.selfhost.yml down
      ok "Docker services stopped"
    else
      warn "No docker-compose.selfhost.yml found at $INSTALL_DIR"
    fi
  else
    warn "No Multica installation found at $INSTALL_DIR"
  fi

  if command_exists multica; then
    multica daemon stop 2>/dev/null && ok "Daemon stopped" || true
  fi

  printf "\n"
}

# ---------------------------------------------------------------------------
# Entry point
# ---------------------------------------------------------------------------
main() {
  local mode="default"

  while [ $# -gt 0 ]; do
    case "$1" in
      --with-server) mode="with-server" ;;
      --local)       mode="with-server" ;;  # backwards compat alias
      --stop)        mode="stop" ;;
	  --source)
		[ $# -ge 2 ] || fail "--source requires fork or official"
		SOURCE_NAME="$2"
		shift
		;;
	  --source=*) SOURCE_NAME="${1#*=}" ;;
	  --ref)
		[ $# -ge 2 ] || fail "--ref requires a branch, tag, or commit"
		SOURCE_REF="$2"
		shift
		;;
	  --ref=*) SOURCE_REF="${1#*=}" ;;
      --help|-h)
		echo "Usage: install.sh [--source fork|official] [--ref <git-ref>] [--with-server | --stop]"
        echo ""
		echo "  (default)       Build and install the fork CLI from develop"
		echo "  --source        Source registry entry: fork (default) or official"
		echo "  --ref           Branch, tag, or commit (defaults to develop/main)"
        echo "  --with-server   Install CLI + provision a self-host server (Docker)"
        echo "  --stop          Stop a self-hosted installation"
        echo ""
        echo "Environment variables:"
        echo "  MULTICA_INSTALL_DIR   Self-host server install directory"
        echo "                        (default: \$HOME/.multica/server)"
        echo "  MULTICA_BIN_DIR       Target directory for the CLI binary when"
		echo "                        building from source"
        echo "                        (default: /usr/local/bin, then \$HOME/.local/bin)"
		echo "  MULTICA_SOURCE_DIR    Managed git checkout root"
		echo "                        (default: \$HOME/.multica/source)"
        echo "  MULTICA_SELFHOST_REF  Git ref to check out for self-host assets"
		echo "                        (default: --ref or the selected source default)"
        echo ""
        echo "After installation, run 'multica setup' to configure your environment."
        exit 0
        ;;
	  *) fail "Unknown option: $1" ;;
    esac
    shift
  done

	if [ "$mode" != "stop" ]; then
	  resolve_source "$SOURCE_NAME"
	fi

  case "$mode" in
    default)     run_default ;;
    with-server) run_with_server ;;
    stop)        run_stop ;;
  esac
}

main "$@"
