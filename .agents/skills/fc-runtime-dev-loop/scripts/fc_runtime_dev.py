#!/usr/bin/env python3
"""Build an FC candidate template and cut a Multica Runtime over to it.

The script intentionally uses only the Python standard library. Credentials are
read from the environment or a Multica CLI profile and are never accepted as
command-line arguments.
"""

from __future__ import annotations

import argparse
import http.client
import ipaddress
import json
import os
import re
import shutil
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Mapping, Sequence


DEFAULT_REPOSITORY = "dingtalk-ai-lab/multica-fc-hermes-runtime"
DEFAULT_PIPELINE_ID = 295064
EXPECTED_PIPELINE_PATH = ".aoneci/runtime-fc-runtime-dev-loop-candidate.yaml"
DEFAULT_PIPELINE_BRANCH = "codex/fc-runtime-dev-loop-20260903"
TEMPLATE_JOB = "build-publish-and-verify"
TEMPLATE_STEP = "build-and-verify-e2b-template"
TERMINAL_STATUSES = {"SUCCESS", "FAILED", "CANCELED", "SKIPPED"}
SUCCESS_STATUS = "SUCCESS"
FULL_COMMIT_RE = re.compile(r"^[0-9a-f]{40}$")
TEMPLATE_FIELDS = {
    "template_id": re.compile(r"(?m)^template_id:\s*(\S+)\s*$"),
    "runtime_commit": re.compile(r"(?m)^runtime_commit:\s*([0-9a-f]{40})\s*$"),
    "multica_commit": re.compile(r"(?m)^multica_commit:\s*([0-9a-f]{40})\s*$"),
    "provider_fingerprint": re.compile(
        r"(?m)^provider_fingerprint:\s*([0-9a-f]{16})\s*$"
    ),
    "display_alias": re.compile(r"(?m)^display_alias:\s*(\S+)\s*$"),
}
DEFAULT_COMMAND_TIMEOUT_SECONDS = 60.0
PROFILE_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$")
RUNTIME_REF_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._/-]{0,254}$")


class DevLoopError(RuntimeError):
    """A user-actionable workflow failure."""


class APITransportError(DevLoopError):
    """The server may have committed a mutation before transport failed."""


def compact_json(value: Any) -> str:
    return json.dumps(value, ensure_ascii=False, separators=(",", ":"))


def print_json(value: Any) -> None:
    json.dump(value, sys.stdout, ensure_ascii=False, indent=2, sort_keys=True)
    sys.stdout.write("\n")


def redact(text: str, secrets: Sequence[str]) -> str:
    result = text
    for secret in secrets:
        if secret:
            result = result.replace(secret, "<redacted>")
    return result


def load_json_text(raw: str, source: str) -> Any:
    try:
        return json.loads(raw)
    except json.JSONDecodeError as exc:
        raise DevLoopError(f"{source} did not return valid JSON: {exc}") from exc


def require_mapping(value: Any, source: str) -> dict[str, Any]:
    if not isinstance(value, dict):
        raise DevLoopError(
            f"{source} returned {type(value).__name__}, expected an object"
        )
    return value


def require_list(value: Any, source: str) -> list[Any]:
    if not isinstance(value, list):
        raise DevLoopError(f"{source} returned {type(value).__name__}, expected a list")
    return value


def run_command(
    command: Sequence[str], timeout_seconds: float = DEFAULT_COMMAND_TIMEOUT_SECONDS
) -> str:
    env = os.environ.copy()
    for key in list(env):
        if key.startswith("MULTICA_"):
            env.pop(key, None)
    env.setdefault("A1_NO_UPDATE_CHECK", "1")
    try:
        completed = subprocess.run(
            list(command),
            check=False,
            capture_output=True,
            text=True,
            env=env,
            timeout=timeout_seconds,
        )
    except subprocess.TimeoutExpired as exc:
        raise DevLoopError(
            f"command timed out after {timeout_seconds:g}s: {command[0]} "
            f"{command[1] if len(command) > 1 else ''}"
        ) from exc
    except OSError as exc:
        raise DevLoopError(f"failed to execute {command[0]}: {exc}") from exc
    if completed.returncode != 0:
        detail = (completed.stderr or completed.stdout).strip()
        raise DevLoopError(
            f"command failed ({completed.returncode}): {command[0]} "
            f"{command[1] if len(command) > 1 else ''}: {detail}"
        )
    return completed.stdout


def full_commit(value: str) -> str:
    normalized = value.strip().lower()
    if not FULL_COMMIT_RE.fullmatch(normalized):
        raise argparse.ArgumentTypeError(
            "must be an immutable 40-character lowercase Git commit"
        )
    return normalized


def runtime_ref(value: str) -> str:
    normalized = value.strip()
    if not RUNTIME_REF_RE.fullmatch(normalized) or ".." in normalized:
        raise argparse.ArgumentTypeError("must be a safe pushed branch name")
    return normalized


def profile_name(value: str) -> str:
    normalized = value.strip()
    if normalized != "default" and not PROFILE_RE.fullmatch(normalized):
        raise argparse.ArgumentTypeError(
            "must contain only letters, digits, dot, underscore, and hyphen"
        )
    return normalized


