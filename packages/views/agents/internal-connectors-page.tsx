"use client";

import { useEffect, useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api, errorCode, type InternalConnector, type InternalConnectorInput, type InternalConnectorTest } from "@multica/core/api";
import { useAuthStore } from "@multica/core/auth";
import { useConfigStore } from "@multica/core/config";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import {
  connectorCatalogOptions,
  internalConnectorKeys,
  internalConnectorListOptions,
  internalConnectorUpdateInput,
  useAddCatalogConnector,
  useRefreshInternalConnectorTools,
  useSetInternalConnectorCredential,
  useSetInternalConnectorWriteEnabled,
  useStartInternalConnectorOAuth,
  type ConnectorCatalogApp,
  type InternalConnectorToolsRefresh,
} from "@multica/core/internal-connectors";
import { agentListOptions, memberListOptions } from "@multica/core/workspace/queries";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Switch } from "@multica/ui/components/ui/switch";
import { AlertCircle, CheckCircle2, ExternalLink, Loader2, Plus, Power } from "lucide-react";
import { ConnectorLogo, connectorBrandName } from "../common/connector-logo";
import { openExternal } from "../platform/open-external";
import { isDesktopShell } from "../platform/local-directory";
import { useT } from "../i18n";

const empty: InternalConnectorInput = {name:"",upstream_url:"",allowed_tools:[],agent_ids:[],enabled:false,auth_mode:"none"};

/** Bearer rules shared with the server: 1..4096 chars, no CR/LF/NUL. */
const MAX_TOKEN_LENGTH = 4096;

type ConnectReturn = { kind: "connected"; slug: string } | { kind: "error"; code: string };

/**
 * Reads the outcome of a provider sign-in the server redirected back with
 * (`?connected=<slug>` or `?connect_error=<code>`) and removes those
 * parameters from the address bar so a reload does not repeat the banner.
 */
function takeConnectReturn(): ConnectReturn | null {
  if (typeof window === "undefined") return null;
  const url = new URL(window.location.href);
  const connected = url.searchParams.get("connected");
  const error = url.searchParams.get("connect_error");
  if (connected === null && error === null) return null;
  url.searchParams.delete("connected");
  url.searchParams.delete("connect_error");
  window.history.replaceState(window.history.state, "", `${url.pathname}${url.search}${url.hash}`);
  if (error !== null) return { kind: "error", code: error };
  return { kind: "connected", slug: connected ?? "" };
}

function errorMessage(error: unknown, fallback: string): string {
  return error instanceof Error && error.message ? error.message : fallback;
}

