"use client";

import type { AgentPackageRequirements } from "@multica/core/types";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import { useT } from "../../i18n";

export function PackageRequirementsForm({ requirements, secrets, onSecretsChange, deferBindings, onDeferChange, disabled }: {
  requirements?: AgentPackageRequirements;
  secrets: Record<string, string>;
  onSecretsChange: (values: Record<string, string>) => void;
  deferBindings: boolean;
  onDeferChange: (value: boolean) => void;
  disabled: boolean;
}) {
  const { t } = useT("agents");
  return <div className="space-y-4">
              {requirements?.secrets.map((ref) => <div className="space-y-2" key={ref}>
                <Label htmlFor={`package-secret-${ref}`}>{t(($) => $.creation_studio.local.secret, { ref })}</Label>
                <Input id={`package-secret-${ref}`} type="password" autoComplete="new-password" value={secrets[ref] ?? ""} disabled={disabled} onChange={(event) => onSecretsChange({ ...secrets, [ref]: event.target.value })} />
              </div>)}
              {!!requirements?.deferred_bindings.length && <div className="space-y-2 rounded border p-3">
                <p className="text-body">{t(($) => $.creation_studio.local.bindings_required)}</p>
                <ul className="list-inside list-disc text-caption">{requirements.deferred_bindings.map((field) => <li key={field}>{field}</li>)}</ul>
                <label className="flex items-start gap-2 text-body"><input type="checkbox" checked={deferBindings} disabled={disabled} onChange={(event) => onDeferChange(event.target.checked)} />{t(($) => $.creation_studio.local.defer_bindings)}</label>
              </div>}
  </div>;
}
