#!/usr/bin/env python3
"""Pre-render image fixtures (one PNG per var_sets row) for cases-v2.

The harness is stdlib-only and cannot draw text, so image fixtures are rendered
once with Pillow and committed: cases/v2/fixtures/<case>/<fixture>.row<N>.png.
Rerun after changing a case's `render` text or var_sets:

  python3 scripts/employee-e2e/cases/v2/fixtures/render_png.py
"""

from __future__ import annotations

import json
import re
from pathlib import Path

from PIL import Image, ImageDraw, ImageFont

HERE = Path(__file__).resolve().parent
SUITES = ["G", "M", "C", "P", "T"]
FONT = "/System/Library/Fonts/Hiragino Sans GB.ttc"
VAR = re.compile(r"\{([A-Z][A-Z0-9_]*)\}")


def dialog(code: str) -> Image.Image:
    """A back-office export dialog like a phone screenshot of the error (M-08)."""
    img = Image.new("RGB", (720, 420), (243, 244, 246))
    d = ImageDraw.Draw(img)
    title, body, small = (ImageFont.truetype(FONT, size) for size in (30, 26, 22))
    d.rounded_rectangle((60, 60, 660, 360), radius=18, fill=(255, 255, 255), outline=(220, 222, 226), width=2)
    d.text((100, 95), "数据导出", fill=(31, 35, 41), font=title)
    d.text((100, 165), f"导出失败（{code}）：", fill=(220, 38, 38), font=body)
    d.text((100, 210), "本月导出次数已达上限，请联系管理员", fill=(220, 38, 38), font=body)
    d.rounded_rectangle((470, 285, 620, 335), radius=10, fill=(37, 99, 235))
    d.text((520, 296), "知道了", fill=(255, 255, 255), font=small)
    return img


RENDERERS = {("M-08", "shot"): lambda row: dialog(row["CODE"])}


def main() -> None:
    for suite in SUITES:
        for case in json.loads((HERE.parent / f"{suite}.json").read_text(encoding="utf-8"))["cases"]:
            for key, fx in (case.get("fixtures") or {}).items():
                if "render" not in fx:
                    continue
                draw = RENDERERS.get((case["id"], key))
                if draw is None:
                    raise SystemExit(f"no renderer for {case['id']}.{key}")
                out_dir = HERE / case["id"]
                out_dir.mkdir(exist_ok=True)
                for i, row in enumerate(case.get("var_sets") or [{}]):
                    path = out_dir / f"{key}.row{i}.png"
                    draw(row).save(path, optimize=True)
                    print(path.relative_to(HERE))


if __name__ == "__main__":
    main()