def positive_int(value: str) -> int:
    parsed = int(value)
    if parsed <= 0:
        raise argparse.ArgumentTypeError("must be greater than zero")
    return parsed


def positive_float(value: str) -> float:
    parsed = float(value)
    if parsed <= 0:
        raise argparse.ArgumentTypeError("must be greater than zero")
    return parsed


def parse_run_id(raw: str) -> int:
    stripped = raw.strip()
    if stripped.isdigit():
        return int(stripped)
    try:
        value = json.loads(stripped)
    except json.JSONDecodeError:
        value = None
    if isinstance(value, dict):
        for key in ("runId", "run_id", "id", "pipelineRunId"):
            candidate = value.get(key)
            if isinstance(candidate, int) or (
                isinstance(candidate, str) and candidate.isdigit()
            ):
                return int(candidate)
    raise DevLoopError(
        "Aone pipeline submission returned no unambiguous run ID; expected JSON"
    )


@dataclass(frozen=True)
class BuildArtifact:
    run_id: int
    run_url: str
    runtime_branch: str
    runtime_commit: str
    multica_commit: str
    template_id: str
    display_alias: str
    provider_fingerprint: str

    def as_dict(self) -> dict[str, Any]:
        return {
            "run_id": self.run_id,
            "run_url": self.run_url,
            "runtime_branch": self.runtime_branch,
            "runtime_commit": self.runtime_commit,
            "multica_commit": self.multica_commit,
            "template_id": self.template_id,
            "display_alias": self.display_alias,
            "provider_fingerprint": self.provider_fingerprint,
        }