export function InternalConnectorsPage() {
  const {t}=useT("agents");
  const workspaceId=useWorkspaceId();
  const queryClient=useQueryClient();
  const userId=useAuthStore((s)=>s.user?.id);
  const members=useQuery(memberListOptions(workspaceId));
  const agents=useQuery(agentListOptions(workspaceId));
  const canManage=members.data?.some((member)=>member.user_id===userId && (member.role==="owner" || member.role==="admin"))===true;
  const key=internalConnectorKeys.all(workspaceId);
  const list=useQuery({...internalConnectorListOptions(workspaceId),enabled:!!workspaceId&&canManage,refetchOnWindowFocus:true});
  const catalog=useQuery({...connectorCatalogOptions(workspaceId),enabled:!!workspaceId&&canManage,refetchOnWindowFocus:true});
  const [connectReturn,setConnectReturn]=useState<ConnectReturn|null>(null);
  useEffect(()=>{
    // Once per mount; a StrictMode re-run finds the parameters already gone.
    const result=takeConnectReturn();
    if (result) setConnectReturn(result);
  },[]);
  const [editing,setEditing]=useState<string|null>(null);
  const [form,setForm]=useState<InternalConnectorInput>(empty);
  const [bearerInput,setBearerInput]=useState("");
  const [rotateCredential,setRotateCredential]=useState(false);
  const [error,setError]=useState("");
  const [busy,setBusy]=useState(false);
  const [credentialToken,setCredentialToken]=useState("");
  const [credentialBusy,setCredentialBusy]=useState(false);
  const [credentialError,setCredentialError]=useState("");
  const [testing,setTesting]=useState<string|null>(null);
  const [toggling,setToggling]=useState<string|null>(null);
  const [toggleErrors,setToggleErrors]=useState<Record<string,string>>({});
  const [testResults,setTestResults]=useState<Record<string,InternalConnectorTest>>({});
  const selectedConnector=editing!=="new"?list.data?.find((c)=>c.id===editing):undefined;
  const credentialReady=selectedConnector?.credentialReady===true;
  const credentialOptional=selectedConnector?.credentialOptional===true;
  const capabilityLink=form.upstream_url.includes("/api/mcp/connect/");

  function edit(connectorId?:string) {
    const c=list.data?.find((item)=>item.id===connectorId);
    setEditing(c?.id??"new");
    setForm(c?{name:c.name,upstream_url:c.upstreamUrl,allowed_tools:c.allowedTools,agent_ids:c.agentIds,enabled:c.enabled,auth_mode:c.authMode==="unknown"?"":c.authMode}:empty);
    setError("");
    setBearerInput("");setRotateCredential(false);
    setCredentialToken("");setCredentialError("");
  }
  async function save() {
    if (!canManage || !editing) return;
    setBusy(true);setError("");
    try {
      if(editing==="new") {
        await api.createInternalConnector(workspaceId,{...form,auth_mode:capabilityLink?"":form.auth_mode,bearer_token:capabilityLink?"":bearerInput,allowed_tools:[],auto_discover:true,enabled:false});
      } else {
        const data:InternalConnectorInput=selectedConnector
          ? internalConnectorUpdateInput(selectedConnector,{name:form.name,agent_ids:form.agent_ids,enabled:form.enabled})
          : {...form,auto_discover:false};
        await api.updateInternalConnector(workspaceId,editing,data);
      }
      await queryClient.invalidateQueries({queryKey:key});
      setEditing(null);setForm(empty);setBearerInput("");setCredentialToken("");
    } catch(e) {setError(e instanceof Error?e.message:t(($)=>$.internal_mcp.save_failed));}
    finally {setBusy(false)}
  }
  async function saveCredential() {
    if(!editing || editing==="new" || !credentialToken) return;
    setCredentialBusy(true);setCredentialError("");
    try {
      await api.setInternalConnectorCredential(workspaceId,editing,credentialToken);
      setCredentialToken("");setRotateCredential(false);
      await queryClient.invalidateQueries({queryKey:key});
    } catch(e) {setCredentialError(e instanceof Error?e.message:t(($)=>$.internal_mcp.credential_failed));}
    finally {setCredentialBusy(false)}
  }
  async function testConnection(id:string) {
    setTesting(id);
    try {const result=await api.testInternalConnector(workspaceId,id);setTestResults((old)=>({...old,[id]:result}));}
    catch {setTestResults((old)=>({...old,[id]:{reachable:false,ready:false,missing_tools:[],message:t(($)=>$.internal_mcp.test_failed)}}));}
    finally {setTesting(null)}
  }
  async function toggleConnector(connector:InternalConnector) {
    setToggling(connector.id);setToggleErrors((old)=>({...old,[connector.id]:""}));
    try {
      await api.updateInternalConnector(workspaceId,connector.id,internalConnectorUpdateInput(connector,{enabled:!connector.enabled}));
      await queryClient.invalidateQueries({queryKey:key});
    } catch(e) {setToggleErrors((old)=>({...old,[connector.id]:e instanceof Error?e.message:t(($)=>$.internal_mcp.save_failed)}));}
    finally {setToggling(null)}
  }

  // Each catalog app claims its workspace connector (by id, else by slug);
  // everything else is a custom connector. While the catalog is unavailable,
  // catalog connectors stay manageable in the custom list.
  const connectors=useMemo(()=>list.data??[],[list.data]);
  const apps=useMemo(()=>catalog.data??[],[catalog.data]);
  const appConnectors=useMemo(()=>{
    const byApp=new Map<string,InternalConnector>();
    for (const app of apps) {
      const match=connectors.find((c)=>c.id===app.connectorId)??connectors.find((c)=>c.catalogSlug===app.slug);
      if (match) byApp.set(app.slug,match);
    }
    return byApp;
  },[apps,connectors]);
  const claimedIds=useMemo(()=>new Set([...appConnectors.values()].map((c)=>c.id)),[appConnectors]);
  const present=connectors.filter((c)=>!claimedIds.has(c.id)&&(c.catalogSlug===""||!catalog.isPending));

  return <main className="mx-auto min-h-0 w-full max-w-6xl flex-1 space-y-7 overflow-y-auto px-6 py-8">
    <header className="flex flex-wrap items-end justify-between gap-4 border-b border-border pb-5">
      <div><p className="text-caption text-muted-foreground">{t(($)=>$.internal_mcp.kicker)}</p>
        <h1 className="mt-2 text-display-sm font-semibold tracking-tight">{t(($)=>$.internal_mcp.title)}</h1>
        <p className="mt-2 max-w-2xl text-body text-muted-foreground">{t(($)=>$.internal_mcp.description)}</p></div>
      {canManage && <Button onClick={()=>edit()}><Plus className="mr-2 size-4"/>{t(($)=>$.internal_mcp.create)}</Button>}
    </header>

    {connectReturn && <ConnectReturnBanner result={connectReturn} apps={apps}/>}

    {!members.isLoading && !canManage && <p className="rounded-xl border border-border bg-muted/40 p-5 text-body text-muted-foreground">{t(($)=>$.internal_mcp.admin_only)}</p>}

    {canManage && <OfficialAppsSection
      workspaceId={workspaceId}
      apps={apps}
      appConnectors={appConnectors}
      loading={catalog.isPending}
      failed={catalog.isError}
      onManage={(id)=>edit(id)}
    />}

    {canManage && <section className="space-y-3">
      <h2 className="text-title font-semibold">{t(($)=>$.internal_mcp.managed)}</h2>
      {list.isError && <p role="alert" className="text-body text-destructive">{t(($)=>$.internal_mcp.load_failed)}</p>}
      {list.isLoading && <p className="text-body text-muted-foreground">{t(($)=>$.internal_mcp.loading)}</p>}
      {!list.isLoading && !list.isError && present.length===0 && <div className="rounded-xl border border-dashed border-border p-6"><p className="text-body text-muted-foreground">{t(($)=>$.internal_mcp.empty)}</p><Button className="mt-4" variant="outline" onClick={()=>edit()}><Plus className="mr-2 size-4"/>{t(($)=>$.internal_mcp.create)}</Button></div>}
      <div className="grid gap-3 md:grid-cols-2">{present.map((c)=><article key={c.id} className="space-y-3 rounded-xl border border-border bg-card p-5">
        <div className="flex flex-wrap items-start justify-between gap-3"><div className="min-w-0 flex-1"><h3 className="text-title-sm font-semibold">{c.name}</h3><p className="mt-1 break-all text-caption text-muted-foreground">{c.upstreamUrl}</p></div><div className="flex flex-wrap gap-2">{!c.credentialReady&&c.credentialOptional&&<Badge variant="outline">{t(($)=>$.internal_mcp.scoped_credentials)}</Badge>}<Badge variant={c.enabled&&(c.credentialReady||c.credentialOptional)?"default":"secondary"}>{!c.credentialReady&&!c.credentialOptional?t(($)=>$.internal_mcp.waiting_credential):c.enabled?t(($)=>$.internal_mcp.enabled):t(($)=>$.internal_mcp.disabled)}</Badge></div></div>
        <p className="text-caption text-muted-foreground">{t(($)=>$.internal_mcp.agent_count,{count:c.agentIds.length})} · {t(($)=>$.internal_mcp.discovered_tools,{count:c.allowedTools.length})} · {c.authMode==="none"?t(($)=>$.internal_mcp.no_auth):c.credentialSource==="workspace"?t(($)=>$.internal_mcp.credential_workspace):c.credentialSource==="environment"?t(($)=>$.internal_mcp.credential_environment):t(($)=>$.internal_mcp.waiting_credential)}</p>
        <div className="flex flex-wrap gap-2"><Button variant="outline" onClick={()=>edit(c.id)}>{t(($)=>$.internal_mcp.manage)}</Button><Button variant="outline" disabled={testing===c.id} onClick={()=>testConnection(c.id)}>{testing===c.id?t(($)=>$.internal_mcp.testing):t(($)=>$.internal_mcp.test_connection)}</Button><Button variant={c.enabled?"outline":"default"} disabled={toggling===c.id||(!c.enabled&&!c.credentialReady&&!c.credentialOptional)} onClick={()=>toggleConnector(c)}><Power className="mr-2 size-4"/>{toggling===c.id?t(($)=>$.internal_mcp.saving):c.enabled?t(($)=>$.internal_mcp.disable_action):t(($)=>$.internal_mcp.enable)}</Button></div>
        {toggleErrors[c.id] && <p role="alert" className="text-caption text-destructive">{toggleErrors[c.id]}</p>}
        {testResults[c.id] && <p role="status" className={`text-caption ${testResults[c.id]?.ready?"text-foreground":testResults[c.id]?.reachable?"text-warning":"text-destructive"}`}>{testResults[c.id]?.ready?t(($)=>$.internal_mcp.test_success,{tools:testResults[c.id]?.tools?.join(", ")||"—"}):testResults[c.id]?.reachable?t(($)=>$.internal_mcp.test_missing_tools,{tools:testResults[c.id]?.missing_tools.join(", ")||"—"}):testResults[c.id]?.message||t(($)=>$.internal_mcp.test_failed)}</p>}
      </article>)}</div>
    </section>}

    {editing && <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4"><div className="flex max-h-[90vh] w-full max-w-xl flex-col overflow-hidden rounded-2xl border border-border bg-background shadow-xl"><div className="min-h-0 flex-1 space-y-4 overflow-y-auto p-6">
      <h2 className="text-title font-semibold">{editing==="new"?t(($)=>$.internal_mcp.create):t(($)=>$.internal_mcp.manage)}</h2>
      <p className="text-body text-muted-foreground">{t(($)=>$.internal_mcp.form_hint)}</p>
      <label className="block text-label font-medium">{t(($)=>$.internal_mcp.name)}<Input className="mt-1" value={form.name} onChange={(e)=>setForm({...form,name:e.target.value})}/></label>
      <label className="block text-label font-medium">{t(($)=>$.internal_mcp.upstream)}<Input className="mt-1" type={editing==="new"&&capabilityLink?"password":"url"} autoComplete="off" placeholder="https://approved.example/mcp" value={form.upstream_url} disabled={editing!=="new"} onChange={(e)=>setForm({...form,upstream_url:e.target.value})}/></label>
      {editing==="new" && <p className="text-caption text-muted-foreground">{capabilityLink?t(($)=>$.internal_mcp.capability_link_hint):t(($)=>$.internal_mcp.url_hint)}</p>}
      {editing==="new" && !capabilityLink && <fieldset className="space-y-2"><legend className="text-label font-medium">{t(($)=>$.internal_mcp.auth_label)}</legend>
        <label className="flex items-center gap-2 text-body"><input type="radio" name="connector-auth" checked={form.auth_mode==="none"} onChange={()=>setForm({...form,auth_mode:"none"})}/>{t(($)=>$.internal_mcp.no_auth)}</label>
        <label className="flex items-center gap-2 text-body"><input type="radio" name="connector-auth" checked={form.auth_mode==="bearer"} onChange={()=>setForm({...form,auth_mode:"bearer"})}/>{t(($)=>$.internal_mcp.bearer_auth)}</label>
        {form.auth_mode==="bearer" && <label className="block text-label font-medium">{t(($)=>$.internal_mcp.credential)}<Input className="mt-1" type="password" autoComplete="off" value={bearerInput} onChange={(e)=>setBearerInput(e.target.value)}/></label>}
      </fieldset>}
      {editing!=="new" && <p className="text-caption text-muted-foreground">{t(($)=>$.internal_mcp.discovered_tools,{count:form.allowed_tools.length})}</p>}
      <fieldset><legend className="text-label font-medium">{t(($)=>$.internal_mcp.agents)}</legend><div className="mt-2 max-h-40 space-y-2 overflow-y-auto rounded-lg border border-border p-3">{agents.data?.map((agent)=><label key={agent.id} className="flex items-center gap-2 text-body"><input type="checkbox" checked={form.agent_ids.includes(agent.id)} onChange={(e)=>setForm({...form,agent_ids:e.target.checked?[...form.agent_ids,agent.id]:form.agent_ids.filter((id)=>id!==agent.id)})}/>{agent.name}</label>)}</div></fieldset>
      {editing!=="new" && <div className="space-y-3 rounded-xl border border-border bg-muted/30 p-4">
        <div className="flex items-center gap-2"><Power className="size-4 text-muted-foreground"/><span className="text-label font-semibold">{t(($)=>$.internal_mcp.state_title)}</span><Badge variant={form.enabled?"default":"secondary"}>{form.enabled?t(($)=>$.internal_mcp.enabled):t(($)=>$.internal_mcp.disabled)}</Badge></div>
        <p className="text-caption text-muted-foreground">{t(($)=>$.internal_mcp.state_hint)}</p>
        <div className="grid grid-cols-2 gap-2"><Button variant={!form.enabled?"default":"outline"} onClick={()=>setForm({...form,enabled:false})}>{t(($)=>$.internal_mcp.keep_disabled)}</Button><Button variant={form.enabled?"default":"outline"} disabled={!credentialReady&&!credentialOptional&&!form.enabled} onClick={()=>setForm({...form,enabled:true})}>{t(($)=>$.internal_mcp.enable)}</Button></div>
        {!credentialReady && <p className="text-caption text-muted-foreground">{credentialOptional?t(($)=>$.internal_mcp.enable_hint_scoped):t(($)=>$.internal_mcp.enable_hint)}</p>}
      </div>}
      {editing!=="new" && selectedConnector?.authMode==="none" && <p className="rounded-lg border border-border bg-muted/40 p-3 text-caption text-muted-foreground">{t(($)=>$.internal_mcp.no_auth_hint)}</p>}
      {editing!=="new" && selectedConnector?.authMode==="bearer" && <div className="space-y-2">
        {credentialReady && !rotateCredential && <Button variant="outline" onClick={()=>setRotateCredential(true)}>{t(($)=>$.internal_mcp.rotate_credential)}</Button>}
        {(!credentialReady || rotateCredential) && <div className="space-y-2 rounded-lg border border-border p-4"><label className="block text-label font-medium">{t(($)=>$.internal_mcp.credential)}<Input className="mt-1" type="password" autoComplete="off" value={credentialToken} onChange={(e)=>setCredentialToken(e.target.value)}/></label><p className="text-caption text-muted-foreground">{t(($)=>$.internal_mcp.credential_hint)}</p><Button variant="outline" disabled={credentialBusy||!credentialToken} onClick={saveCredential}>{credentialBusy?t(($)=>$.internal_mcp.saving):t(($)=>$.internal_mcp.save_credential)}</Button>{credentialError && <p role="alert" className="text-caption text-destructive">{credentialError}</p>}</div>}
      </div>}
      {error && <p role="alert" className="text-body text-destructive">{error}</p>}
      </div><div className="flex shrink-0 justify-end gap-2 border-t border-border bg-background px-6 py-4"><Button variant="outline" onClick={()=>{setEditing(null);setForm(empty);setBearerInput("");setCredentialToken("")}}>{t(($)=>$.internal_mcp.cancel)}</Button><Button disabled={busy} onClick={save}>{busy?t(($)=>$.internal_mcp.saving):selectedConnector&&form.enabled!==selectedConnector.enabled?form.enabled?t(($)=>$.internal_mcp.save_enable):t(($)=>$.internal_mcp.save_disable):t(($)=>$.internal_mcp.save)}</Button></div>
    </div></div>}
  </main>;
}

