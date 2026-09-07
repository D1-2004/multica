"use client";

import { ArrowUpRight, ChevronRight } from "lucide-react";
import { useWorkspacePaths } from "@multica/core/paths";
import type {
  ChatCoordinatorIssueResult,
  ChatCoordinatorTrace,
} from "@multica/core/types";
import { AppLink } from "../../navigation";
import { useT } from "../../i18n";

/** Keep committed outcomes visible even when the model's tool steps are folded. */
export function CoordinatorFinish({
  trace,
  input,
}: {
  trace: ChatCoordinatorTrace;
  input?: Record<string, unknown>;
}) {
  const { t } = useT("chat");
  const results = trace.issue_results ?? [];
  const lastStep = trace.steps?.at(-1);
  const failed = lastStep?.error === true || lastStep?.type === "error";
  const summary =
    trace.action === "reply"
      ? t(($) => $.message_list.coordinator_finish_reply)
      : trace.action === "silence"
        ? t(($) => $.message_list.coordinator_finish_silence)
        : t(($) => $.message_list.coordinator_finish_unrecorded);

  return (
    <div className="rounded-lg border border-border bg-muted/20 px-3 py-2 text-caption">
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
        <span className="font-medium text-foreground">
          {t(($) => $.message_list.coordinator_finish)}
        </span>
        <span className="text-muted-foreground">
          {failed
            ? t(($) => $.message_list.coordinator_finish_failed)
            : results.length > 0
              ? t(($) => $.message_list.coordinator_finish_results)
              : summary}
        </span>
      </div>
      {failed && (lastStep?.content || lastStep?.output) && (
        <p className="mt-1 break-words text-destructive">
          {lastStep.content || lastStep.output}
        </p>
      )}
      {results.length > 0 && (
        <ul className="mt-1.5 space-y-1.5">
          {results.map((result, index) => (
            <IssueResult
              key={`${result.issue_id}:${result.comment_id ?? index}`}
              result={result}
            />
          ))}
        </ul>
      )}
      {input && (
        <details className="group mt-1.5">
          <summary className="flex w-fit cursor-pointer list-none items-center gap-1 rounded text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring [&::-webkit-details-marker]:hidden">
            <ChevronRight
              aria-hidden="true"
              className="size-3 transition-transform group-open:rotate-90"
            />
            {t(($) => $.message_list.coordinator_finish_input)}
          </summary>
          <pre className="mt-1 max-h-48 overflow-auto whitespace-pre-wrap break-all rounded bg-muted/50 p-2 text-caption text-muted-foreground">
            {JSON.stringify(input, null, 2)}
          </pre>
        </details>
      )}
    </div>
  );
}

function IssueResult({ result }: { result: ChatCoordinatorIssueResult }) {
  const { t } = useT("chat");
  const paths = useWorkspacePaths();
  let action: string;
  switch (result.action) {
    case "issue_created":
      action = t(($) => $.message_list.coordinator_issue_created);
      break;
    case "issue_commented":
      action = t(($) => $.message_list.coordinator_issue_commented);
      break;
    default:
      action = t(($) => $.message_list.coordinator_issue_linked);
  }
  const label = result.issue_identifier?.trim() || result.issue_id;

  return (
    <li className="flex min-w-0 flex-wrap items-baseline gap-x-2 gap-y-0.5">
      <span className="shrink-0 text-muted-foreground">{action}</span>
      <AppLink
        href={paths.issueDetail(result.issue_id)}
        newTabTitle={result.issue_title || label}
        className="inline-flex min-w-0 max-w-full items-center gap-1 rounded font-medium text-foreground underline decoration-border underline-offset-4 hover:decoration-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        <span className="min-w-0 break-words">
          {label}{result.issue_title ? ` · ${result.issue_title}` : ""}
        </span>
        <ArrowUpRight aria-hidden="true" className="size-3 shrink-0" />
      </AppLink>
      {result.task_id && (
        <span className="text-muted-foreground">
          {t(($) => $.message_list.coordinator_task_enqueued)}
        </span>
      )}
    </li>
  );
}
