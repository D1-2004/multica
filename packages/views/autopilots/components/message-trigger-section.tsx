"use client";

import { useId } from "react";
import { useQuery } from "@tanstack/react-query";
import { dingtalkAccountBindingsOptions } from "@multica/core/dingtalk-account-bindings";
import { Input } from "@multica/ui/components/ui/input";
import { useT } from "../../i18n";

export type EditableTriggerKind = "schedule" | "webhook" | "dingtalk_message";

export function useMessageTriggerEligibility(wsId: string, assigneeType: string, assigneeId: string) {
  const { data, isPending } = useQuery(dingtalkAccountBindingsOptions(wsId));
  const binding = data?.bindings?.find((item) => item.agentId === assigneeId);
  return {
    eligible: assigneeType === "agent" && binding?.messageRoute?.status === "active",
    loading: isPending,
  };
}

export function validMessageInterval(value: number) {
  return Number.isInteger(value) && value >= 1 && value <= 1440;
}

export function TriggerKindPicker({ kind, onChange, messageEligible, disabled = false }: {
  kind: EditableTriggerKind;
  onChange: (value: EditableTriggerKind) => void;
  messageEligible: boolean;
  disabled?: boolean;
}) {
  const { t } = useT("autopilots");
  const choices: [EditableTriggerKind, string][] = [
    ["schedule", t(($) => $.dialog.trigger_kind_schedule)],
    ["webhook", t(($) => $.dialog.trigger_kind_webhook)],
    ["dingtalk_message", t(($) => $.message_trigger.kind)],
  ];
  return <div className="space-y-2">
    <div role="radiogroup" aria-label={t(($) => $.dialog.section_trigger_kind)} className="flex flex-col gap-1">
      {choices.map(([value, label]) => <button key={value} type="button" role="radio"
        aria-checked={kind === value} disabled={disabled || (value === "dingtalk_message" && !messageEligible)}
        onClick={() => onChange(value)}
        className={`rounded-md border px-3 py-2 text-left text-body transition-colors disabled:opacity-50 ${kind === value ? "border-primary bg-primary/10 font-medium" : "border-transparent hover:bg-accent"}`}>
        {label}
      </button>)}
    </div>
    {!messageEligible && <p className="text-caption text-muted-foreground">{t(($) => $.message_trigger.binding_required)}</p>}
  </div>;
}

export function MessageTriggerSection({ minutes, onChange, eligible, disabled = false }: {
  minutes: number;
  onChange: (minutes: number) => void;
  eligible: boolean;
  disabled?: boolean;
}) {
  const { t } = useT("autopilots");
  const id = useId();
  const valid = validMessageInterval(minutes);
  const example = {
    event: "dingtalk.messages.received",
    window_id: "example-window",
    window_start: "2026-09-10T06:00:00Z",
    window_end: new Date(Date.UTC(2026, 8, 10, 6, valid ? minutes : 5)).toISOString(),
    merge_interval_minutes: valid ? minutes : 5,
    message_count: 8,
    mention_count: 1,
    conversation_count: 2,
    conversations: [
      { conversation_id: "cid-example-group", conversation_cid: "12345", title: t(($) => $.message_trigger.example_group), type: "group", message_count: 6, mention_count: 1, first_message_at: "2026-09-10T06:00:10Z", last_message_at: "2026-09-10T06:00:40Z" },
      { conversation_id: "cid-example-direct", conversation_cid: "67890", title: t(($) => $.message_trigger.example_direct), type: "single", message_count: 2, mention_count: 0, first_message_at: "2026-09-10T06:00:20Z", last_message_at: "2026-09-10T06:00:50Z" },
    ],
  };
  return <section className="space-y-3" aria-label={t(($) => $.message_trigger.kind)}>
    <div className="space-y-2">
      <label htmlFor={id} className="text-body font-medium">{t(($) => $.message_trigger.interval)}</label>
      <div className="flex items-center gap-2">
        <Input id={id} type="number" min={1} max={1440} step={1} value={Number.isNaN(minutes) ? "" : minutes}
          onChange={(event) => onChange(event.target.value === "" ? NaN : Number(event.target.value))}
          disabled={disabled} aria-invalid={!valid} aria-describedby={`${id}-help`} className="w-24" />
        <span className="text-body">{t(($) => $.message_trigger.minutes)}</span>
      </div>
      {!valid && <p role="alert" className="text-caption text-destructive">{t(($) => $.message_trigger.invalid_interval)}</p>}
      {!eligible && <p role="alert" className="text-caption text-destructive">{t(($) => $.message_trigger.binding_required)}</p>}
      <p id={`${id}-help`} className="text-caption text-muted-foreground">{t(($) => $.message_trigger.help)}</p>
      <p className="text-caption text-muted-foreground">{t(($) => $.message_trigger.scope)}</p>
    </div>
    <div className="rounded-lg border bg-background p-3 space-y-2">
      <h3 className="text-body font-medium">{t(($) => $.message_trigger.data_title)}</h3>
      <p className="text-caption text-muted-foreground">{t(($) => $.message_trigger.data_help)}</p>
      <p className="text-caption font-medium">{t(($) => $.message_trigger.total)}</p>
      <table className="w-full text-caption">
        <caption className="sr-only">{t(($) => $.message_trigger.example)}</caption>
        <thead><tr className="text-left text-muted-foreground"><th className="py-1 font-normal">{t(($) => $.message_trigger.conversation)}</th><th className="text-right font-normal">{t(($) => $.message_trigger.messages)}</th><th className="text-right font-normal">{t(($) => $.message_trigger.mentions)}</th></tr></thead>
        <tbody>{example.conversations.map((c) => <tr key={c.conversation_id}><td className="py-1">{c.title}</td><td className="text-right tabular-nums">{c.message_count}</td><td className="text-right tabular-nums">{c.mention_count}</td></tr>)}</tbody>
      </table>
      <details><summary className="cursor-pointer text-caption text-muted-foreground">{t(($) => $.message_trigger.json)}</summary>
        <pre className="mt-2 max-h-56 overflow-auto rounded bg-muted p-2 text-micro">{JSON.stringify(example, null, 2)}</pre>
      </details>
    </div>
  </section>;
}
