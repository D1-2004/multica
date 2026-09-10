"""Read one supplied JSON file and print its top-level keys."""
import json
import sys
from pathlib import Path

if len(sys.argv) != 2:
    raise SystemExit("Usage: count_fields.py <json-file>")
data = json.loads(Path(sys.argv[1]).read_text(encoding="utf-8"))
if not isinstance(data, dict):
    raise SystemExit("Expected a JSON object")
print(json.dumps({"field_count": len(data), "fields": sorted(data)}, ensure_ascii=False))
