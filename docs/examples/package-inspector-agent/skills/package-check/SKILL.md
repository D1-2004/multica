---
name: package-check
description: 核对 Agent 清单、引用文件及配置导入结果
---

# Package check

Read references/checklist.md and templates/report.md. Compare each declared file with the actual package and report missing content. To count top-level JSON fields, run `python3 scripts/count_fields.py <json-file>` from this skill directory. The script reads only that file and performs no network operations or writes.
