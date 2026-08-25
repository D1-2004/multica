"use client";

import { useEffect, useState } from "react";
import { Loader2, Plus, Save, X } from "lucide-react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import type { Agent, AgentOKRSpend } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { useT } from "../../../i18n";

type DraftOKR = { objective: string; keyResults: string[] };

// Ticks are the integer cost unit the usage tables store; the divisor matches
// the one the label usage page and runtime utils already use.
const COST_USD_TICKS_PER_USD = 10_000_000_000;

function formatSpendUsd(ticks: number): string {
  const usd = ticks / COST_USD_TICKS_PER_USD;
  if (usd === 0) return "$0.00";
  // Anything that rounds to zero at two decimals is still real money spent —
  // showing "$0.00" next to 40 tasks reads as a bug, not as "almost nothing".
  if (usd < 0.01) return "<$0.01";
  if (usd >= 100) return `$${usd.toFixed(0)}`;
  return `$${usd.toFixed(2)}`;
}

/**
 * Cost of the work tagged with this objective or key result.
 *
 * Rendered only once the entry exists server-side: a row the owner just typed
 * has no label yet, so it has nothing to have spent.
 */
function SpendChip({ spend }: { spend: AgentOKRSpend | undefined }) {
  const { t } = useT("agents");
  if (!spend || spend.task_count === 0) return null;

  const cost = formatSpendUsd(spend.total_cost_usd_ticks);
  const partial = spend.unpriced_task_count > 0;

  return (
    <span
      className="ml-auto flex shrink-0 items-baseline gap-2 pl-2 text-caption tabular-nums text-muted-foreground"
      title={
        partial
          ? t(($) => $.tab_body.okr.spend_partial_hint)
          : t(($) => $.tab_body.okr.spend_hint)
      }
    >
      <span className="font-medium text-foreground">
        {cost}
        {partial ? "*" : ""}
      </span>
      <span>
        {t(($) => $.tab_body.okr.spend_tasks, { count: spend.task_count })}
      </span>
    </span>
  );
}

/**
 * Agent OKRs. Each objective and key result becomes a real workspace label, and
 * the catalog is injected into the agent's instructions so it tags issues from
 * a fixed set instead of inventing a tag per run.
 *
 * The editor is whole-set: OKRs are read as a list, and a rewrite keeps ordering
 * and the objective -> key result grouping unambiguous without a per-row
 * reconciliation protocol.
 */
