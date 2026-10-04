"use client";

import { useCallback, useEffect, useState } from "react";
import { Check, Copy } from "lucide-react";
import { api } from "@multica/core/api";
import type { ConnectorApp, ConnectorResolveResult, SettingsConnectorSpec } from "@multica/core/api";
import { useCurrentWorkspace } from "@multica/core/paths";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { toast } from "sonner";
import { AtlassianDomainNote } from "../../common/atlassian-domain-note";
import { ConnectorMark } from "../../common/connector-logo";
import { useT } from "../../i18n";
import { SettingsCard, SettingsTab } from "./settings-layout";

const FALLBACK_CALLBACK = "https://fde-workbench.dingtalk.com/api/connectors/oauth/callback";

const QUALIFIER: Record<string, string> = {
  github: "GitHub App",
  google: "Drive / Gmail / Calendar",
  atlassian: "Jira / Confluence",
};

function errorText(error: unknown, fallback: string) {
  return error instanceof Error && error.message ? error.message : fallback;
}

type StatusKind = "configured" | "needs" | "automatic" | "env" | "limited" | "disabled";

function savedFor(apps: ConnectorApp[], slug: string) {
  const rows = apps.filter((app) => app.provider === slug);
  return rows.find((app) => app.active_for_catalog) ?? rows.find((app) => app.enabled) ?? rows[0];
}

function statusOf(spec: SettingsConnectorSpec, saved: ConnectorApp | undefined): StatusKind {
  if (spec.mode === "limited") return "limited";
  if (spec.mode === "dcr") return "automatic";
  if (saved?.client_secret_set && saved.enabled) return "configured";
  if (saved && !saved.enabled && saved.client_secret_set) return "disabled";
  if (!saved?.client_secret_set && spec.env_configured) return "env";
  return "needs";
}

export function ConnectorsTab() {
  const { t } = useT("settings");
  const workspaceId = useCurrentWorkspace()?.id ?? "";
  const [apps, setApps] = useState<ConnectorApp[]>([]);
  const [catalog, setCatalog] = useState<SettingsConnectorSpec[]>([]);
  const [callbackUrl, setCallbackUrl] = useState(FALLBACK_CALLBACK);
  const [loading, setLoading] = useState(true);
  const [openSlug, setOpenSlug] = useState<string | null>(null);

  const load = useCallback(async () => {
    if (!workspaceId) return;
    try {
      const page = await api.listConnectorApps(workspaceId);
      setApps(page.apps ?? []);
      setCatalog(page.catalog ?? []);
      if (page.callback_url) setCallbackUrl(page.callback_url);
    } catch (error) {
      toast.error(errorText(error, t(($) => $.connectors.load_failed)));
    } finally {
      setLoading(false);
    }
  }, [t, workspaceId]);

  useEffect(() => { void load(); }, [load]);

  const statusLabel = (kind: StatusKind) => {
    switch (kind) {
      case "configured": return t(($) => $.connectors.status_configured);
      case "needs": return t(($) => $.connectors.status_needs);
      case "automatic": return t(($) => $.connectors.status_automatic);
      case "env": return t(($) => $.connectors.status_env);
      case "limited": return t(($) => $.connectors.status_limited);
      case "disabled": return t(($) => $.connectors.status_disabled);
    }
  };

  return (
    <SettingsTab title={t(($) => $.connectors.title)} description={t(($) => $.connectors.description)}>
      {loading ? null : (
        <div className="space-y-3">
          {catalog.map((spec) => {
            const saved = savedFor(apps, spec.slug);
            const kind = statusOf(spec, saved);
            const quiet = kind === "automatic" || kind === "limited";
            const name = QUALIFIER[spec.slug] ? `${spec.name}（${QUALIFIER[spec.slug]}）` : spec.name;
            return (
              <div key={spec.slug}>
                <div className={quiet ? "opacity-60" : undefined}>
                <div className="flex flex-wrap items-center gap-3 rounded-lg border border-surface-border px-3 py-3">
                  <span className="flex size-9 shrink-0 items-center justify-center rounded-md border border-surface-border bg-muted">
                    <ConnectorMark slug={spec.slug} className="size-5" />
                  </span>
                  <div className="min-w-0 flex-1">
                    <div className="flex flex-wrap items-center gap-2">
                      <h3 className="text-body font-semibold">{name}</h3>
                      {spec.later ? <span className="text-caption text-muted-foreground">{t(($) => $.connectors.later)}</span> : null}
                    </div>
                    {kind === "configured" || kind === "disabled" ? (
                      <p className="truncate text-caption text-muted-foreground">
                        {saved?.client_id}
                        {saved?.client_secret_hint ? ` · ${saved.client_secret_hint}` : ""}
                      </p>
                    ) : null}
                  </div>
                  <span className="rounded-full border border-surface-border px-2 py-0.5 text-caption">{statusLabel(kind)}</span>
                  {quiet ? null : (
                    <div className="flex gap-2">
                      <Button type="button" size="sm" variant="outline" onClick={() => setOpenSlug(openSlug === spec.slug ? null : spec.slug)}>
                        {kind === "configured" || kind === "disabled" ? t(($) => $.connectors.edit) : t(($) => $.connectors.fill)}
                      </Button>
                      {saved && kind === "configured" ? (
                        <Button type="button" size="sm" variant="outline" onClick={() => void toggleApp(workspaceId, saved, false, load).catch((error: unknown) => {
                          toast.error(errorText(error, t(($) => $.connectors.load_failed)));
                        })}>
                          {t(($) => $.connectors.disable_action)}
                        </Button>
                      ) : null}
                      {saved && kind === "disabled" ? (
                        <Button type="button" size="sm" variant="outline" onClick={() => void toggleApp(workspaceId, saved, true, load).catch((error: unknown) => {
                          toast.error(errorText(error, t(($) => $.connectors.load_failed)));
                        })}>
                          {t(($) => $.connectors.enable_action)}
                        </Button>
                      ) : null}
                    </div>
                  )}
                </div>
                {openSlug === spec.slug && !quiet ? (
                  <AppDetail
                    spec={spec}
                    saved={saved}
                    workspaceId={workspaceId}
                    callbackUrl={callbackUrl}
                    onChanged={load}
                  />
                ) : null}
                </div>
                {spec.slug === "atlassian" ? (
                  <div className="mt-2">
                    <AtlassianDomainNote
                      body={t(($) => $.connectors.atlassian_domain)}
                      copyLabel={t(($) => $.connectors.copy)}
                      copiedLabel={t(($) => $.connectors.copied)}
                      docsLabel={t(($) => $.connectors.atlassian_docs)}
                    />
                  </div>
                ) : null}
              </div>
            );
          })}
        </div>
      )}
    </SettingsTab>
  );
}

