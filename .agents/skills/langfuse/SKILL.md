---
name: langfuse
description: >
  本仓预发 Langfuse（unify-aipilot，v3）。查 Coordinator / 沙箱 traces 用
  inspect-langfuse 和 scripts/query-langfuse.sh。凭证 ~/.grok/langfuse.env。
  通用 CLI/文档见 ~/.agents/skills/langfuse/SKILL.md。
---

# Langfuse（本仓）

凭证只放本机 `~/.grok/langfuse.env`（`LANGFUSE_PUBLIC_KEY` / `LANGFUSE_SECRET_KEY` / `LANGFUSE_HOST`），mode 600。不要提交、不要贴进聊天。

侦测闭环：

- Host 召回 / 裁决 → skill `inspect-coordinator-sls`
- generation I/O、沙箱 `agent_task` → skill `inspect-langfuse`
- 脚本：`scripts/query-langfuse.sh`

本机 self-hosted 是 Langfuse **3.x**。不要用 `npx langfuse-cli api observations list`（打 v2，404）。

通用 Langfuse API/文档：`~/.agents/skills/langfuse/SKILL.md`。