class AoneClient:
    def __init__(
        self,
        repository: str,
        pipeline_id: int,
        pipeline_path: str,
        a1_bin: str | None = None,
    ) -> None:
        self.repository = repository
        self.pipeline_id = pipeline_id
        self.pipeline_path = pipeline_path
        self.a1_bin = a1_bin or os.environ.get("A1_BIN", "a1")

    def _call_json(self, args: Sequence[str], source: str) -> Any:
        raw = run_command([self.a1_bin, *args, "-f", "json"])
        return load_json_text(raw, source)

    def validate_pipeline(self) -> dict[str, Any]:
        value = require_mapping(
            self._call_json(
                [
                    "ci",
                    "pipeline",
                    "get",
                    str(self.pipeline_id),
                    "--repo",
                    self.repository,
                ],
                "Aone pipeline get",
            ),
            "Aone pipeline get",
        )
        if value.get("repo") != self.repository:
            raise DevLoopError(
                f"pipeline {self.pipeline_id} belongs to {value.get('repo')!r}, "
                f"not {self.repository!r}"
            )
        if value.get("path") != self.pipeline_path:
            raise DevLoopError(
                f"pipeline {self.pipeline_id} points to {value.get('path')!r}; "
                f"expected the isolated FC candidate path {self.pipeline_path!r}"
            )
        normalized_path = self.pipeline_path.lower()
        pipeline_name = str(value.get("name", ""))
        if (
            not normalized_path.startswith(".aoneci/")
            or "candidate" not in normalized_path
            or "master" in normalized_path
            or "asb" in normalized_path
            or (
                "candidate" not in pipeline_name.lower() and "候选" not in pipeline_name
            )
            or "asb" in pipeline_name.lower()
        ):
            raise DevLoopError(
                f"pipeline {self.pipeline_id} is not an isolated FC candidate pipeline: "
                f"name={pipeline_name!r}, path={self.pipeline_path!r}"
            )
        if str(value.get("status", "")).upper() not in {"NORMAL", "ACTIVE"}:
            raise DevLoopError(
                f"pipeline {self.pipeline_id} is not active: {value.get('status')!r}"
            )
        return value

    def submit(self, runtime_ref: str, multica_ref: str) -> int:
        raw = run_command(
            [
                self.a1_bin,
                "ci",
                "pipeline",
                "run",
                str(self.pipeline_id),
                "--repo",
                self.repository,
                "--branch",
                runtime_ref,
                "--param",
                f"multica_ref={multica_ref}",
                "-f",
                "json",
            ]
        )
        return parse_run_id(raw)

    def get_run(self, run_id: int) -> dict[str, Any]:
        return require_mapping(
            self._call_json(
                [
                    "ci",
                    "run",
                    "get",
                    str(run_id),
                    "--repo",
                    self.repository,
                ],
                "Aone run get",
            ),
            "Aone run get",
        )

    def wait_for_run(
        self,
        run_id: int,
        timeout_seconds: int,
        poll_interval_seconds: float,
    ) -> dict[str, Any]:
        deadline = time.monotonic() + timeout_seconds
        last_status = "UNKNOWN"
        while True:
            run = self.get_run(run_id)
            last_status = str(run.get("status", "UNKNOWN")).upper()
            if last_status in TERMINAL_STATUSES:
                return run
            if time.monotonic() >= deadline:
                raise DevLoopError(
                    f"timed out waiting for Aone run {run_id}; last status={last_status}"
                )
            time.sleep(poll_interval_seconds)

    def get_template_log(self, run_id: int) -> str:
        value = require_mapping(
            self._call_json(
                [
                    "ci",
                    "run",
                    "log",
                    str(run_id),
                    "--repo",
                    self.repository,
                    "--job",
                    TEMPLATE_JOB,
                    "--step",
                    TEMPLATE_STEP,
                ],
                "Aone template step log",
            ),
            "Aone template step log",
        )
        jobs = require_list(value.get("jobs"), "Aone template step jobs")
        for job in jobs:
            if not isinstance(job, dict) or job.get("name") != TEMPLATE_JOB:
                continue
            steps = job.get("steps")
            if not isinstance(steps, list):
                continue
            for step in steps:
                if isinstance(step, dict) and step.get("name") == TEMPLATE_STEP:
                    log = step.get("log")
                    if isinstance(log, str) and log.strip():
                        return log
        raise DevLoopError(
            f"Aone run {run_id} has no log for {TEMPLATE_JOB}/{TEMPLATE_STEP}"
        )

    def artifact_from_run(
        self,
        run: Mapping[str, Any],
        expected_runtime_ref: str,
        expected_runtime_commit: str,
        expected_multica_commit: str,
    ) -> BuildArtifact:
        run_id = int(run.get("id", 0))
        if run_id <= 0:
            raise DevLoopError("Aone run has no valid id")
        run_pipeline_id = int(run.get("pipelineId", 0))
        if run_pipeline_id != self.pipeline_id:
            raise DevLoopError(
                f"Aone run {run_id} belongs to pipeline {run_pipeline_id}, "
                f"expected candidate pipeline {self.pipeline_id}"
            )
        status = str(run.get("status", "")).upper()
        if status != SUCCESS_STATUS:
            url = str(run.get("url", ""))
            raise DevLoopError(
                f"Aone run {run_id} ended with {status or 'UNKNOWN'}; "
                f"candidate Runtime mutation is blocked; run_url={url}"
            )
        log = self.get_template_log(run_id)
        fields: dict[str, str] = {}
        for name, pattern in TEMPLATE_FIELDS.items():
            matches = pattern.findall(log)
            values = set(matches)
            if not values:
                raise DevLoopError(
                    f"successful Aone run {run_id} did not emit required field {name}"
                )
            if len(values) != 1:
                raise DevLoopError(
                    f"successful Aone run {run_id} emitted conflicting {name} values"
                )
            fields[name] = values.pop()
        run_commit = str(run.get("commit") or run.get("revision") or "").lower()
        if not FULL_COMMIT_RE.fullmatch(run_commit):
            raise DevLoopError(
                f"Aone run {run_id} returned invalid commit {run_commit!r}"
            )
        if fields["runtime_commit"] != run_commit:
            raise DevLoopError(
                f"Aone run commit {run_commit} does not match Template log commit "
                f"{fields['runtime_commit']}"
            )
        if run_commit != expected_runtime_commit:
            raise DevLoopError(
                f"Aone run commit {run_commit} does not match expected Runtime commit "
                f"{expected_runtime_commit}"
            )
        run_branch = str(run.get("branch", ""))
        if run_branch != expected_runtime_ref:
            raise DevLoopError(
                f"Aone built branch {run_branch!r}, expected {expected_runtime_ref!r}"
            )
        params = run.get("params")
        if not isinstance(params, dict):
            raise DevLoopError(f"Aone run {run_id} returned no parameter map")
        multica_ref = str(params.get("multica_ref", "")).strip().lower()
        if not FULL_COMMIT_RE.fullmatch(multica_ref):
            raise DevLoopError(
                f"Aone run {run_id} used mutable or missing multica_ref {multica_ref!r}"
            )
        if multica_ref != expected_multica_commit:
            raise DevLoopError(
                f"Aone used multica_ref {multica_ref}, expected {expected_multica_commit}"
            )
        if fields["multica_commit"] != expected_multica_commit:
            raise DevLoopError(
                f"Template log multica_commit {fields['multica_commit']} does not match "
                f"expected {expected_multica_commit}"
            )
        return BuildArtifact(
            run_id=run_id,
            run_url=str(run.get("url", "")),
            runtime_branch=run_branch,
            runtime_commit=run_commit,
            multica_commit=fields["multica_commit"],
            template_id=fields["template_id"],
            display_alias=fields["display_alias"],
            provider_fingerprint=fields["provider_fingerprint"],
        )

    def build(
        self,
        runtime_ref: str,
        runtime_commit: str,
        multica_commit: str,
        timeout_seconds: int,
        poll_interval_seconds: float,
    ) -> BuildArtifact:
        if (
            self.pipeline_id == DEFAULT_PIPELINE_ID
            and self.pipeline_path == EXPECTED_PIPELINE_PATH
            and runtime_ref != DEFAULT_PIPELINE_BRANCH
        ):
            raise DevLoopError(
                f"pipeline {DEFAULT_PIPELINE_ID} belongs to Runtime branch "
                f"{DEFAULT_PIPELINE_BRANCH!r}; pass the target branch's dedicated "
                "--pipeline-id and --pipeline-path"
            )
        self.validate_pipeline()
        run_id = self.submit(runtime_ref, multica_commit)
        print(f"submitted Aone candidate run {run_id}", file=sys.stderr, flush=True)
        run = self.wait_for_run(run_id, timeout_seconds, poll_interval_seconds)
        return self.artifact_from_run(
            run,
            expected_runtime_ref=runtime_ref,
            expected_runtime_commit=runtime_commit,
            expected_multica_commit=multica_commit,
        )