async function toggleApp(
  workspaceId: string,
  saved: ConnectorApp,
  enabled: boolean,
  onChanged: () => Promise<void>,
) {
  await api.updateConnectorApp(workspaceId, saved.id, { enabled });
  await onChanged();
}

function AppDetail({
  spec,
  saved,
  workspaceId,
  callbackUrl,
  onChanged,
}: {
  spec: SettingsConnectorSpec;
  saved?: ConnectorApp;
  workspaceId: string;
  callbackUrl: string;
  onChanged: () => Promise<void>;
}) {
  const { t } = useT("settings");
  const [clientId, setClientId] = useState(saved?.client_id || spec.known_client_id || "");
  const [clientSecret, setClientSecret] = useState("");
  const [appId, setAppId] = useState(saved?.app_identifier ?? "");
  const [appSlug, setAppSlug] = useState(saved?.install_slug ?? "");
  const [privateKey, setPrivateKey] = useState("");
  const [optionalSecret, setOptionalSecret] = useState("");
  const [saving, setSaving] = useState(false);
  const [copied, setCopied] = useState(false);
  const [showAccount, setShowAccount] = useState(false);
  const [scopeKind, setScopeKind] = useState("workspace");
  const [scopeId, setScopeId] = useState("");
  const [connecting, setConnecting] = useState(false);
  const [resolveAgent, setResolveAgent] = useState("");
  const [resolveProject, setResolveProject] = useState("");
  const [resolved, setResolved] = useState<ConnectorResolveResult | null>(null);
  const scopeKindItems = [
    { value: "workspace", label: t(($) => $.connectors.scope_workspace) },
    { value: "agent", label: t(($) => $.connectors.scope_agent) },
    { value: "project", label: t(($) => $.connectors.scope_project) },
  ];

  useEffect(() => {
    setClientId(saved?.client_id || spec.known_client_id || "");
    setClientSecret("");
    setAppId(saved?.app_identifier ?? "");
    setAppSlug(saved?.install_slug ?? "");
    setPrivateKey("");
    setOptionalSecret("");
  }, [saved?.id, saved?.client_id, saved?.app_identifier, saved?.install_slug, spec.known_client_id, spec.slug]);

  const labelFor = (key: string) => {
    if ((spec.slug === "dropbox" || spec.slug === "box") && key === "client_id") return t(($) => $.connectors.field_app_key);
    if ((spec.slug === "dropbox" || spec.slug === "box") && key === "client_secret") return t(($) => $.connectors.field_app_secret);
    switch (key) {
      case "app_id": return t(($) => $.connectors.field_app_id);
      case "app_slug": return t(($) => $.connectors.field_app_slug);
      case "client_id": return t(($) => $.connectors.client_id);
      case "client_secret": return t(($) => $.connectors.client_secret);
      case "private_key": return t(($) => $.connectors.field_private_key);
      case "webhook_secret": return t(($) => $.connectors.field_webhook);
      case "signing_secret": return t(($) => $.connectors.field_signing);
      default: return key;
    }
  };

  const where = () => {
    switch (spec.slug) {
      case "github": return t(($) => $.connectors.where_github);
      case "slack": return t(($) => $.connectors.where_slack);
      case "asana": return t(($) => $.connectors.where_asana);
      case "google": return t(($) => $.connectors.where_google);
      case "dropbox": return t(($) => $.connectors.where_dropbox);
      case "box": return t(($) => $.connectors.where_box);
      default: return "";
    }
  };

  const valueOf = (key: string) => {
    switch (key) {
      case "app_id": return appId;
      case "app_slug": return appSlug;
      case "client_id": return clientId;
      case "client_secret": return clientSecret;
      case "webhook_secret":
      case "signing_secret": return optionalSecret;
      default: return "";
    }
  };

  const setValue = (key: string, value: string) => {
    switch (key) {
      case "app_id": setAppId(value); break;
      case "app_slug": setAppSlug(value); break;
      case "client_id": setClientId(value); break;
      case "client_secret": setClientSecret(value); break;
      case "webhook_secret":
      case "signing_secret": setOptionalSecret(value); break;
      default: break;
    }
  };

  const save = async () => {
    if (!clientId.trim() || (!clientSecret && !saved?.client_secret_set)) {
      toast.error(t(($) => $.connectors.secret_missing));
      return;
    }
    const needsKey = spec.fields.some((field) => field.key === "private_key" && !field.optional);
    if (needsKey && !privateKey && !saved?.private_key_set) {
      toast.error(t(($) => $.connectors.field_private_key));
      return;
    }
    setSaving(true);
    try {
      const body = {
        provider: spec.slug,
        display_name: spec.name,
        client_id: clientId.trim(),
        app_identifier: appId.trim(),
        install_slug: appSlug.trim(),
        callback_mode: "production_forward" as const,
        ...(clientSecret ? { client_secret: clientSecret } : {}),
        ...(privateKey ? { private_key: privateKey } : {}),
        ...(optionalSecret ? { optional_secret: optionalSecret } : {}),
      };
      if (saved) await api.updateConnectorApp(workspaceId, saved.id, body);
      else await api.createConnectorApp(workspaceId, body);
      setClientSecret("");
      setPrivateKey("");
      setOptionalSecret("");
      toast.success(t(($) => $.connectors.saved));
      await onChanged();
    } catch (error) {
      toast.error(errorText(error, t(($) => $.connectors.load_failed)));
    } finally {
      setSaving(false);
    }
  };

  const copyCallback = async () => {
    try {
      await navigator.clipboard.writeText(callbackUrl);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch (error) {
      toast.error(errorText(error, t(($) => $.connectors.load_failed)));
    }
  };

  const connectAccount = async () => {
    if (!saved) {
      toast.error(t(($) => $.connectors.secret_missing));
      return;
    }
    if (!spec.oauth_connect) {
      toast.error(t(($) => $.connectors.no_connect));
      return;
    }
    const binding = { scope_kind: scopeKind, scope_id: scopeKind === "workspace" ? "" : scopeId.trim() };
    if (scopeKind !== "workspace" && !binding.scope_id) {
      toast.error(t(($) => $.connectors.scope_id));
      return;
    }
    setConnecting(true);
    try {
      const created = await api.createConnectorInstance(workspaceId, saved.id, { label: t(($) => $.connectors.connect_label) });
      await api.replaceConnectorBindings(workspaceId, saved.id, created.id, [binding]);
      let connectorId = spec.connector_id ?? "";
      if (!connectorId) {
        const added = await api.addCatalogConnector(workspaceId, spec.slug);
        connectorId = added?.id ?? "";
      }
      const url = connectorId
        ? await api.startInternalConnectorOAuth(workspaceId, connectorId, window.location.href)
        : "";
      if (!url) {
        toast.error(t(($) => $.connectors.no_connect));
        await onChanged();
        return;
      }
      window.location.assign(url);
    } catch (error) {
      toast.error(errorText(error, t(($) => $.connectors.load_failed)));
      setConnecting(false);
    }
  };

  const runResolve = async () => {
    try {
      setResolved(await api.resolveConnectorApp(workspaceId, {
        provider: spec.slug,
        agent_id: resolveAgent.trim(),
        project_id: resolveProject.trim(),
      }));
    } catch (error) {
      toast.error(errorText(error, t(($) => $.connectors.load_failed)));
    }
  };

  return (
    <SettingsCard className="mt-2">
      <div className="space-y-4 p-4">
        <div className="flex flex-wrap items-center gap-2">
          <p className="text-body">{t(($) => $.connectors.callback_instruction)}</p>
          <code className="rounded bg-muted px-2 py-1 text-caption break-all">{callbackUrl}</code>
          <Button type="button" size="sm" variant="outline" onClick={() => void copyCallback()}>
            {copied ? <Check className="size-4" /> : <Copy className="size-4" />}
            {copied ? t(($) => $.connectors.copied) : t(($) => $.connectors.copy)}
          </Button>
        </div>
        <p className="text-caption text-muted-foreground">{t(($) => $.connectors.preset_note)}</p>
        {spec.authorization_endpoint ? <Preset label={t(($) => $.connectors.authorization_endpoint)} value={spec.authorization_endpoint} /> : null}
        {spec.token_endpoint ? <Preset label={t(($) => $.connectors.token_endpoint)} value={spec.token_endpoint} /> : null}
        {spec.scopes ? <Preset label={t(($) => $.connectors.scopes)} value={spec.scopes} /> : null}
        {spec.mcp_url ? <Preset label="MCP" value={spec.mcp_url} /> : null}
        {spec.slug === "asana" ? <p className="text-caption text-muted-foreground">{t(($) => $.connectors.asana_note)}</p> : null}
        {spec.known_client_id ? (
          <p className="text-caption text-muted-foreground">{t(($) => $.connectors.known_client)}：{spec.known_client_id}</p>
        ) : null}
        {!saved && spec.env_configured ? <p className="text-caption text-muted-foreground">{t(($) => $.connectors.env_migrate)}</p> : null}
        <div className="space-y-3">
          <p className="text-caption text-muted-foreground">
            {t(($) => $.connectors.where)}
            {spec.docs_url ? <> · <a className="underline" href={spec.docs_url} target="_blank" rel="noreferrer">{spec.docs_url.replace(/^https:\/\//, "")}</a></> : null}
          </p>
          <p className="text-body">{where()}</p>
          {spec.fields.map((field) => field.file ? (
            <label key={field.key} className="block space-y-1 text-caption text-muted-foreground">
              <span>{labelFor(field.key)}</span>
              <Input
                type="file"
                accept=".pem,application/x-pem-file,text/plain"
                onChange={(event) => {
                  const file = event.target.files?.[0];
                  if (!file) return;
                  void file.text().then(setPrivateKey);
                }}
              />
              {saved?.private_key_set && !privateKey ? <span className="block">{t(($) => $.connectors.uploaded)}</span> : null}
              {privateKey ? <span className="block">{t(($) => $.connectors.uploaded)}</span> : null}
            </label>
          ) : (
            <label key={field.key} className="block space-y-1 text-caption text-muted-foreground">
              <span>{labelFor(field.key)}</span>
              <Input
                value={valueOf(field.key)}
                type={field.key.includes("secret") ? "password" : "text"}
                autoComplete={field.key.includes("secret") ? "new-password" : "off"}
                placeholder={field.key === "client_secret" && saved?.client_secret_hint ? saved.client_secret_hint : field.key === "signing_secret" || field.key === "webhook_secret" ? (saved?.optional_secret_hint || "") : ""}
                onChange={(event) => setValue(field.key, event.target.value)}
              />
              {field.key === "client_secret" && !clientSecret ? <span className="block">{t(($) => $.connectors.secret_kept)}</span> : null}
            </label>
          ))}
        </div>
        <Button type="button" onClick={() => void save()} disabled={saving}>{t(($) => $.connectors.save)}</Button>

        <div className="space-y-3 border-t border-surface-border pt-4">
          <h4 className="text-body font-semibold">{t(($) => $.connectors.accounts)}</h4>
          {(saved?.instances ?? []).map((instance) => saved ? (
            <div key={instance.id} className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-surface-border px-3 py-2">
              <div>
                <p className="text-body">{instance.label}{instance.external_login ? ` · ${instance.external_login}` : ""}</p>
                <p className="text-caption text-muted-foreground">
                  {instance.bindings.map((binding) => binding.scope_kind === "workspace" ? binding.scope_kind : `${binding.scope_kind}:${binding.scope_id}`).join(", ") || t(($) => $.connectors.token_missing)}
                  {instance.token_set ? ` · ${instance.token_hint || t(($) => $.connectors.token_set)}` : ` · ${t(($) => $.connectors.token_missing)}`}
                </p>
              </div>
              <Button
                type="button"
                size="sm"
                variant="ghost"
                onClick={() => void api.deleteConnectorInstance(workspaceId, saved.id, instance.id).then(onChanged).catch((error: unknown) => {
                  toast.error(errorText(error, t(($) => $.connectors.load_failed)));
                })}
              >
                {t(($) => $.connectors.remove_instance)}
              </Button>
            </div>
          ) : null)}
          {spec.oauth_connect ? (
            <>
              <Button type="button" size="sm" variant="outline" onClick={() => setShowAccount((open) => !open)}>
                + {t(($) => $.connectors.add_account)}
              </Button>
              {showAccount ? (
                <div className="grid gap-3 sm:grid-cols-2">
                  <label className="space-y-1 text-caption text-muted-foreground">
                    <span>{t(($) => $.connectors.where_used)}</span>
                    <Select items={scopeKindItems} value={scopeKind} onValueChange={(value) => { if (value) setScopeKind(value); }}>
                      <SelectTrigger><SelectValue /></SelectTrigger>
                      <SelectContent>
                        <SelectItem value="workspace">{t(($) => $.connectors.scope_workspace)}</SelectItem>
                        <SelectItem value="agent">{t(($) => $.connectors.scope_agent)}</SelectItem>
                        <SelectItem value="project">{t(($) => $.connectors.scope_project)}</SelectItem>
                      </SelectContent>
                    </Select>
                  </label>
                  {scopeKind === "workspace" ? null : (
                    <label className="space-y-1 text-caption text-muted-foreground">
                      <span>{t(($) => $.connectors.scope_id)}</span>
                      <Input value={scopeId} onChange={(event) => setScopeId(event.target.value)} />
                    </label>
                  )}
                  <div>
                    <Button type="button" size="sm" onClick={() => void connectAccount()} disabled={connecting || !saved}>
                      {t(($) => $.connectors.connect)}
                    </Button>
                  </div>
                </div>
              ) : null}
            </>
          ) : (
            <p className="text-caption text-muted-foreground">{t(($) => $.connectors.no_connect)}</p>
          )}
        </div>

        <div className="space-y-3 border-t border-surface-border pt-4">
          <h4 className="text-body font-semibold">{t(($) => $.connectors.test_title)}</h4>
          <div className="grid gap-3 sm:grid-cols-2">
            <label className="space-y-1 text-caption text-muted-foreground">
              <span>{t(($) => $.connectors.agent)}</span>
              <Input value={resolveAgent} onChange={(event) => setResolveAgent(event.target.value)} />
            </label>
            <label className="space-y-1 text-caption text-muted-foreground">
              <span>{t(($) => $.connectors.project)}</span>
              <Input value={resolveProject} onChange={(event) => setResolveProject(event.target.value)} />
            </label>
          </div>
          <Button type="button" size="sm" variant="outline" onClick={() => void runResolve()}>{t(($) => $.connectors.test_title)}</Button>
          {resolved ? (
            <p className="text-body">
              {resolved.matched
                ? `${t(($) => $.connectors.matched)} ${resolved.instance_label ?? ""} · ${resolved.scope_kind ?? ""}${resolved.scope_id ? ` ${resolved.scope_id}` : ""}`
                : t(($) => $.connectors.unmatched)}
            </p>
          ) : null}
        </div>
      </div>
    </SettingsCard>
  );
}

function Preset({ label, value }: { label: string; value: string }) {
  return (
    <p className="text-caption text-muted-foreground">
      <span className="font-medium text-foreground">{label}</span> {value}
    </p>
  );
}
