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
              {!!requirements?.binding_declarations?.length && <section className="space-y-3 rounded-md border p-4" aria-label={t(($) => $.package_bindings.declarations_title)}>
                <h3 className="text-title-sm font-medium">{t(($) => $.package_bindings.declarations_title)}</h3>
                <p className="text-caption text-muted-foreground">{t(($) => $.package_bindings.declarations_description)}</p>
                <dl className="space-y-3">
                  {requirements.binding_declarations.map((item) => <div key={item.path} className="space-y-1 text-caption">
                    <dt className="break-all font-mono">{item.path}</dt>
                    <dd>
                      {item.declaration === null || (Array.isArray(item.declaration) && item.declaration.length === 0)
                        ? <p className="text-muted-foreground">{t(($) => $.package_bindings.declaration_clear)}</p>
                        : <pre className="overflow-x-auto whitespace-pre-wrap break-all rounded bg-muted p-2">{JSON.stringify(item.declaration, null, 2)}</pre>}
                    </dd>
                  </div>)}
                </dl>
              </section>}
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