@dataclass(frozen=True)
class APIConfig:
    server_url: str
    workspace_id: str
    token: str
    profile_path: str


def profile_config_path(profile: str) -> Path:
    if profile == "default":
        return Path.home() / ".multica" / "config.json"
    return Path.home() / ".multica" / "profiles" / profile / "config.json"


def load_profile(profile: str) -> tuple[dict[str, Any], Path]:
    path = profile_config_path(profile)
    if not path.is_file():
        return {}, path
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise DevLoopError(f"cannot read Multica profile {path}: {exc}") from exc
    return require_mapping(value, f"Multica profile {path}"), path


def resolve_api_config(
    args: argparse.Namespace, require_token: bool = True
) -> APIConfig:
    profile, path = load_profile(args.profile)
    profile_server_url = str(profile.get("server_url", "")).strip().rstrip("/")
    env_server_url = os.environ.get("MULTICA_SERVER_URL", "").strip().rstrip("/")
    profile_workspace_id = str(profile.get("workspace_id", "")).strip()
    env_workspace_id = os.environ.get("MULTICA_WORKSPACE_ID", "").strip()
    if (
        not args.server_url
        and profile_server_url
        and env_server_url
        and profile_server_url != env_server_url
    ):
        raise DevLoopError(
            "MULTICA_SERVER_URL conflicts with the selected profile; unset it or pass "
            "--server-url explicitly"
        )
    if (
        not args.workspace_id
        and profile_workspace_id
        and env_workspace_id
        and profile_workspace_id != env_workspace_id
    ):
        raise DevLoopError(
            "MULTICA_WORKSPACE_ID conflicts with the selected profile; unset it or pass "
            "--workspace-id explicitly"
        )
    server_url = (
        (args.server_url or profile_server_url or env_server_url).strip().rstrip("/")
    )
    workspace_id = (
        args.workspace_id or profile_workspace_id or env_workspace_id
    ).strip()
    token = (
        os.environ.get("MULTICA_TOKEN", "") or str(profile.get("token", ""))
    ).strip()
    if not server_url:
        raise DevLoopError(
            "Multica server URL is required via --server-url, MULTICA_SERVER_URL, "
            f"or profile {path}"
        )
    parsed = urllib.parse.urlparse(server_url)
    if (
        parsed.scheme not in {"http", "https"}
        or not parsed.netloc
        or parsed.username is not None
        or parsed.password is not None
        or parsed.path not in {"", "/"}
        or parsed.params
        or parsed.query
        or parsed.fragment
    ):
        raise DevLoopError(f"invalid Multica server URL: {server_url!r}")
    if parsed.scheme == "http":
        hostname = parsed.hostname or ""
        is_loopback = hostname == "localhost"
        if not is_loopback:
            try:
                is_loopback = ipaddress.ip_address(hostname).is_loopback
            except ValueError:
                is_loopback = False
        if not is_loopback:
            raise DevLoopError(
                "Multica PATs require HTTPS; HTTP is allowed only for loopback testing"
            )
    if not workspace_id:
        raise DevLoopError(
            "Multica workspace ID is required via --workspace-id, "
            f"MULTICA_WORKSPACE_ID, or profile {path}"
        )
    if require_token and not token:
        raise DevLoopError(
            "Multica PAT is required via MULTICA_TOKEN or the selected profile; "
            "refresh it with 'multica --profile " + args.profile + " login'"
        )
    return APIConfig(server_url, workspace_id, token, str(path))


class RefuseRedirects(urllib.request.HTTPRedirectHandler):
    def redirect_request(
        self,
        req: urllib.request.Request,
        fp: Any,
        code: int,
        msg: str,
        headers: Any,
        newurl: str,
    ) -> urllib.request.Request | None:
        raise urllib.error.HTTPError(
            req.full_url,
            code,
            f"redirect refused to protect Authorization header ({newurl})",
            headers,
            fp,
        )


