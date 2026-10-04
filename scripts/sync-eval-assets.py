#!/usr/bin/env python3
"""Synchronize reviewed repository Markdown into the embedded evaluation reader."""

import argparse
import hashlib
import json
from pathlib import Path
import re
import sys
from urllib.parse import quote, unquote

ROOT = Path(__file__).resolve().parents[1]
DEST = ROOT / "server/internal/evalcatalog"


def digest(data):
    return hashlib.sha256(data).hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args()
    assets = json.loads((DEST / "asset-index.json").read_text())["assets"]
    known = {a["path"] for a in assets}
    missing = {p.relative_to(ROOT).as_posix() for p in (ROOT / "docs/evals").rglob("*.md")} - known
    if missing:
        raise ValueError("unindexed eval Markdown: " + ", ".join(sorted(missing)))
    generated = {}
    index = []
    for asset in assets:
        path = Path(asset["path"])
        if path.is_absolute() or ".." in path.parts or not (ROOT / path).is_file():
            raise ValueError("invalid Markdown source: " + asset["path"])
        raw = (ROOT / path).read_bytes()
        body = raw.decode("utf-8")
        if re.search(r"(?<![A-Za-z0-9])(?:sk-|AKIA)[A-Za-z0-9_-]{20,}|-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----", body):
            raise ValueError("credential-like literal in source: " + asset["path"])

        def link(match):
            label, target = match.group(1), match.group(2)
            bare, _, fragment = target.partition("#")
            if ":" in bare or not bare.lower().endswith((".md", ".mdx")):
                return match.group(0)
            resolved = (ROOT / path.parent / unquote(bare)).resolve()
            if not resolved.is_relative_to(ROOT):
                return label
            relative = resolved.relative_to(ROOT).as_posix()
            if relative not in known:
                return match.group(0)
            href = "/evals?tab=" + asset["category"] + "&doc=" + quote(relative, safe="")
            if fragment:
                href += "#" + quote(fragment)
            return f"[{label}]({href})"

        body = re.sub(r"(?<!!)\[([^\]]+)\]\(([^\s)]+)\)", link, body)
        name = digest(asset["path"].encode())[:20] + ".md"
        generated["documents/" + name] = body.encode()
        index.append({**asset, "file": name, "sourceSHA256": digest(raw), "renderSHA256": digest(body.encode())})
    generated["document-index.json"] = (json.dumps({"assets": index}, ensure_ascii=False, indent=2) + "\n").encode()
    stale = []
    for name, data in generated.items():
        output = DEST / name
        if args.check:
            if not output.is_file() or output.read_bytes() != data:
                stale.append(name)
        else:
            output.parent.mkdir(parents=True, exist_ok=True)
            output.write_bytes(data)
    extra = {p.relative_to(DEST).as_posix() for p in (DEST / "documents").glob("*.md")} - set(generated)
    if args.check:
        stale += sorted(extra)
    else:
        for name in extra:
            (DEST / name).unlink()
    if stale:
        raise ValueError("stale Markdown snapshots; run scripts/sync-eval-assets.py: " + ", ".join(stale[:8]))
    print(f"Evaluation Markdown {'checked' if args.check else 'synced'}: {len(index)} assets")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (ValueError, KeyError, OSError) as error:
        print(str(error), file=sys.stderr)
        raise SystemExit(1)
