"use client";

import { useCallback, useEffect, useState } from "react";
import { api } from "@multica/core/api";
import type { ConnectorApp, ConnectorBinding, ConnectorResolveResult } from "@multica/core/api";
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
import { useT } from "../../i18n";
import { SettingsCard, SettingsSection, SettingsTab } from "./settings-layout";

function errorText(error: unknown, fallback: string) {
  return error instanceof Error && error.message ? error.message : fallback;
}

export function ConnectorsTab() {
  const { t } = useT("settings");
  const workspaceId = useCurrentWorkspace()?.id ?? "";
  const [apps, setApps] = useState<ConnectorApp[]>([]);
  const [priority, setPriority] = useState<string[]>(["agent", "project", "environment", "workspace"]);
  const [loading, setLoading] = useState(true);
  const [provider, setProvider] = useState("github");
  const [displayName, setDisplayName] = useState("");
  const [clientId, setClientId] = useState("");
  const [clientSecret, setClientSecret] = useState("");
  const [scopes, setScopes] = useState("");
  const [authorizationEndpoint, setAuthorizationEndpoint] = useState("");
  const [tokenEndpoint, setTokenEndpoint] = useState("");
  const [callbackMode, setCallbackMode] = useState("production_forward");
  const [creating, setCreating] = useState(false);
  const [pendingDelete, setPendingDelete] = useState<string | null>(null);
  const [resolveProvider, setResolveProvider] = useState("github");
  const [resolveAgent, setResolveAgent] = useState("");
  const [resolveProject, setResolveProject] = useState("");
  const [resolveEnvironment, setResolveEnvironment] = useState("");
  const [resolved, setResolved] = useState<ConnectorResolveResult | null>(null);
  const callbackModeItems = [
    { value: "production_forward", label: t(($) => $.connectors.callback_forward) },
    { value: "self", label: t(($) => $.connectors.callback_self) },
  ];

  const load = useCallback(async () => {
    if (!workspaceId) return;
    try {
      const page = await api.listConnectorApps(workspaceId);
      setApps(page.apps ?? []);
      if (page.priority?.length) setPriority(page.priority);
    } catch (error) {
      toast.error(errorText(error, t(($) => $.connectors.load_failed)));
    } finally {
      setLoading(false);
    }
  }, [t, workspaceId]);

  useEffect(() => { void load(); }, [load]);

  const createApp = async () => {
    if (!workspaceId) return;
    setCreating(true);
    try {
      await api.createConnectorApp(workspaceId, {
        provider: provider.trim(),
        display_name: displayName.trim(),
        client_id: clientId.trim(),
        client_secret: clientSecret,
        scopes: scopes.trim(),
        authorization_endpoint: authorizationEndpoint.trim(),
        token_endpoint: tokenEndpoint.trim(),
        callback_mode: callbackMode,
      });
      setClientSecret("");
      toast.success(t(($) => $.connectors.saved));
      await load();
    } catch (error) {
      toast.error(errorText(error, t(($) => $.connectors.load_failed)));
    } finally {
      setCreating(false);
    }
  };

  const removeApp = async (appId: string) => {
    if (pendingDelete !== appId) {
      setPendingDelete(appId);
      return;
    }
    try {
      await api.deleteConnectorApp(workspaceId, appId);
      setPendingDelete(null);
      await load();
    } catch (error) {
      toast.error(errorText(error, t(($) => $.connectors.load_failed)));
    }
  };

  const runResolve = async () => {
    if (!workspaceId) return;
    try {
      setResolved(await api.resolveConnectorApp(workspaceId, {
        provider: resolveProvider.trim(),
        agent_id: resolveAgent.trim(),
        project_id: resolveProject.trim(),
        environment: resolveEnvironment.trim(),
      }));
    } catch (error) {
      toast.error(errorText(error, t(($) => $.connectors.load_failed)));
    }
  };

  return (
    <SettingsTab title={t(($) => $.connectors.title)} description={t(($) => $.connectors.description)}>
      <SettingsSection title={t(($) => $.connectors.priority)} description={priority.join(" → ")}>
        <SettingsCard>
          <div className="grid gap-3 p-4 sm:grid-cols-2">
            <Field label={t(($) => $.connectors.provider)} value={provider} onChange={setProvider} />
            <Field label={t(($) => $.connectors.display_name)} value={displayName} onChange={setDisplayName} />
            <Field label={t(($) => $.connectors.client_id)} value={clientId} onChange={setClientId} />
            <Field label={t(($) => $.connectors.client_secret)} value={clientSecret} onChange={setClientSecret} secret />
            <Field label={t(($) => $.connectors.scopes)} value={scopes} onChange={setScopes} />
            <Field label={t(($) => $.connectors.authorization_endpoint)} value={authorizationEndpoint} onChange={setAuthorizationEndpoint} />
            <Field label={t(($) => $.connectors.token_endpoint)} value={tokenEndpoint} onChange={setTokenEndpoint} />
            <label className="space-y-1 text-caption text-muted-foreground">
              <span>{t(($) => $.connectors.callback_mode)}</span>
              <Select
                items={callbackModeItems}
                value={callbackMode}
                onValueChange={(value) => { if (value) setCallbackMode(value); }}
              >
                <SelectTrigger><SelectValue /></SelectTrigger>
                <SelectContent>
                  <SelectItem value="production_forward">{t(($) => $.connectors.callback_forward)}</SelectItem>
                  <SelectItem value="self">{t(($) => $.connectors.callback_self)}</SelectItem>
                </SelectContent>
              </Select>
            </label>
          </div>
          <div className="px-4 pb-4">
            <Button type="button" onClick={() => void createApp()} disabled={creating || !clientId.trim() || !clientSecret}>
              {t(($) => $.connectors.create)}
            </Button>
          </div>
        </SettingsCard>
      </SettingsSection>

      <SettingsSection title={t(($) => $.connectors.title)}>
        {loading ? null : apps.length === 0 ? (
          <p className="text-body text-muted-foreground">{t(($) => $.connectors.empty)}</p>
        ) : (
          <div className="space-y-4">
            {apps.map((app) => (
              <AppCard
                key={app.id}
                app={app}
                workspaceId={workspaceId}
                pendingDelete={pendingDelete === app.id}
                onChanged={load}
                onDelete={() => void removeApp(app.id)}
              />
            ))}
          </div>
        )}
      </SettingsSection>

      <SettingsSection title={t(($) => $.connectors.resolve)}>
        <SettingsCard>
          <div className="grid gap-3 p-4 sm:grid-cols-2">
            <Field label={t(($) => $.connectors.provider)} value={resolveProvider} onChange={setResolveProvider} />
            <Field label={t(($) => $.connectors.agent)} value={resolveAgent} onChange={setResolveAgent} />
            <Field label={t(($) => $.connectors.project)} value={resolveProject} onChange={setResolveProject} />
            <Field label={t(($) => $.connectors.environment)} value={resolveEnvironment} onChange={setResolveEnvironment} />
          </div>
          <div className="space-y-2 px-4 pb-4">
            <Button type="button" variant="outline" onClick={() => void runResolve()}>{t(($) => $.connectors.resolve)}</Button>
            {resolved ? (
              <p className="text-body">
                {resolved.matched
                  ? `${t(($) => $.connectors.matched)} ${resolved.instance_label ?? ""} · ${resolved.scope_kind ?? ""}${resolved.scope_id ? ` ${resolved.scope_id}` : ""} · ${resolved.token_ready ? t(($) => $.connectors.token_set) : t(($) => $.connectors.token_missing)}`
                  : t(($) => $.connectors.unmatched)}
              </p>
            ) : null}
          </div>
        </SettingsCard>
      </SettingsSection>
    </SettingsTab>
  );
}