class MulticaAPI:
    def __init__(self, config: APIConfig, timeout_seconds: float = 30.0) -> None:
        self.config = config
        self.timeout_seconds = timeout_seconds
        self.opener = urllib.request.build_opener(RefuseRedirects())

    def request(self, method: str, path: str, body: Any | None = None) -> Any:
        url = self.config.server_url + path
        headers = {
            "Authorization": f"Bearer {self.config.token}",
            "X-Workspace-ID": self.config.workspace_id,
            "Accept": "application/json",
        }
        data = None
        if body is not None:
            headers["Content-Type"] = "application/json"
            data = compact_json(body).encode("utf-8")
        request = urllib.request.Request(url, data=data, headers=headers, method=method)
        try:
            with self.opener.open(request, timeout=self.timeout_seconds) as response:
                raw = response.read().decode("utf-8")
        except urllib.error.HTTPError as exc:
            raw = exc.read().decode("utf-8", errors="replace")
            safe = redact(raw.strip(), [self.config.token])
            raise DevLoopError(
                f"Multica API {method} {path} returned HTTP {exc.code}: {safe}"
            ) from exc
        except (
            urllib.error.URLError,
            TimeoutError,
            ConnectionError,
            http.client.HTTPException,
        ) as exc:
            safe = redact(str(getattr(exc, "reason", exc)), [self.config.token])
            raise APITransportError(
                f"Multica API {method} {path} transport failed: {safe}"
            ) from exc
        if not raw.strip():
            return None
        try:
            return json.loads(raw)
        except json.JSONDecodeError as exc:
            raise DevLoopError(
                f"Multica API {method} {path} returned invalid JSON"
            ) from exc

    def stable_channel(self) -> dict[str, Any]:
        return require_mapping(
            self.request("GET", "/api/runtimes/fc-e2b/stable-channel"),
            "FC stable-channel preflight",
        )

    def templates(self) -> list[dict[str, Any]]:
        raw = require_list(
            self.request("GET", "/api/runtimes/fc-e2b/templates"),
            "FC Template catalog",
        )
        return [item for item in raw if isinstance(item, dict)]

    def runtimes(self) -> list[dict[str, Any]]:
        raw = require_list(self.request("GET", "/api/runtimes"), "Runtime list")
        return [item for item in raw if isinstance(item, dict)]

    def doctor(self) -> dict[str, Any]:
        channel = self.stable_channel()
        if channel.get("can_publish") is not True:
            raise DevLoopError(
                "the authenticated Multica user is not permitted to manage candidate "
                "FC Runtimes (stable-channel.can_publish=false)"
            )
        templates = self.templates()
        runtimes = self.runtimes()
        return {
            "server_url": self.config.server_url,
            "workspace_id": self.config.workspace_id,
            "profile_path": self.config.profile_path,
            "can_publish_candidates": True,
            "template_count": len(templates),
            "runtime_count": len(runtimes),
        }

    @staticmethod
    def find_template(
        templates: Sequence[Mapping[str, Any]], template_id: str, provider: str
    ) -> Mapping[str, Any]:
        provider = provider.strip().lower()
        selected = next(
            (item for item in templates if str(item.get("id", "")) == template_id),
            None,
        )
        if selected is None:
            raise DevLoopError(
                f"Template ID {template_id!r} is not present in the FC catalog"
            )
        if str(selected.get("status", "")).lower() != "ready":
            raise DevLoopError(
                f"Template {template_id!r} is not ready: {selected.get('status')!r}"
            )
        providers = selected.get("providers")
        if not isinstance(providers, list) or provider not in providers:
            raise DevLoopError(
                f"Template {template_id!r} does not declare provider {provider!r}; "
                f"providers={providers!r}"
            )
        return selected

    @staticmethod
    def find_runtime(
        runtimes: Sequence[Mapping[str, Any]], runtime_id: str
    ) -> Mapping[str, Any]:
        runtime = next(
            (item for item in runtimes if str(item.get("id", "")) == runtime_id),
            None,
        )
        if runtime is None:
            raise DevLoopError(
                f"Runtime {runtime_id!r} is not in the selected workspace"
            )
        return runtime

    @staticmethod
    def validate_candidate_runtime(runtime: Mapping[str, Any]) -> str:
        if runtime.get("runtime_mode") != "cloud":
            raise DevLoopError("target Runtime is not a cloud Runtime")
        metadata = runtime.get("metadata")
        if not isinstance(metadata, dict):
            raise DevLoopError("target Runtime has no cloud sandbox metadata")
        backend = metadata.get("sandbox_backend")
        if backend is None and metadata.get("kind") == "fc-e2b":
            backend = "aliyun_fc"
        if backend != "aliyun_fc":
            raise DevLoopError(
                f"target Runtime uses sandbox backend {backend!r}, expected 'aliyun_fc'"
            )
        channel = metadata.get("template_channel") or metadata.get("artifact_channel")
        if channel != "candidate":
            raise DevLoopError(
                "target Runtime is stable-managed; create a separate candidate Runtime "
                "instead of changing it in place"
            )
        provider = str(runtime.get("provider", "")).strip().lower()
        if not provider:
            raise DevLoopError("target Runtime has no provider")
        return provider

    @staticmethod
    def runtime_template_id(runtime: Mapping[str, Any]) -> str:
        metadata = runtime.get("metadata")
        if not isinstance(metadata, dict):
            return ""
        return str(metadata.get("template_id") or metadata.get("artifact_ref") or "")

    def verify_readback(
        self,
        runtime_id: str,
        template_id: str,
        provider: str,
        expected_name: str | None = None,
        expected_visibility: str | None = None,
    ) -> dict[str, Any]:
        runtime = self.find_runtime(self.runtimes(), runtime_id)
        actual_provider = self.validate_candidate_runtime(runtime)
        actual_template_id = self.runtime_template_id(runtime)
        metadata = require_mapping(
            runtime.get("metadata"), "Runtime read-back metadata"
        )
        actual_status = str(runtime.get("status", "")).strip().lower()
        actual_visibility = str(runtime.get("visibility", "")).strip().lower()
        actual_name = str(runtime.get("name", ""))
        template_status = str(metadata.get("template_status", "")).strip().lower()
        if actual_provider != provider:
            raise DevLoopError(
                f"Runtime read-back provider {actual_provider!r} != expected {provider!r}"
            )
        if actual_template_id != template_id:
            raise DevLoopError(
                f"Runtime read-back Template {actual_template_id!r} != expected "
                f"{template_id!r}"
            )
        if actual_status != "online":
            raise DevLoopError(
                f"Runtime read-back status {actual_status!r} != expected 'online'"
            )
        if template_status and template_status != "ready":
            raise DevLoopError(
                f"Runtime read-back Template status {template_status!r} != expected 'ready'"
            )
        if expected_name is not None and actual_name != expected_name:
            raise DevLoopError(
                f"Runtime read-back name {actual_name!r} != expected {expected_name!r}"
            )
        if expected_visibility is not None and actual_visibility != expected_visibility:
            raise DevLoopError(
                f"Runtime read-back visibility {actual_visibility!r} != expected "
                f"{expected_visibility!r}"
            )
        return {
            "runtime_id": runtime_id,
            "runtime_name": actual_name,
            "provider": actual_provider,
            "template_id": actual_template_id,
            "template_status": template_status or "ready-by-catalog",
            "runtime_mode": "cloud",
            "sandbox_backend": "aliyun_fc",
            "template_channel": "candidate",
            "status": actual_status,
            "visibility": actual_visibility,
            "configuration_verified": True,
        }

    def switch(self, runtime_id: str, template_id: str) -> dict[str, Any]:
        channel = self.stable_channel()
        if channel.get("can_publish") is not True:
            raise DevLoopError("candidate Runtime publisher permission is required")
        runtime = self.find_runtime(self.runtimes(), runtime_id)
        provider = self.validate_candidate_runtime(runtime)
        previous_template_id = self.runtime_template_id(runtime)
        self.find_template(self.templates(), template_id, provider)
        self.request(
            "PATCH",
            f"/api/runtimes/{urllib.parse.quote(runtime_id, safe='')}/fc-e2b-template",
            {"template_id": template_id},
        )
        result = self.verify_readback(runtime_id, template_id, provider)
        result["mutation"] = "switched"
        result["previous_template_id"] = previous_template_id
        return result

    def create(
        self,
        name: str,
        template_id: str,
        provider: str,
        visibility: str,
        reconcile_only: bool = False,
    ) -> dict[str, Any]:
        name = name.strip()
        provider = provider.strip().lower()
        visibility = visibility.strip().lower()
        if not name:
            raise DevLoopError("candidate Runtime name is required")
        channel = self.stable_channel()
        if channel.get("can_publish") is not True:
            raise DevLoopError("candidate Runtime publisher permission is required")
        self.find_template(self.templates(), template_id, provider)

        def reconcile_existing(mutation: str) -> dict[str, Any] | None:
            matches = [
                runtime
                for runtime in self.runtimes()
                if str(runtime.get("name", "")) == name
            ]
            if len(matches) > 1:
                raise DevLoopError(
                    f"candidate Runtime name {name!r} is not unique; refusing to guess"
                )
            if not matches:
                return None
            runtime_id = str(matches[0].get("id", ""))
            result = self.verify_readback(
                runtime_id,
                template_id,
                provider,
                expected_name=name,
                expected_visibility=visibility,
            )
            result["mutation"] = mutation
            return result

        existing = reconcile_existing("reused")
        if existing is not None:
            return existing
        if reconcile_only:
            raise DevLoopError(
                f"no Runtime named {name!r} is visible yet; reconcile-only mode "
                "will not POST. Repeat this read-only reconciliation after the "
                "visibility window, or obtain authoritative proof that the original "
                "request did not commit before starting a new create operation"
            )
        try:
            created_raw = self.request(
                "POST",
                "/api/runtimes/fc-e2b",
                {
                    "name": name,
                    "template_id": template_id,
                    "template_channel": "candidate",
                    "provider": provider,
                    "visibility": visibility,
                },
            )
        except APITransportError as exc:
            reconciled = reconcile_existing("reconciled_after_transport_error")
            if reconciled is not None:
                return reconciled
            raise DevLoopError(
                "candidate Runtime create outcome is unknown and no matching row is "
                "visible yet; do not rerun create normally. Re-run the same command "
                "with --reconcile-only until the row appears, or obtain authoritative "
                "proof that the request did not commit"
            ) from exc
        created = require_mapping(created_raw, "candidate Runtime create")
        runtime_id = str(created.get("id", ""))
        if not runtime_id:
            raise DevLoopError("candidate Runtime create returned no Runtime ID")
        result = self.verify_readback(
            runtime_id,
            template_id,
            provider,
            expected_name=name,
            expected_visibility=visibility,
        )
        result["mutation"] = "created"
        return result


