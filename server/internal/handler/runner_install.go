package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
)

const runnerInstallScript = `#!/bin/sh
set -eu

server_url=""
pairing_token=""
reconnect_token=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --server-url)
      [ "$#" -ge 2 ] || { echo "--server-url requires a value" >&2; exit 2; }
      server_url="$2"
      shift 2
      ;;
    --pairing-token)
      [ "$#" -ge 2 ] || { echo "--pairing-token requires a value" >&2; exit 2; }
      pairing_token="$2"
      shift 2
      ;;
    --reconnect-token)
      [ "$#" -ge 2 ] || { echo "--reconnect-token requires a value" >&2; exit 2; }
      reconnect_token="$2"
      shift 2
      ;;
    *)
      echo "unknown argument: $1" >&2
      exit 2
      ;;
  esac
done

[ -n "$server_url" ] || { echo "--server-url is required" >&2; exit 2; }
if [ -n "$pairing_token" ] && [ -n "$reconnect_token" ]; then
  echo "--pairing-token and --reconnect-token are mutually exclusive" >&2
  exit 2
fi
if [ -z "$pairing_token" ] && [ -z "$reconnect_token" ]; then
  echo "--pairing-token or --reconnect-token is required" >&2
  exit 2
fi

case "$(uname -s)" in
  Darwin) runner_os="darwin" ;;
  Linux) runner_os="linux" ;;
  *) echo "Multica Runner currently supports macOS and Linux" >&2; exit 1 ;;
esac

case "$(uname -m)" in
  x86_64) runner_arch="amd64" ;;
  arm64|aarch64) runner_arch="arm64" ;;
  *) echo "Multica Runner requires amd64 or arm64" >&2; exit 1 ;;
esac

install_dir="${HOME}/.multica/runner/bin"
temp_dir="$(mktemp -d)"
trap 'rm -rf "$temp_dir"' EXIT INT TERM
binary_url="${server_url%/}/api/runner/binaries/${runner_os}/${runner_arch}"

curl -fsSL "$binary_url/checksum" -o "$temp_dir/checksum"
expected="$(awk '{print $1}' "$temp_dir/checksum")"
case "$expected" in
  *[!0-9a-f]*|'') echo "Runner checksum response is invalid" >&2; exit 1 ;;
esac
[ "${#expected}" -eq 64 ] || { echo "Runner checksum response is invalid" >&2; exit 1; }

echo "Downloading Multica Runner for ${runner_os}/${runner_arch}..."
curl -fsSL "${binary_url}?sha256=${expected}" -o "$temp_dir/multica"

if [ "$runner_os" = "darwin" ]; then
  actual="$(shasum -a 256 "$temp_dir/multica" | awk '{print $1}')"
else
  actual="$(sha256sum "$temp_dir/multica" | awk '{print $1}')"
fi
[ "$expected" = "$actual" ] || { echo "Runner checksum verification failed" >&2; exit 1; }

mkdir -p "$install_dir"
chmod 0755 "$temp_dir/multica"
mv "$temp_dir/multica" "$install_dir/multica"

if [ -n "$pairing_token" ]; then
  exec "$install_dir/multica" runner bind --server-url "$server_url" --pairing-token "$pairing_token"
fi
exec "$install_dir/multica" runner reconnect --server-url "$server_url" --reconnect-token "$reconnect_token"
`

func (h *Handler) ServeRunnerInstall(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(runnerInstallScript)))
	_, _ = io.WriteString(w, runnerInstallScript)
}

func runnerBinaryPath(goos, goarch string) (string, error) {
	if (goos != "darwin" && goos != "linux") || (goarch != "amd64" && goarch != "arm64") {
		return "", errors.New("unsupported Runner binary")
	}
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(executable), "runner-cli", goos+"-"+goarch, "multica"), nil
}

func (h *Handler) ServeRunnerBinary(w http.ResponseWriter, r *http.Request) {
	path, err := runnerBinaryPath(strings.TrimSpace(chi.URLParam(r, "os")), strings.TrimSpace(chi.URLParam(r, "arch")))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if _, err := os.Stat(path); err != nil {
		writeError(w, http.StatusNotFound, "Runner binary is not available")
		return
	}
	digest, err := runnerBinarySHA256(path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to checksum Runner binary")
		return
	}
	requestedDigest := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("sha256")))
	if requestedDigest == "" || requestedDigest != digest {
		writeError(w, http.StatusBadRequest, "Runner binary sha256 is required and must match the current build")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="multica"`)
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeFile(w, r, path)
}

func runnerBinarySHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func (h *Handler) ServeRunnerBinaryChecksum(w http.ResponseWriter, r *http.Request) {
	path, err := runnerBinaryPath(strings.TrimSpace(chi.URLParam(r, "os")), strings.TrimSpace(chi.URLParam(r, "arch")))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	digest, err := runnerBinarySHA256(path)
	if err != nil {
		writeError(w, http.StatusNotFound, "Runner binary is not available")
		return
	}
	value := digest + "  multica\n"
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(value)))
	_, _ = io.WriteString(w, value)
}