function AppCard({
  app,
  workspaceId,
  pendingDelete,
  onChanged,
  onDelete,
}: {
  app: ConnectorApp;
  workspaceId: string;
  pendingDelete: boolean;
  onChanged: () => Promise<void>;
  onDelete: () => void;
}) {
  const { t } = useT("settings");
  const scopeKindItems = [
    { value: "agent", label: t(($) => $.connectors.scope_agent) },
    { value: "project", label: t(($) => $.connectors.scope_project) },
    { value: "environment", label: t(($) => $.connectors.scope_environment) },
    { value: "workspace", label: t(($) => $.connectors.scope_workspace) },
  ];
  const [secret, setSecret] = useState("");
  const [label, setLabel] = useState("");
  const [login, setLogin] = useState("");
  const [token, setToken] = useState("");
  const [scopeKind, setScopeKind] = useState<string>("workspace");
  const [scopeId, setScopeId] = useState("");

  const rotateSecret = async () => {
    if (!secret) return;
    try {
      await api.updateConnectorApp(workspaceId, app.id, { client_secret: secret });
      setSecret("");
      toast.success(t(($) => $.connectors.saved));
      await onChanged();
    } catch (error) {
      toast.error(errorText(error, t(($) => $.connectors.load_failed)));
    }
  };

  const toggle = async () => {
    try {
      await api.updateConnectorApp(workspaceId, app.id, { enabled: !app.enabled });
      await onChanged();
    } catch (error) {
      toast.error(errorText(error, t(($) => $.connectors.load_failed)));
    }
  };

  const addInstance = async () => {
    if (!label.trim()) return;
    try {
      const created = await api.createConnectorInstance(workspaceId, app.id, {
        label: label.trim(),
        external_login: login.trim(),
        token,
      });
      const binding: ConnectorBinding = {
        scope_kind: scopeKind,
        scope_id: scopeKind === "workspace" ? "" : scopeId.trim(),
      };
      if (scopeKind === "workspace" || binding.scope_id) {
        await api.replaceConnectorBindings(workspaceId, app.id, created.id, [binding]);
      }
      setLabel("");
      setLogin("");
      setToken("");
      setScopeId("");
      toast.success(t(($) => $.connectors.saved));
      await onChanged();
    } catch (error) {
      toast.error(errorText(error, t(($) => $.connectors.load_failed)));
    }
  };

  return (
    <SettingsCard>
      <div className="space-y-2 p-4">
        <div className="flex flex-wrap items-start justify-between gap-2">
          <div>
            <h3 className="text-body font-semibold">{app.display_name || app.provider}</h3>
            <p className="text-caption text-muted-foreground">
              {app.provider} · {app.client_id}
              {app.client_secret_set ? ` · ${app.client_secret_hint || t(($) => $.connectors.secret_set)}` : ""}
              {app.active_for_catalog ? ` · ${t(($) => $.connectors.active)}` : ""}
              {app.enabled ? "" : ` · ${t(($) => $.connectors.disabled)}`}
            </p>
            <p className="text-caption text-muted-foreground">
              {app.callback_mode === "self" ? t(($) => $.connectors.callback_self) : t(($) => $.connectors.callback_forward)}
            </p>
          </div>
          <div className="flex gap-2">
            <Button type="button" variant="outline" size="sm" onClick={() => void toggle()}>
              {app.enabled ? t(($) => $.connectors.disabled) : t(($) => $.connectors.enabled)}
            </Button>
            <Button type="button" variant="outline" size="sm" onClick={onDelete}>
              {pendingDelete ? t(($) => $.connectors.confirm_delete) : t(($) => $.connectors.delete)}
            </Button>
          </div>
        </div>
        <div className="flex flex-wrap items-end gap-2">
          <Field label={t(($) => $.connectors.client_secret)} value={secret} onChange={setSecret} secret hint={t(($) => $.connectors.secret_kept)} />
          <Button type="button" variant="outline" size="sm" onClick={() => void rotateSecret()} disabled={!secret}>{t(($) => $.connectors.save)}</Button>
        </div>
        <div className="space-y-2">
          <h4 className="text-caption font-medium">{t(($) => $.connectors.instances)}</h4>
          {app.instances.map((instance) => (
            <InstanceRow key={instance.id} appId={app.id} workspaceId={workspaceId} instance={instance} onChanged={onChanged} />
          ))}
          <div className="grid gap-2 sm:grid-cols-2">
            <Field label={t(($) => $.connectors.label)} value={label} onChange={setLabel} />
            <Field label={t(($) => $.connectors.login)} value={login} onChange={setLogin} />
            <Field label={t(($) => $.connectors.token)} value={token} onChange={setToken} secret />
            <label className="space-y-1 text-caption text-muted-foreground">
              <span>{t(($) => $.connectors.scope_kind)}</span>
              <Select
                items={scopeKindItems}
                value={scopeKind}
                onValueChange={(value) => { if (value) setScopeKind(value); }}
              >
                <SelectTrigger><SelectValue /></SelectTrigger>
                <SelectContent>
                  <SelectItem value="agent">{t(($) => $.connectors.scope_agent)}</SelectItem>
                  <SelectItem value="project">{t(($) => $.connectors.scope_project)}</SelectItem>
                  <SelectItem value="environment">{t(($) => $.connectors.scope_environment)}</SelectItem>
                  <SelectItem value="workspace">{t(($) => $.connectors.scope_workspace)}</SelectItem>
                </SelectContent>
              </Select>
            </label>
            {scopeKind === "workspace" ? null : (
              <Field label={t(($) => $.connectors.scope_id)} value={scopeId} onChange={setScopeId} />
            )}
          </div>
          <Button type="button" variant="outline" size="sm" onClick={() => void addInstance()}>{t(($) => $.connectors.add_instance)}</Button>
        </div>
      </div>
    </SettingsCard>
  );
}