def add_aone_args(parser: argparse.ArgumentParser) -> None:
    parser.add_argument("--repository", default=DEFAULT_REPOSITORY)
    parser.add_argument(
        "--pipeline-id",
        type=int,
        default=int(
            os.environ.get("FC_RUNTIME_CANDIDATE_PIPELINE_ID", DEFAULT_PIPELINE_ID)
        ),
    )
    parser.add_argument(
        "--pipeline-path",
        default=os.environ.get(
            "FC_RUNTIME_CANDIDATE_PIPELINE_PATH", EXPECTED_PIPELINE_PATH
        ),
    )


def add_build_args(parser: argparse.ArgumentParser) -> None:
    add_aone_args(parser)
    parser.add_argument(
        "--runtime-ref", required=True, type=runtime_ref, help="Pushed Runtime branch"
    )
    parser.add_argument(
        "--runtime-commit",
        required=True,
        type=full_commit,
        help="Expected immutable Runtime repository commit",
    )
    parser.add_argument(
        "--multica-ref",
        required=True,
        type=full_commit,
        help="Immutable dt-fde-multica commit baked into the image",
    )
    parser.add_argument("--timeout-seconds", type=positive_int, default=7200)
    parser.add_argument("--poll-interval-seconds", type=positive_float, default=20.0)
    parser.add_argument("--dry-run", action="store_true")


