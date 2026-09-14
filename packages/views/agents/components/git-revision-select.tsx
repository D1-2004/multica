"use client";

import { useState } from "react";
import type { AgentSourceBranches } from "@multica/core/types";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import { useT } from "../../i18n";

export function GitRevisionSelect({ id, value, onChange, onReadyChange, revisions, disabled, allowDefault = false }: {
  id: string; value: string; onChange: (value: string) => void;
  onReadyChange: (ready: boolean) => void;
  revisions?: AgentSourceBranches; disabled?: boolean; allowDefault?: boolean;
}) {
  const { t } = useT("agents");
  const [kind, setKind] = useState(value.startsWith("refs/tags/") ? "tag" : /^[0-9a-f]{7,64}$/i.test(value) ? "commit" : "branch");
  const options = kind === "tag" ? revisions?.tags : kind === "branch" ? revisions?.branches : [];
  const text = value.replace(/^refs\/(heads|tags)\//, "");
  const change = (nextKind: string, next: string) => {
    const trimmed = next.trim();
    onReadyChange(nextKind === "commit" ? /^[0-9a-f]{7,40}$|^[0-9a-f]{64}$/i.test(trimmed) : !!trimmed || (allowDefault && nextKind === "branch"));
    onChange(next && nextKind !== "commit" ? `refs/${nextKind === "tag" ? "tags" : "heads"}/${next}` : next);
  };
  return <fieldset disabled={disabled} className="space-y-2">
    <Label htmlFor={`${id}-kind`}>{t(($) => $.git_revision.type)}</Label>
    <select id={`${id}-kind`} className="h-9 w-full rounded-md border bg-background px-3 text-body" value={kind} onChange={(event) => { setKind(event.target.value); change(event.target.value, ""); }}>
      <option value="branch">{t(($) => $.git_revision.branch)}</option>
      <option value="tag">{t(($) => $.git_revision.tag)}</option>
      <option value="commit">{t(($) => $.git_revision.commit)}</option>
    </select>
    <Label htmlFor={id}>{t(($) => $.tab_body.publish.branch)}</Label>
    <Input id={id} list={kind === "commit" ? undefined : `${id}-options`} value={text}
      placeholder={kind === "commit" ? t(($) => $.git_revision.commit_hint) : allowDefault && kind === "branch" ? revisions?.default_branch || t(($) => $.creation_studio.git.default_branch) : undefined}
      onChange={(event) => change(kind, event.target.value)} />
    <datalist id={`${id}-options`}>{options?.map((item) => <option key={item.name} value={item.name}>{item.commit.sha.slice(0, 12)}</option>)}</datalist>
  </fieldset>;
}
