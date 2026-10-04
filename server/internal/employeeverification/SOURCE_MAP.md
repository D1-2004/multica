# Employee verification source map

This package is Multica code. It contains **no verbatim GawkBot code**; the
mechanisms below were re-implemented from the design of GawkBot
(`github.com/nex-crm/wuphf`, Sustainable Use License v1.0, Copyright (c) 2026
Nex) at commit `71e82a1809565281cbd0bf8185d3c125b715d934`. If verbatim code is
copied later, register the symbol here and add the upstream license file.

| Upstream design | Adopted mechanism | Multica adaptation |
| --- | --- | --- |
| `internal/team/task_verification.go` | Verification gate; peek under lock, run the check unlocked, re-find and write back under lock; discard on a spec change; unknown kinds fail closed; check where the task's work really lives | PostgreSQL transactions replace the broker mutex. The Task row lock and the spec revision replace the in-memory spec comparison; an evidence-manifest digest also discards results when artifacts change mid-check. The "real workdir" rule becomes "only artifacts bound to this exact Task/Run/queue/goal revision": Host-held artifacts, or files the Run's queue execution delivered into the origin conversation (provider-confirmed `sandbox_send_receipt`), whose bytes the Host downloads as the agent identity (G1.1). No command or URL is executed by the Host: commands stay `unknown`, and sandbox exit codes and assistant text are never evidence. Results are immutable records keyed by (run, check, evidence) with a checker version. |
| `internal/team/task_dod_derive.go` | Conservative derivation of required checks from explicit done-criteria cues in the human's own words; prefer a miss over a false check; backtick commands only after a cue | Chinese and English cue/pattern table (file exists/contains, CSV/TSV rows and columns, expected value bound to a produced file, explicit command). Han file names are accepted only inside quotes. Model-written success criteria are stored as `model_proposed` and stay inactive until the requester confirms. |
| `internal/team/task_distill.go` | Distill only verified outcomes (Trusted, confidence 7) and surface write failures | A durable intent row is written in the verification transaction; `ProcessVerifiedDistill` consumes it under Task/intent locks with `employeememory.DistillTx`, replacing the post-commit goroutine and process-local single flight. Scope is requester-private for human Tasks; automation sources never use a human namespace. |

Upstream file SHA-256 values at the pinned commit:

```text
8d6c20bdcbccba574b0862ef8f6ad3d0fb62815dddc922aa9a40fab6f95e39d9  internal/team/task_verification.go
5e3d0c4b2947e22022b39f0b43f6ac1ec40bcdc2c0aa0141d393d365a76cbe45  internal/team/task_dod_derive.go
a52522932758ce33b4f4014e1525884e844817ea7d89c3c6a950f2d4f863cb86  internal/team/task_distill.go
fc468cc42d61e463265c07994c99161a11cbede8ea48d5ef45a5981bcdb5cad0  LICENSE
```
