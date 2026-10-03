"""DWS wrapper pinned to the PROD gateway through a private DWS_CONFIG_DIR.

The global ~/.dws/mcp_url is flipped between prod and pre by other sessions
within seconds, and DWS_*_MCP_URL environment variables are ignored by dws.
A private config dir that contains no mcp_url/terminal_url makes every dws
process in this harness use the compiled-in production endpoints regardless
of the global file. Tokens live in the macOS keychain and are shared, so a
refresh is done against ~/.dws first and the metadata files are re-copied.
"""

from __future__ import annotations

import os
import shutil
import time
from pathlib import Path
from typing import Any

from .common import EVIDENCE_ROOT, clean_env, extract_json, run_cmd

HOME_DWS = Path.home() / ".dws"
GW_DIR = Path(os.environ.get("EL2E_DWS_DIR", str(EVIDENCE_ROOT / ".dws-prod-gw")))
COPY_FILES = ("profiles.json", "token.json", "dws-env-roles.json")
LINK_FILES = ("app.json", "identity.json")
FORBIDDEN_FILES = ("mcp_url", "terminal_url")


class GatewayError(RuntimeError):
    pass


def prepare(refresh_profiles: list[str] | None = None) -> Path:
    """Create or re-sync the private prod-gateway config dir."""
    if refresh_profiles:
        refresh(refresh_profiles)
    EVIDENCE_ROOT.mkdir(mode=0o700, parents=True, exist_ok=True)
    GW_DIR.mkdir(mode=0o700, parents=True, exist_ok=True)
    os.chmod(GW_DIR, 0o700)
    for name in COPY_FILES:
        src = HOME_DWS / name
        if src.exists():
            shutil.copy2(src, GW_DIR / name)
            os.chmod(GW_DIR / name, 0o600)
    for name in LINK_FILES:
        dst = GW_DIR / name
        if dst.is_symlink() or dst.exists():
            dst.unlink()
        if (HOME_DWS / name).exists():
            dst.symlink_to(HOME_DWS / name)
    assert_prod()
    return GW_DIR


def assert_prod() -> dict[str, Any]:
    """The private dir must never carry an endpoint override."""
    present = [name for name in FORBIDDEN_FILES if (GW_DIR / name).exists()]
    if present:
        raise GatewayError(f"private DWS dir {GW_DIR} carries endpoint override(s) {present}; refusing to send")
    if not (GW_DIR / "profiles.json").exists():
        raise GatewayError(f"private DWS dir {GW_DIR} is not prepared; run `e2e.py gw prepare`")
    return {"config_dir": str(GW_DIR), "mcp": "https://mcp.dingtalk.com (compiled default)", "environment": "prod"}


def refresh(profiles: list[str]) -> list[dict[str, Any]]:
    """Renew access tokens in ~/.dws (keychain shared), then re-copy metadata."""
    results = []
    for profile in profiles:
        res = run_cmd(["dws", "auth", "status", "--profile", profile, "--format", "json"],
                      env=clean_env({"DWS_CONFIG_DIR": str(HOME_DWS)}), timeout=90)
        try:
            body = extract_json(res["stdout"])
        except ValueError:
            body = {}
        ok = bool(body.get("success") and body.get("authenticated") and body.get("token_valid"))
        results.append({"profile": profile, "ok": ok, "refreshed": bool(body.get("refreshed")),
                        "expires_at": body.get("expires_at"), "rc": res["rc"]})
    if GW_DIR.exists():
        for name in COPY_FILES:
            src = HOME_DWS / name
            if src.exists():
                shutil.copy2(src, GW_DIR / name)
    return results


def dws(profile: str, args: list[str], *, timeout: int = 60, retries: int = 0) -> dict[str, Any]:
    """Run one dws command as `profile` through the private prod gateway."""
    gw = assert_prod()
    cmd = ["dws", "--profile", profile, *args]
    if "--format" not in args and "-f" not in args:
        cmd += ["--format", "json"]
    attempt = 0
    while True:
        res = run_cmd(cmd, env=clean_env({"DWS_CONFIG_DIR": gw["config_dir"]}), timeout=timeout)
        try:
            res["json"] = extract_json(res["stdout"])
        except ValueError:
            res["json"] = None
        if res["rc"] == 0 and res["json"] is not None:
            break
        transient = res["timeout"] or "timeout" in (res["stderr"] + res["stdout"]).lower() or \
            "backend_dependency_unavailable" in (res["stderr"] + res["stdout"])
        if attempt >= retries or not transient:
            break
        attempt += 1
        time.sleep(3 * attempt)
    res["attempts"] = attempt + 1
    res["gateway"] = gw
    # Never keep raw stderr beyond a short tail; it can echo request ids but not tokens.
    res["stderr"] = (res.get("stderr") or "")[-800:]
    res["stdout"] = (res.get("stdout") or "")[-4000:] if res["json"] is None else ""
    return res