def add_api_args(parser: argparse.ArgumentParser) -> None:
    parser.add_argument("--profile", type=profile_name, default="pre-fde")
    parser.add_argument("--server-url")
    parser.add_argument("--workspace-id")
    parser.add_argument("--api-timeout-seconds", type=positive_float, default=30.0)


def create_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description="Build and select an FC/E2B candidate Runtime Template"
    )
    subparsers = parser.add_subparsers(dest="command", required=True)

    doctor = subparsers.add_parser("doctor", help="Read-only CI and API preflight")
    add_aone_args(doctor)
    add_api_args(doctor)
    doctor.add_argument(
        "--action", choices=("read", "create", "switch"), default="read"
    )
    doctor.add_argument("--runtime-id")

    build = subparsers.add_parser(
        "build", help="Build and inspect a candidate Template"
    )
    add_build_args(build)

    inspect_build = subparsers.add_parser(
        "inspect-build", help="Inspect an existing candidate CI run"
    )
    add_aone_args(inspect_build)
    inspect_build.add_argument("--run-id", required=True, type=int)
    inspect_build.add_argument("--runtime-ref", required=True, type=runtime_ref)
    inspect_build.add_argument("--runtime-commit", required=True, type=full_commit)
    inspect_build.add_argument("--multica-ref", required=True, type=full_commit)
    inspect_build.add_argument("--wait", action="store_true")
    inspect_build.add_argument("--timeout-seconds", type=positive_int, default=7200)
    inspect_build.add_argument(
        "--poll-interval-seconds", type=positive_float, default=20.0
    )

    switch = subparsers.add_parser(
        "switch", help="Switch an existing candidate Runtime"
    )
    add_api_args(switch)
    switch.add_argument("--template-id", required=True)
    switch.add_argument("--runtime-id", required=True)
    switch.add_argument("--dry-run", action="store_true")

    create = subparsers.add_parser("create", help="Create a candidate Runtime")
    add_api_args(create)
    create.add_argument("--template-id", required=True)
    create.add_argument("--name", required=True)
    create.add_argument("--provider", default="hermes")
    create.add_argument(
        "--visibility", choices=("private", "public"), default="private"
    )
    create.add_argument(
        "--reconcile-only",
        action="store_true",
        help="Read by unique name without POST after an uncertain create response",
    )
    create.add_argument("--dry-run", action="store_true")

    cutover = subparsers.add_parser(
        "cutover", help="Build then switch or create a candidate Runtime"
    )
    add_build_args(cutover)
    add_api_args(cutover)
    target = cutover.add_mutually_exclusive_group(required=True)
    target.add_argument("--runtime-id")
    target.add_argument("--create-name")
    cutover.add_argument("--provider", default="hermes")
    cutover.add_argument(
        "--visibility", choices=("private", "public"), default="private"
    )
    return parser


def build_plan(args: argparse.Namespace) -> dict[str, Any]:
    if (
        args.pipeline_id == DEFAULT_PIPELINE_ID
        and args.pipeline_path == EXPECTED_PIPELINE_PATH
        and args.runtime_ref != DEFAULT_PIPELINE_BRANCH
    ):
        raise DevLoopError(
            f"pipeline {DEFAULT_PIPELINE_ID} belongs to Runtime branch "
            f"{DEFAULT_PIPELINE_BRANCH!r}; pass the target branch's dedicated "
            "--pipeline-id and --pipeline-path"
        )
    command = [
        os.environ.get("A1_BIN", "a1"),
        "ci",
        "pipeline",
        "run",
        str(args.pipeline_id),
        "--repo",
        args.repository,
        "--branch",
        args.runtime_ref,
        "--param",
        f"multica_ref={args.multica_ref}",
        "-f",
        "json",
    ]
    return {
        "dry_run": True,
        "build": {
            "pipeline_id": args.pipeline_id,
            "pipeline_path_guard": args.pipeline_path,
            "repository": args.repository,
            "runtime_ref": args.runtime_ref,
            "runtime_commit": args.runtime_commit,
            "multica_commit": args.multica_ref,
            "command": command,
            "wait_for": "SUCCESS",
            "extract": sorted(TEMPLATE_FIELDS),
        },
    }