function ConnectReturnBanner({ result, apps }: { result: ConnectReturn; apps: ConnectorCatalogApp[] }) {
  const { t } = useT("agents");
  if (result.kind === "connected") {
    const name = apps.find((app) => app.slug === result.slug)?.name || connectorBrandName(result.slug);
    return (
      <p role="status" className="flex items-start gap-2 rounded-xl border border-border bg-muted/40 px-4 py-3 text-body">
        <CheckCircle2 className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
        <span>{t(($) => $.internal_mcp.catalog.returned_connected, { name })}</span>
      </p>
    );
  }
  return (
    <p role="alert" className="flex items-start gap-2 rounded-xl border border-border bg-muted/40 px-4 py-3 text-body text-destructive">
      <AlertCircle className="mt-0.5 size-4 shrink-0" />
      <span>
        {result.code === "access_denied"
          ? t(($) => $.internal_mcp.catalog.returned_denied)
          : result.code === "browser_mismatch"
            ? t(($) => $.internal_mcp.catalog.returned_browser_mismatch)
            : t(($) => $.internal_mcp.catalog.returned_error)}
      </span>
    </p>
  );
}

function OfficialAppsSection({
  workspaceId,
  apps,
  appConnectors,
  loading,
  failed,
  onManage,
}: {
  workspaceId: string;
  apps: ConnectorCatalogApp[];
  appConnectors: Map<string, InternalConnector>;
  loading: boolean;
  failed: boolean;
  onManage: (connectorId: string) => void;
}) {
  const { t } = useT("agents");
  return (
    <section className="space-y-3" aria-labelledby="official-apps-title">
      <div className="space-y-1">
        <h2 id="official-apps-title" className="text-title font-semibold">
          {t(($) => $.internal_mcp.catalog.title)}
        </h2>
        <p className="max-w-3xl text-caption text-muted-foreground">
          {t(($) => $.internal_mcp.catalog.description)}
        </p>
      </div>
      {failed ? (
        <p role="alert" className="text-body text-destructive">{t(($) => $.internal_mcp.catalog.load_failed)}</p>
      ) : loading ? (
        <p className="text-body text-muted-foreground">{t(($) => $.internal_mcp.catalog.loading)}</p>
      ) : apps.length === 0 ? (
        <p className="text-body text-muted-foreground">{t(($) => $.internal_mcp.catalog.empty)}</p>
      ) : (
        <div className="grid gap-3 md:grid-cols-2">
          {apps.map((app) => (
            <OfficialAppCard
              key={app.slug}
              workspaceId={workspaceId}
              app={app}
              connector={appConnectors.get(app.slug) ?? null}
              onManage={onManage}
            />
          ))}
        </div>
      )}
    </section>
  );
}

