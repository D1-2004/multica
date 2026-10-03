"""Shared helpers for the EmployeeLoop real-IM e2e harness (stdlib only)."""

from __future__ import annotations

import datetime as _dt
import hashlib
import json
import os
import subprocess
import time
import uuid
from pathlib import Path
from typing import Any

HARNESS_DIR = Path(__file__).resolve().parent.parent
REPO_ROOT = HARNESS_DIR.parent.parent
REGISTRY_PATH = HARNESS_DIR / "registry.json"
EVIDENCE_ROOT = Path(os.environ.get("EL2E_EVIDENCE_ROOT", str(Path.home() / "d1" / "employee-e2e-evidence")))
TZ = _dt.timezone(_dt.timedelta(hours=8), "Asia/Shanghai")
PROXY_VARS = ("HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy")


def clean_env(extra: dict[str, str] | None = None) -> dict[str, str]:
    """Environment for intranet/DWS subprocesses: every proxy variable removed."""
    env = {k: v for k, v in os.environ.items() if k not in PROXY_VARS}
    if extra:
        env.update(extra)
    return env


def scrub_process_proxies() -> None:
    for key in PROXY_VARS:
        os.environ.pop(key, None)


def now() -> _dt.datetime:
    return _dt.datetime.now(TZ)


def iso(ts: _dt.datetime) -> str:
    return ts.astimezone(TZ).isoformat(timespec="seconds")


def parse_dws_time(value: str) -> _dt.datetime:
    """DWS createTime is a local 'YYYY-MM-DD HH:MM:SS' string (Asia/Shanghai)."""
    return _dt.datetime.strptime(value.strip(), "%Y-%m-%d %H:%M:%S").replace(tzinfo=TZ)


def parse_iso(value: str) -> _dt.datetime:
    ts = _dt.datetime.fromisoformat(value.replace("Z", "+00:00"))
    if ts.tzinfo is None:
        ts = ts.replace(tzinfo=TZ)
    return ts


def load_json(path: Path, default: Any = None) -> Any:
    try:
        return json.loads(Path(path).read_text(encoding="utf-8"))
    except FileNotFoundError:
        return default


def write_json(path: Path, data: Any) -> None:
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_suffix(path.suffix + ".tmp")
    tmp.write_text(json.dumps(data, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    os.replace(tmp, path)


def append_jsonl(path: Path, record: dict[str, Any]) -> None:
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("a", encoding="utf-8") as fh:
        fh.write(json.dumps(record, ensure_ascii=False) + "\n")


def read_jsonl(path: Path) -> list[dict[str, Any]]:
    try:
        lines = Path(path).read_text(encoding="utf-8").splitlines()
    except FileNotFoundError:
        return []
    return [json.loads(line) for line in lines if line.strip()]


def registry() -> dict[str, Any]:
    return load_json(REGISTRY_PATH)


def save_registry(data: dict[str, Any]) -> None:
    write_json(REGISTRY_PATH, data)


def run_dir(run_id: str) -> Path:
    path = EVIDENCE_ROOT / run_id
    if not path.exists():
        EVIDENCE_ROOT.mkdir(mode=0o700, parents=True, exist_ok=True)
        path.mkdir(mode=0o700, parents=True, exist_ok=True)
    return path


def stable_uuid(marker: str) -> str:
    """Deterministic idempotency key for one logical send (retries reuse it)."""
    return str(uuid.uuid5(uuid.NAMESPACE_URL, "el2e:" + marker))


def sha256_text(text: str) -> str:
    return hashlib.sha256(text.encode("utf-8")).hexdigest()


def run_cmd(cmd: list[str], *, env: dict[str, str] | None = None, timeout: int = 120,
            cwd: str | None = None) -> dict[str, Any]:
    started = time.monotonic()
    try:
        proc = subprocess.run(cmd, capture_output=True, text=True, env=env or clean_env(),
                              timeout=timeout, cwd=cwd)
    except subprocess.TimeoutExpired as exc:
        return {"rc": None, "timeout": True, "stdout": exc.stdout or "", "stderr": exc.stderr or "",
                "elapsed_s": round(time.monotonic() - started, 1)}
    return {"rc": proc.returncode, "timeout": False, "stdout": proc.stdout, "stderr": proc.stderr,
            "elapsed_s": round(time.monotonic() - started, 1)}


def extract_json(raw: str) -> Any:
    """Parse the first JSON value in CLI output that may carry banner lines."""
    raw = raw or ""
    idx = 0
    decoder = json.JSONDecoder()
    while True:
        starts = [i for i in (raw.find("{", idx), raw.find("[", idx)) if i != -1]
        if not starts:
            raise ValueError("no JSON value in output")
        pos = min(starts)
        try:
            return decoder.raw_decode(raw[pos:])[0]
        except json.JSONDecodeError:
            idx = pos + 1
