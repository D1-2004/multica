# PRI-47: one performance rollout with agent isolation

The PRI-34 quick wins used ten separate Diamond booleans. Operators now have
one public control in `dt-fde-multica-runtime.json`:

```json
"runtime": {
  "performance_optimization": {
    "enabled": true,
    "agent_ids": ["e2293e9e-1e79-4926-b0e6-da4cb693add0"]
  }
}
```

This is one rollout object, not ten independent publication steps. A missing
object **disables the entire batch in the new binary**, even when old fields
are still present in the Diamond document. The old fields remain decode-only
to let both binary versions parse the same document during rollout. An
`enabled: false` value or an empty `agent_ids`
array selects nobody. Targets are canonical, exact agent UUIDs. No percentage,
workspace inheritance, or wildcard is supported. Keep pre and production
Diamond documents separate; this change does not edit production.

Q1 uses the resolved inbound `Turn.AgentID` and freezes its selected state for
one Coordinator decision. FC takes one config snapshot at `LaunchTask` entry,
then freezes the selected state for that task so a Diamond update cannot mix
old and new sandbox startup stages. Claimed-task prompt and skill-bundle
delivery use `task.agent_id`. The DSH event listener, bounded wait SQL, FC
abandoned-launch query, readiness reconcile scan, and ASB fast retry SQL
restrict their accelerated or terminal behavior to the same allowlist. The
old sweeper and retry conditions remain available to non-target agents.
Background workers re-read Diamond on each scan; current tasks may finish
under their frozen startup snapshot. A shutdown always drains already
tracked launches even if the switch was turned off meanwhile.

Deployment order:

1. While the old binary is still live, set all ten old pre Diamond performance
   fields to false and confirm the update on both pre replicas. This closes
   the rolling-deploy interval in which old replicas would otherwise keep
   optimizing every agent. Do not add the new field yet: old strict parsers
   would reject it.
2. Release the new binary to pre through the existing CR and pipeline 66,
   preserving other authorized release-branch changes. The new binary remains
   off when the new object is absent. Wait for every pre replica to run it.
3. Add the single object to the pre Diamond runtime document, selecting only
   approved test agents. Old fields may be removed in this same publish.
4. Verify targeted and non-targeted agents in the same pre environment with
   matching tasks: Coordinator budget/repair trace, FC startup selection,
   DSH and ASB background wakeups, claimed-task payload, and on→off→on.
   Record config revision, agent/task/trace IDs, correctness, and latency.
5. Leave production untouched. Production rollout requires a separate
   decision, verified production Diamond values, and review of changes that
   this switch cannot revert (E2B cancellation cleanup and existing schema
   migrations). Do not pass the manual pre verification gate automatically.

## Collect window

`performance_optimization.collect_quiet_ms` (500–4000) shortens the inbound
Coordinator collect silence for the selected agents, for example to 1000 ms;
absent keeps 4 s, and the 12 s window cap is unchanged. A later message that
arrives after the silence starts the scene's next window instead of joining
the one being decided. Production had 32 merged windows in 3,916 jobs over
30 days (0.8%), so measure split windows and duplicate or missed work before
widening it.

## Temporary finish schema experiment

`performance_optimization.finish_schema_experiment` isolates one Q1 part, the
finish schema pruning, for a controlled comparison:

```json
"finish_schema_experiment": {"enabled": true, "mode": "split", "salt": "<fixed per experiment>"}
```

`"mode": "expanded"` is the stop-loss: it keeps the switch on and sends the
expanded finish schema for every decision of the selected agents. Setting
`enabled: false` instead returns them to the switch's pruning.

It is absent and off by default, and it has no effect for an agent the parent
object does not select. While on, each Coordinator job is assigned once by a
salted hash of its job UUID: the `pruned` group keeps Q1 unchanged and the
`expanded` group sends the expanded finish schema; every other Q1 parameter is
the same. The same deployment order applies: deploy the binary to every
replica before adding the key, and remove the key before any binary rollback.
Changing the salt reshuffles the groups, so keep it fixed for one experiment;
a job's first group is persisted with the job, and a later claim under a
different salt or a disabled experiment is logged as `excluded_config_changed`.
The contract is in `docs/inbound-coordinator-loop.md`; remove the field and
its code when the experiment concludes.

## Where further latency work has evidence

PRI-34 found that 109/192 Coordinator main calls hit the old 1536-token cap;
8/110 sampled rounds retried the whole decision, and decision P90 was 48.8s.
Q1 now raises the budget and narrows finish repair, but the enabled repair
branch has not yet been observed under a real truncation fault. Measure
external event→decision with retry count and result quality before claiming
an overall improvement. A controlled malformed-finish fixture is the next
high-value acceptance case.

The next large share is after task start: PRI-34 measured task start→first
DingTalk reply P50 81s, with one sampled task spending five LLM turns (about
45s) discovering DWS help. Q5 provides a ready reply command, but one live
ON task still chose help; injection alone cannot guarantee fewer turns. Check
the actual first-turn prompt, tool choice, and final answer correctness on
matched samples before considering a narrower reply path or protocol changes.

Normal FC cold startup was already P99 11.7s, while hot startup was P99 9.3s.
The 454s FC outlier came from a dropped launch during deployment, addressed
by Q4 but not yet fault-tested end to end. Further steady-state FC micro
optimizations are lower priority than proving Q4 correctness and reducing
Coordinator/agent LLM round trips. These figures are historical baseline,
not post-change benefit claims.