function OfficialAppCard({
  workspaceId,
  app,
  connector,
  onManage,
}: {
  workspaceId: string;
  app: ConnectorCatalogApp;
  connector: InternalConnector | null;
  onManage: (connectorId: string) => void;
}) {
  const { t } = useT("agents");
  const add = useAddCatalogConnector(workspaceId);
  const startOAuth = useStartInternalConnectorOAuth(workspaceId);
  const refresh = useRefreshInternalConnectorTools(workspaceId);
  const setWrite = useSetInternalConnectorWriteEnabled(workspaceId);
  const daemonAppUrl = useConfigStore((s) => s.daemonAppUrl);
  const connectorsPath = useWorkspacePaths().internalConnectors();
  const [message, setMessage] = useState<{ tone: "status" | "alert"; text: string } | null>(null);
  const [patOpen, setPatOpen] = useState(false);
  const [redirecting, setRedirecting] = useState(false);

  // Coming Back from the provider can restore this page from the
  // back/forward cache with "redirecting" still set; clear it then.
  useEffect(() => {
    if (!redirecting) return;
    const onPageShow = (event: PageTransitionEvent) => {
      if (event.persisted) setRedirecting(false);
    };
    window.addEventListener("pageshow", onPageShow);
    return () => window.removeEventListener("pageshow", onPageShow);
  }, [redirecting]);

  const description = (() => {
    switch (app.slug) {
      case "github": return t(($) => $.internal_mcp.catalog.apps.github);
      case "notion": return t(($) => $.internal_mcp.catalog.apps.notion);
      case "linear": return t(($) => $.internal_mcp.catalog.apps.linear);
      case "atlassian": return t(($) => $.internal_mcp.catalog.apps.atlassian);
      case "sentry": return t(($) => $.internal_mcp.catalog.apps.sentry);
      case "asana": return t(($) => $.internal_mcp.catalog.apps.asana);
      case "figma": return t(($) => $.internal_mcp.catalog.apps.figma);
      case "stripe": return t(($) => $.internal_mcp.catalog.apps.stripe);
      default: return t(($) => $.internal_mcp.catalog.apps.other);
    }
  })();

  const account = connector?.credentialAccount ?? "";
  const hasSharedAccount = connector?.credentialReady === true;
  const toolCount = connector?.allowedTools.length ?? 0;
  const status = !connector
    ? t(($) => $.internal_mcp.catalog.status_not_added)
    : connector.discoveredToolCount === 0 && toolCount === 0
      ? t(($) => $.internal_mcp.catalog.status_pending)
      : [
          t(($) => $.internal_mcp.catalog.tools_count, { count: toolCount }),
          account
            ? t(($) => $.internal_mcp.catalog.connected_as, { account })
            : hasSharedAccount
              ? t(($) => $.internal_mcp.catalog.connected)
              : t(($) => $.internal_mcp.catalog.no_shared_account),
        ].join(" · ");

  async function addApp() {
    setMessage(null);
    try {
      await add.mutateAsync(app.slug);
    } catch (error) {
      setMessage({ tone: "alert", text: errorMessage(error, t(($) => $.internal_mcp.catalog.add_failed, { name: app.name })) });
    }
  }

  async function connectShared() {
    if (!connector) return;
    setMessage(null);
    if (isDesktopShell()) {
      // The start response binds the sign-in to the browser that receives
      // it, and desktop API responses land in the app's own cookie jar. So
      // desktop never starts the connect: it opens this workspace's web
      // connectors page in the system browser, where the admin connects
      // (the list here refetches on focus).
      const appUrl = daemonAppUrl.trim().replace(/\/+$/, "");
      if (!appUrl) {
        setMessage({ tone: "alert", text: t(($) => $.internal_mcp.catalog.connect_on_web) });
        return;
      }
      openExternal(`${appUrl}${connectorsPath}`);
      setMessage({ tone: "status", text: t(($) => $.internal_mcp.catalog.continue_in_browser) });
      return;
    }
    try {
      const url = await startOAuth.mutateAsync({ connectorId: connector.id });
      if (!url) {
        setMessage({ tone: "alert", text: t(($) => $.internal_mcp.catalog.connect_failed, { name: app.name }) });
        return;
      }
      setRedirecting(true);
      window.location.assign(url);
    } catch (error) {
      setMessage({ tone: "alert", text: errorMessage(error, t(($) => $.internal_mcp.catalog.connect_failed, { name: app.name })) });
    }
  }

  async function refreshTools() {
    if (!connector) return;
    setMessage(null);
    try {
      const result: InternalConnectorToolsRefresh | null = await refresh.mutateAsync(connector.id);
      setMessage(
        result
          ? {
              tone: "status",
              text: t(($) => $.internal_mcp.catalog.refresh_result, {
                discovered: result.discovered,
                allowed: result.allowedTools.length,
              }),
            }
          : null,
      );
    } catch (error) {
      setMessage({
        tone: "alert",
        text:
          errorCode(error) === "no_connected_account"
            ? t(($) => $.internal_mcp.catalog.refresh_no_account)
            : errorMessage(error, t(($) => $.internal_mcp.catalog.refresh_failed)),
      });
    }
  }

  async function toggleWrite(next: boolean) {
    if (!connector) return;
    setMessage(null);
    try {
      await setWrite.mutateAsync({ connector, writeEnabled: next });
    } catch (error) {
      setMessage({ tone: "alert", text: errorMessage(error, t(($) => $.internal_mcp.catalog.write_failed)) });
    }
  }

  const connecting = startOAuth.isPending || redirecting;
  const writeSwitchId = `connector-write-${app.slug}`;

  return (
    <article className="flex flex-col gap-4 rounded-xl border border-border bg-card p-5" aria-label={app.name}>
      <div className="flex items-start gap-3">
        <ConnectorLogo slug={app.slug} size="md" />
        <div className="min-w-0 flex-1 space-y-1">
          <div className="flex flex-wrap items-center gap-2">
            <h3 className="truncate text-title-sm font-semibold">{app.name}</h3>
            {connector && !connector.enabled && (
              <Badge variant="secondary">{t(($) => $.internal_mcp.catalog.disabled)}</Badge>
            )}
          </div>
          <p className="text-caption text-muted-foreground">{description}</p>
          <p className="text-caption text-foreground">{status}</p>
        </div>
      </div>

      {!connector ? (
        <div className="flex flex-wrap gap-2">
          <Button onClick={() => void addApp()} disabled={add.isPending}>
            {add.isPending ? <Loader2 className="mr-2 size-4 animate-spin motion-reduce:animate-none" /> : <Plus className="mr-2 size-4" />}
            {add.isPending ? t(($) => $.internal_mcp.catalog.adding) : t(($) => $.internal_mcp.catalog.add)}
          </Button>
        </div>
      ) : (
        <>
          <div className="flex flex-wrap gap-2">
            {app.oauthAvailable && (
              <Button variant={hasSharedAccount ? "outline" : "default"} disabled={connecting} onClick={() => void connectShared()}>
                {connecting && <Loader2 className="mr-2 size-4 animate-spin motion-reduce:animate-none" />}
                {connecting
                  ? t(($) => $.internal_mcp.catalog.connecting)
                  : hasSharedAccount
                    ? t(($) => $.internal_mcp.catalog.reconnect_shared)
                    : t(($) => $.internal_mcp.catalog.connect_shared)}
              </Button>
            )}
            {app.allowsPat && !patOpen && (
              <Button variant="outline" onClick={() => setPatOpen(true)}>
                {t(($) => $.internal_mcp.catalog.use_pat)}
              </Button>
            )}
            {/* The server lists tools with the shared account or, without
                one, with any person's or group's connected account, so the
                action stays available; it reports when nobody is connected. */}
            <Button variant="outline" disabled={refresh.isPending} onClick={() => void refreshTools()}>
              {refresh.isPending && <Loader2 className="mr-2 size-4 animate-spin motion-reduce:animate-none" />}
              {refresh.isPending ? t(($) => $.internal_mcp.catalog.refreshing) : t(($) => $.internal_mcp.catalog.refresh_tools)}
            </Button>
            <Button variant="outline" onClick={() => onManage(connector.id)}>
              {t(($) => $.internal_mcp.manage)}
            </Button>
          </div>

          {!app.oauthAvailable && !app.allowsPat && (
            <p className="text-caption text-muted-foreground">
              {t(($) => $.internal_mcp.catalog.oauth_unconfigured, { name: app.name })}
            </p>
          )}

          {/* The mobile configure page lets people and groups connect their
              own accounts only for apps an agent offers; an app that is only
              granted to agents works through the shared account. */}
          {!hasSharedAccount && (
            <p className="text-caption text-muted-foreground">
              {t(($) => $.internal_mcp.catalog.personal_connect_hint)}
            </p>
          )}

          {patOpen && (
            <SharedTokenForm
              workspaceId={workspaceId}
              connectorId={connector.id}
              appName={app.name}
              onDone={(text) => {
                setPatOpen(false);
                setMessage({ tone: "status", text });
              }}
              onCancel={() => setPatOpen(false)}
            />
          )}

          <div className="flex items-start justify-between gap-4">
            <div className="min-w-0 flex-1 space-y-0.5">
              <label htmlFor={writeSwitchId} className="block text-label font-medium">
                {t(($) => $.internal_mcp.catalog.write_label)}
              </label>
              <p id={`${writeSwitchId}-hint`} className="text-caption text-muted-foreground">
                {t(($) => $.internal_mcp.catalog.write_hint)}
              </p>
            </div>
            <span className="flex h-6 w-10 shrink-0 items-center justify-end">
              {setWrite.isPending ? (
                <Loader2 className="size-4 animate-spin text-muted-foreground motion-reduce:animate-none" />
              ) : (
                <Switch
                  id={writeSwitchId}
                  checked={connector.writeEnabled}
                  onCheckedChange={(next) => void toggleWrite(next)}
                  aria-describedby={`${writeSwitchId}-hint`}
                />
              )}
            </span>
          </div>
        </>
      )}

      {app.slug === "github" && (
        <p className="text-caption text-muted-foreground">
          {t(($) => $.internal_mcp.catalog.install_hint)}
          {app.installUrl && (
            <>
              {" "}
              <a
                href={app.installUrl}
                target="_blank"
                rel="noopener noreferrer"
                className="inline-flex items-center gap-1 font-medium text-foreground underline-offset-4 hover:underline"
                onClick={(event) => {
                  if (!isDesktopShell()) return;
                  event.preventDefault();
                  openExternal(app.installUrl);
                }}
              >
                {t(($) => $.internal_mcp.catalog.install_link)}
                <ExternalLink className="size-3" />
              </a>
            </>
          )}
        </p>
      )}

      {message && (
        <p role={message.tone} className={message.tone === "alert" ? "text-caption text-destructive" : "text-caption text-muted-foreground"}>
          {message.text}
        </p>
      )}
    </article>
  );
}

