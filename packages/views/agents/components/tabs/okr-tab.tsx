"use client";

import { useEffect, useState } from "react";
import { Loader2, Plus, Save, X } from "lucide-react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import type { Agent } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { useT } from "../../../i18n";

type DraftOKR = { objective: string; keyResults: string[] };

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

  const { data: okrs, isLoading } = useQuery({
    queryKey: ["agent-okrs", agent.id],
    queryFn: () => api.listAgentOKRs(agent.id),
    retry: false,
  });

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