export function OKRTab({
  agent,
  onDirtyChange,
  readOnly = false,
}: {
  agent: Agent;
  onDirtyChange?: (dirty: boolean) => void;
  readOnly?: boolean;
}) {
  const { t } = useT("agents");
  const queryClient = useQueryClient();
  const [draft, setDraft] = useState<DraftOKR[]>([]);
  const [baseline, setBaseline] = useState<string>("[]");

  const { data: okrResponse, isLoading } = useQuery({
    queryKey: ["agent-okrs", agent.id],
    queryFn: () => api.listAgentOKRs(agent.id),
    retry: false,
  });
  const okrs = okrResponse?.okrs;

  useEffect(() => {
    if (!okrs) return;
    const loaded = okrs.map((okr) => ({
      objective: okr.objective,
      keyResults: okr.key_results.map((keyResult) => keyResult.text),
    }));
    setDraft(loaded);
    setBaseline(JSON.stringify(loaded));
  }, [okrs]);

  const isDirty = JSON.stringify(draft) !== baseline;

  // Spend belongs to what is saved, not to what is being typed. Index by
  // position against the loaded set so an unsaved edit never shows a cost that
  // belongs to a different entry.
  const savedSpend = (okrIndex: number, keyResultIndex?: number) => {
    if (isDirty || okrResponse?.usage_available === false) return undefined;
    const saved = okrs?.[okrIndex];
    if (!saved) return undefined;
    return keyResultIndex === undefined
      ? saved.spend
      : saved.key_results[keyResultIndex]?.spend;
  };

  useEffect(() => {
    onDirtyChange?.(isDirty);
  }, [isDirty, onDirtyChange]);

  const save = useMutation({
    mutationFn: () =>
      api.setAgentOKRs(
        agent.id,
        draft
          .map((okr) => ({
            objective: okr.objective.trim(),
            key_results: okr.keyResults
              .map((keyResult) => keyResult.trim())
              .filter(Boolean),
          }))
          // A blank objective row is an editing artifact, not a submission.
          .filter((okr) => okr.objective !== ""),
      ),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["agent-okrs", agent.id] });
    },
  });

  const updateObjective = (index: number, value: string) => {
    setDraft((current) =>
      current.map((okr, i) =>
        i === index ? { ...okr, objective: value } : okr,
      ),
    );
  };

  const updateKeyResult = (
    okrIndex: number,
    keyResultIndex: number,
    value: string,
  ) => {
    setDraft((current) =>
      current.map((okr, i) =>
        i === okrIndex
          ? {
              ...okr,
              keyResults: okr.keyResults.map((keyResult, j) =>
                j === keyResultIndex ? value : keyResult,
              ),
            }
          : okr,
      ),
    );
  };

  if (isLoading) {
    return (
      <div className="flex items-center gap-2 py-8 text-caption text-muted-foreground">
        <Loader2
          className="h-3.5 w-3.5 animate-spin motion-reduce:animate-none"
          aria-hidden="true"
        />
        {t(($) => $.tab_body.okr.loading)}
      </div>
    );
  }

  return (
    <div className="space-y-5">
      <p className="max-w-2xl text-pretty text-body leading-6 text-muted-foreground">
        {t(($) => $.tab_body.okr.intro)}
      </p>

      {okrResponse?.usage_available === false ? (
        <p className="text-caption text-warning">
          {t(($) => $.tab_body.okr.spend_unavailable)}
        </p>
      ) : null}

      {draft.length === 0 && (
        <p className="text-caption text-muted-foreground">
          {t(($) => $.tab_body.okr.empty)}
        </p>
      )}

      <div className="space-y-4">
        {draft.map((okr, okrIndex) => (
          <div key={okrIndex} className="space-y-2 rounded-lg border p-4">
            <div className="flex items-center gap-2">
              <span className="shrink-0 rounded bg-muted px-1.5 py-0.5 text-caption font-medium">
                O
              </span>
              <Input
                value={okr.objective}
                onChange={(event) =>
                  updateObjective(okrIndex, event.target.value)
                }
                placeholder={t(($) => $.tab_body.okr.objective_placeholder)}
                disabled={readOnly}
                aria-label={t(($) => $.tab_body.okr.objective_label)}
              />
              <SpendChip spend={savedSpend(okrIndex)} />
              {!readOnly && (
                <Button
                  size="xs"
                  variant="ghost"
                  aria-label={t(($) => $.tab_body.okr.remove_objective)}
                  onClick={() =>
                    setDraft((current) =>
                      current.filter((_, i) => i !== okrIndex),
                    )
                  }
                >
                  <X className="h-3.5 w-3.5" aria-hidden="true" />
                </Button>
              )}
            </div>

            <div className="space-y-2 pl-6">
              {okr.keyResults.map((keyResult, keyResultIndex) => (
                <div key={keyResultIndex} className="flex items-center gap-2">
                  <span className="shrink-0 rounded bg-muted px-1.5 py-0.5 text-caption font-medium">
                    KR
                  </span>
                  <Input
                    value={keyResult}
                    onChange={(event) =>
                      updateKeyResult(
                        okrIndex,
                        keyResultIndex,
                        event.target.value,
                      )
                    }
                    placeholder={t(($) => $.tab_body.okr.key_result_placeholder)}
                    disabled={readOnly}
                    aria-label={t(($) => $.tab_body.okr.key_result_label)}
                  />
                  <SpendChip spend={savedSpend(okrIndex, keyResultIndex)} />
                  {!readOnly && (
                    <Button
                      size="xs"
                      variant="ghost"
                      aria-label={t(($) => $.tab_body.okr.remove_key_result)}
                      onClick={() =>
                        setDraft((current) =>
                          current.map((entry, i) =>
                            i === okrIndex
                              ? {
                                  ...entry,
                                  keyResults: entry.keyResults.filter(
                                    (_, j) => j !== keyResultIndex,
                                  ),
                                }
                              : entry,
                          ),
                        )
                      }
                    >
                      <X className="h-3.5 w-3.5" aria-hidden="true" />
                    </Button>
                  )}
                </div>
              ))}
              {!readOnly && (
                <Button
                  size="xs"
                  variant="outline"
                  onClick={() =>
                    setDraft((current) =>
                      current.map((entry, i) =>
                        i === okrIndex
                          ? { ...entry, keyResults: [...entry.keyResults, ""] }
                          : entry,
                      ),
                    )
                  }
                >
                  <Plus className="h-3.5 w-3.5" aria-hidden="true" />
                  {t(($) => $.tab_body.okr.add_key_result)}
                </Button>
              )}
            </div>
          </div>
        ))}
      </div>

      {!readOnly && (
        <div className="flex flex-wrap items-center justify-between gap-3">
          <Button
            size="sm"
            variant="outline"
            onClick={() =>
              setDraft((current) => [
                ...current,
                { objective: "", keyResults: [""] },
              ])
            }
          >
            <Plus className="h-3.5 w-3.5" aria-hidden="true" />
            {t(($) => $.tab_body.okr.add_objective)}
          </Button>
          <div className="flex items-center gap-3">
            {isDirty && (
              <span className="text-caption text-muted-foreground">
                {t(($) => $.tab_body.common.unsaved_changes)}
              </span>
            )}
            <Button
              size="sm"
              onClick={() => save.mutate()}
              disabled={!isDirty || save.isPending}
            >
              {save.isPending ? (
                <Loader2
                  className="h-3.5 w-3.5 animate-spin motion-reduce:animate-none"
                  aria-hidden="true"
                />
              ) : (
                <Save className="h-3.5 w-3.5" aria-hidden="true" />
              )}
              {t(($) => $.tab_body.common.save)}
            </Button>
          </div>
        </div>
      )}

      <p className="text-caption leading-snug text-muted-foreground">
        {t(($) => $.tab_body.okr.label_hint)}
      </p>
    </div>
  );
}