function SharedTokenForm({
  workspaceId,
  connectorId,
  appName,
  onDone,
  onCancel,
}: {
  workspaceId: string;
  connectorId: string;
  appName: string;
  onDone: (message: string) => void;
  onCancel: () => void;
}) {
  const { t } = useT("agents");
  const save = useSetInternalConnectorCredential(workspaceId);
  const [token, setToken] = useState("");
  const [error, setError] = useState("");
  const inputId = `connector-pat-${connectorId}`;

  async function submit() {
    const value = token.trim();
    if (!value || value.length > MAX_TOKEN_LENGTH || /[\r\n\0]/.test(value)) {
      setError(t(($) => $.internal_mcp.catalog.pat_invalid));
      return;
    }
    setError("");
    try {
      await save.mutateAsync({ connectorId, bearer: value });
      setToken("");
      onDone(t(($) => $.internal_mcp.catalog.pat_saved));
    } catch (e) {
      setError(errorMessage(e, t(($) => $.internal_mcp.catalog.pat_failed)));
    } finally {
      // Drop the submitted secret from the mutation state right away.
      save.reset();
    }
  }

  return (
    <div className="space-y-2 rounded-lg border border-border p-4">
      <label htmlFor={inputId} className="block text-label font-medium">
        {t(($) => $.internal_mcp.catalog.pat_label, { name: appName })}
      </label>
      <Input
        id={inputId}
        type="password"
        autoComplete="off"
        autoCapitalize="off"
        spellCheck={false}
        maxLength={MAX_TOKEN_LENGTH}
        value={token}
        aria-invalid={error ? true : undefined}
        placeholder={t(($) => $.internal_mcp.catalog.pat_placeholder)}
        onChange={(event) => {
          setToken(event.target.value);
          setError("");
        }}
      />
      <p className={error ? "text-caption text-destructive" : "text-caption text-muted-foreground"} role={error ? "alert" : undefined}>
        {error || t(($) => $.internal_mcp.catalog.pat_hint)}
      </p>
      <div className="flex flex-wrap justify-end gap-2">
        <Button variant="ghost" onClick={onCancel} disabled={save.isPending}>
          {t(($) => $.internal_mcp.catalog.cancel)}
        </Button>
        <Button onClick={() => void submit()} disabled={save.isPending || !token.trim()}>
          {save.isPending && <Loader2 className="mr-2 size-4 animate-spin motion-reduce:animate-none" />}
          {t(($) => $.internal_mcp.catalog.save)}
        </Button>
      </div>
    </div>
  );
}