def api_plan(args: argparse.Namespace, template_id: str) -> dict[str, Any]:
    config = resolve_api_config(args, require_token=False)
    if getattr(args, "runtime_id", None):
        return {
            "server_url": config.server_url,
            "workspace_id": config.workspace_id,
            "token_source": "environment-or-profile (value never printed)",
            "preflight": [
                "GET /api/runtimes/fc-e2b/stable-channel",
                "GET /api/runtimes/fc-e2b/templates",
                "GET /api/runtimes",
            ],
            "mutation": {
                "method": "PATCH",
                "path": f"/api/runtimes/{args.runtime_id}/fc-e2b-template",
                "body": {"template_id": template_id},
            },
            "read_back": "GET /api/runtimes",
        }
    name = getattr(args, "create_name", None) or getattr(args, "name", None)
    reconcile_only = bool(getattr(args, "reconcile_only", False))
    return {
        "server_url": config.server_url,
        "workspace_id": config.workspace_id,
        "token_source": "environment-or-profile (value never printed)",
        "reconcile_only": reconcile_only,
        "preflight": [
            "GET /api/runtimes/fc-e2b/stable-channel",
            "GET /api/runtimes/fc-e2b/templates",
            "GET /api/runtimes",
        ],
        "mutation": None
        if reconcile_only
        else {
            "method": "POST",
            "path": "/api/runtimes/fc-e2b",
            "body": {
                "name": name,
                "template_id": template_id,
                "template_channel": "candidate",
                "provider": args.provider,
                "visibility": args.visibility,
            },
        },
        "read_back": "GET /api/runtimes",
    }


def build_client(args: argparse.Namespace) -> AoneClient:
    return AoneClient(args.repository, args.pipeline_id, args.pipeline_path)


def api_client(args: argparse.Namespace) -> MulticaAPI:
    return MulticaAPI(
        resolve_api_config(args), timeout_seconds=args.api_timeout_seconds
    )


def execute(args: argparse.Namespace) -> dict[str, Any]:
    if args.command == "doctor":
        if shutil.which(os.environ.get("A1_BIN", "a1")) is None:
            raise DevLoopError("a1 CLI is not installed or not on PATH")
        pipeline = build_client(args).validate_pipeline()
        api_instance = api_client(args)
        api = api_instance.doctor()
        if args.action == "switch":
            if not args.runtime_id:
                raise DevLoopError("doctor --action switch requires --runtime-id")
            target = api_instance.find_runtime(api_instance.runtimes(), args.runtime_id)
            api["target_provider"] = api_instance.validate_candidate_runtime(target)
            api["target_runtime_id"] = args.runtime_id
        api["planned_action"] = args.action
        api["mutation_authority"] = (
            "enforced by the mutation endpoint, not proven by GET"
        )
        return {
            "ok": True,
            "aone": {
                "pipeline_id": pipeline.get("id"),
                "pipeline_name": pipeline.get("name"),
                "pipeline_path": pipeline.get("path"),
                "repository": pipeline.get("repo"),
            },
            "multica": api,
        }

    if args.command == "inspect-build":
        client = build_client(args)
        client.validate_pipeline()
        if args.wait:
            run = client.wait_for_run(
                args.run_id, args.timeout_seconds, args.poll_interval_seconds
            )
        else:
            run = client.get_run(args.run_id)
        return {
            "build": client.artifact_from_run(
                run,
                expected_runtime_ref=args.runtime_ref,
                expected_runtime_commit=args.runtime_commit,
                expected_multica_commit=args.multica_ref,
            ).as_dict()
        }

    if args.command == "build":
        if args.dry_run:
            return build_plan(args)
        artifact = build_client(args).build(
            args.runtime_ref,
            args.runtime_commit,
            args.multica_ref,
            args.timeout_seconds,
            args.poll_interval_seconds,
        )
        return {"build": artifact.as_dict()}

    if args.command == "switch":
        if args.dry_run:
            return {"dry_run": True, "runtime": api_plan(args, args.template_id)}
        return {"runtime": api_client(args).switch(args.runtime_id, args.template_id)}

    if args.command == "create":
        if args.dry_run:
            return {"dry_run": True, "runtime": api_plan(args, args.template_id)}
        return {
            "runtime": api_client(args).create(
                args.name,
                args.template_id,
                args.provider,
                args.visibility,
                args.reconcile_only,
            )
        }

    if args.command == "cutover":
        if args.dry_run:
            plan = build_plan(args)
            plan["runtime"] = api_plan(args, "<template-id-from-successful-build>")
            return plan
        api = api_client(args)
        api.doctor()
        if args.runtime_id:
            # Fail before spending a CI build if the requested cutover target is
            # not itself a mutable candidate FC Runtime in this workspace.
            target = api.find_runtime(api.runtimes(), args.runtime_id)
            api.validate_candidate_runtime(target)
        artifact = build_client(args).build(
            args.runtime_ref,
            args.runtime_commit,
            args.multica_ref,
            args.timeout_seconds,
            args.poll_interval_seconds,
        )
        if args.runtime_id:
            runtime = api.switch(args.runtime_id, artifact.template_id)
        else:
            runtime = api.create(
                args.create_name,
                artifact.template_id,
                args.provider,
                args.visibility,
            )
        return {
            "build": artifact.as_dict(),
            "runtime": runtime,
            "runtime_configured": True,
            "complete": False,
            "required_next_gate": "multica_task_canary",
        }

    raise DevLoopError(f"unsupported command: {args.command}")


def main(argv: Sequence[str] | None = None) -> int:
    parser = create_parser()
    args = parser.parse_args(argv)
    try:
        result = execute(args)
    except DevLoopError as exc:
        token = os.environ.get("MULTICA_TOKEN", "")
        print(f"error: {redact(str(exc), [token])}", file=sys.stderr)
        return 1
    print_json(result)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