function InstanceRow({
  appId,
  workspaceId,
  instance,
  onChanged,
}: {
  appId: string;
  workspaceId: string;
  instance: ConnectorApp["instances"][number];
  onChanged: () => Promise<void>;
}) {
  const { t } = useT("settings");
  const bindingText = instance.bindings.map((binding) => (
    binding.scope_kind === "workspace" ? binding.scope_kind : `${binding.scope_kind}:${binding.scope_id}`
  )).join(", ");

  const clearBinding = async (index: number) => {
    const next = instance.bindings.filter((_, i) => i !== index);
    try {
      await api.replaceConnectorBindings(workspaceId, appId, instance.id, next);
      await onChanged();
    } catch (error) {
      toast.error(errorText(error, t(($) => $.connectors.load_failed)));
    }
  };

  return (
    <div className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-surface-border px-3 py-2">
      <div>
        <p className="text-body">{instance.label}{instance.external_login ? ` · ${instance.external_login}` : ""}</p>
        <p className="text-caption text-muted-foreground">
          {instance.status} · {instance.token_set ? (instance.token_hint || t(($) => $.connectors.token_set)) : t(($) => $.connectors.token_missing)}
          {bindingText ? ` · ${bindingText}` : ""}
        </p>
      </div>
      {instance.bindings.length > 0 ? (
        <Button type="button" variant="ghost" size="sm" onClick={() => void clearBinding(instance.bindings.length - 1)}>
          {t(($) => $.connectors.bindings)}
        </Button>
      ) : null}
    </div>
  );
}

function Field({
  label,
  value,
  onChange,
  secret,
  hint,
}: {
  label: string;
  value: string;
  onChange: (value: string) => void;
  secret?: boolean;
  hint?: string;
}) {
  return (
    <label className="space-y-1 text-caption text-muted-foreground">
      <span>{label}</span>
      <Input
        value={value}
        type={secret ? "password" : "text"}
        autoComplete={secret ? "new-password" : "off"}
        onChange={(event) => onChange(event.target.value)}
      />
      {hint ? <span className="block">{hint}</span> : null}
    </label>
  );
}
