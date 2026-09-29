"use client";

import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api, type InternalConnectorInput, type InternalConnectorTest } from "@multica/core/api";
import { useAuthStore } from "@multica/core/auth";
import { useWorkspaceId } from "@multica/core/hooks";
import { agentListOptions, memberListOptions } from "@multica/core/workspace/queries";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Plus, Power } from "lucide-react";
import { useT } from "../i18n";

const empty: InternalConnectorInput = {name:"",upstream_url:"",allowed_tools:[],agent_ids:[],enabled:false,auth_mode:"none"};

export function InternalConnectorsPage() {
  const {t}=useT("agents");
  const workspaceId=useWorkspaceId();
  const queryClient=useQueryClient();
  const userId=useAuthStore((s)=>s.user?.id);
  const members=useQuery(memberListOptions(workspaceId));
  const agents=useQuery(agentListOptions(workspaceId));
  const canManage=members.data?.some((member)=>member.user_id===userId && (member.role==="owner" || member.role==="admin"))===true;
  const key=["workspaces",workspaceId,"internal-connectors"];
  const list=useQuery({queryKey:key,queryFn:()=>api.listInternalConnectors(workspaceId),enabled:!!workspaceId&&canManage,refetchOnWindowFocus:true});
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
  const capabilityLink=form.upstream_url.includes("/api/mcp/connect/");

  function edit(connectorId?:string) {
    const c=list.data?.find((item)=>item.id===connectorId);
    setEditing(c?.id??"new");
    setForm(c?{name:c.name,upstream_url:c.upstreamUrl,allowed_tools:c.allowedTools,agent_ids:c.agentIds,enabled:c.enabled,auth_mode:c.authMode}:empty);
    setError("");
    setBearerInput("");setRotateCredential(false);
    setCredentialToken("");setCredentialError("");
  }
  async function save() {
    if (!canManage || !editing) return;
    setBusy(true);setError("");
    const data:InternalConnectorInput=editing==="new"
      ? {...form,auth_mode:capabilityLink?"":form.auth_mode,bearer_token:capabilityLink?"":bearerInput,allowed_tools:[],auto_discover:true,enabled:false}
      : {...form,auto_discover:false};
    try {
      if(editing==="new") await api.createInternalConnector(workspaceId,data);
      else await api.updateInternalConnector(workspaceId,editing,data);
      await Promise.all([queryClient.invalidateQueries({queryKey:key}),queryClient.invalidateQueries({queryKey:[...key,"available"]})]);
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
  async function toggleConnector(connector:NonNullable<typeof list.data>[number]) {
    setToggling(connector.id);setToggleErrors((old)=>({...old,[connector.id]:""}));
    try {
      await api.updateInternalConnector(workspaceId,connector.id,{name:connector.name,upstream_url:connector.upstreamUrl,allowed_tools:connector.allowedTools,agent_ids:connector.agentIds,auth_mode:connector.authMode,enabled:!connector.enabled});
      await Promise.all([queryClient.invalidateQueries({queryKey:key}),queryClient.invalidateQueries({queryKey:[...key,"available"]})]);
    } catch(e) {setToggleErrors((old)=>({...old,[connector.id]:e instanceof Error?e.message:t(($)=>$.internal_mcp.save_failed)}));}
    finally {setToggling(null)}
  }

  const present=list.data??[];

  return <main className="mx-auto min-h-0 w-full max-w-6xl flex-1 space-y-7 overflow-y-auto px-6 py-8">
    <header className="flex flex-wrap items-end justify-between gap-4 border-b border-border pb-5">
      <div><p className="text-caption text-muted-foreground">{t(($)=>$.internal_mcp.kicker)}</p>
        <h1 className="mt-2 text-display-sm font-semibold tracking-tight">{t(($)=>$.internal_mcp.title)}</h1>
        <p className="mt-2 max-w-2xl text-body text-muted-foreground">{t(($)=>$.internal_mcp.description)}</p></div>
      {canManage && <Button onClick={()=>edit()}><Plus className="mr-2 size-4"/>{t(($)=>$.internal_mcp.create)}</Button>}
    </header>

    {!members.isLoading && !canManage && <p className="rounded-xl border border-border bg-muted/40 p-5 text-body text-muted-foreground">{t(($)=>$.internal_mcp.admin_only)}</p>}

    {canManage && <section className="space-y-3">
      <h2 className="text-title font-semibold">{t(($)=>$.internal_mcp.managed)}</h2>
      {list.isError && <p role="alert" className="text-body text-destructive">{t(($)=>$.internal_mcp.load_failed)}</p>}
      {list.isLoading && <p className="text-body text-muted-foreground">{t(($)=>$.internal_mcp.loading)}</p>}
      {!list.isLoading && !list.isError && present.length===0 && <div className="rounded-xl border border-dashed border-border p-6"><p className="text-body text-muted-foreground">{t(($)=>$.internal_mcp.empty)}</p><Button className="mt-4" variant="outline" onClick={()=>edit()}><Plus className="mr-2 size-4"/>{t(($)=>$.internal_mcp.create)}</Button></div>}
      <div className="grid gap-3 md:grid-cols-2">{present.map((c)=><article key={c.id} className="space-y-3 rounded-xl border border-border bg-card p-5">
        <div className="flex flex-wrap items-start justify-between gap-3"><div className="min-w-0 flex-1"><h3 className="text-title-sm font-semibold">{c.name}</h3><p className="mt-1 break-all text-caption text-muted-foreground">{c.upstreamUrl}</p></div><Badge variant={c.enabled&&c.credentialReady?"default":"secondary"}>{!c.credentialReady?t(($)=>$.internal_mcp.waiting_credential):c.enabled?t(($)=>$.internal_mcp.enabled):t(($)=>$.internal_mcp.disabled)}</Badge></div>
        <p className="text-caption text-muted-foreground">{t(($)=>$.internal_mcp.agent_count,{count:c.agentIds.length})} · {t(($)=>$.internal_mcp.discovered_tools,{count:c.allowedTools.length})} · {c.authMode==="none"?t(($)=>$.internal_mcp.no_auth):c.credentialSource==="workspace"?t(($)=>$.internal_mcp.credential_workspace):c.credentialSource==="environment"?t(($)=>$.internal_mcp.credential_environment):t(($)=>$.internal_mcp.waiting_credential)}</p>
        <div className="flex flex-wrap gap-2"><Button variant="outline" onClick={()=>edit(c.id)}>{t(($)=>$.internal_mcp.manage)}</Button><Button variant="outline" disabled={testing===c.id} onClick={()=>testConnection(c.id)}>{testing===c.id?t(($)=>$.internal_mcp.testing):t(($)=>$.internal_mcp.test_connection)}</Button><Button variant={c.enabled?"outline":"default"} disabled={toggling===c.id||(!c.enabled&&!c.credentialReady)} onClick={()=>toggleConnector(c)}><Power className="mr-2 size-4"/>{toggling===c.id?t(($)=>$.internal_mcp.saving):c.enabled?t(($)=>$.internal_mcp.disable_action):t(($)=>$.internal_mcp.enable)}</Button></div>
        {toggleErrors[c.id] && <p role="alert" className="text-caption text-destructive">{toggleErrors[c.id]}</p>}
        {testResults[c.id] && <p role="status" className={`text-caption ${testResults[c.id]?.ready?"text-foreground":testResults[c.id]?.reachable?"text-warning":"text-destructive"}`}>{testResults[c.id]?.ready?t(($)=>$.internal_mcp.test_success,{tools:testResults[c.id]?.tools?.join(", ")||"—"}):testResults[c.id]?.reachable?t(($)=>$.internal_mcp.test_missing_tools,{tools:testResults[c.id]?.missing_tools.join(", ")||"—"}):testResults[c.id]?.message||t(($)=>$.internal_mcp.test_failed)}</p>}
      </article>)}</div>
    </section>}

    {editing && <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4"><div className="max-h-[90vh] w-full max-w-xl space-y-4 overflow-y-auto rounded-2xl border border-border bg-background p-6 shadow-xl">
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
        <div className="grid grid-cols-2 gap-2"><Button variant={!form.enabled?"default":"outline"} onClick={()=>setForm({...form,enabled:false})}>{t(($)=>$.internal_mcp.keep_disabled)}</Button><Button variant={form.enabled?"default":"outline"} disabled={!credentialReady&&!form.enabled} onClick={()=>setForm({...form,enabled:true})}>{t(($)=>$.internal_mcp.enable)}</Button></div>
        {!credentialReady && <p className="text-caption text-muted-foreground">{t(($)=>$.internal_mcp.enable_hint)}</p>}
      </div>}
      {editing!=="new" && selectedConnector?.authMode==="none" && <p className="rounded-lg border border-border bg-muted/40 p-3 text-caption text-muted-foreground">{t(($)=>$.internal_mcp.no_auth_hint)}</p>}
      {editing!=="new" && selectedConnector?.authMode==="bearer" && <div className="space-y-2">
        {credentialReady && !rotateCredential && <Button variant="outline" onClick={()=>setRotateCredential(true)}>{t(($)=>$.internal_mcp.rotate_credential)}</Button>}
        {(!credentialReady || rotateCredential) && <div className="space-y-2 rounded-lg border border-border p-4"><label className="block text-label font-medium">{t(($)=>$.internal_mcp.credential)}<Input className="mt-1" type="password" autoComplete="off" value={credentialToken} onChange={(e)=>setCredentialToken(e.target.value)}/></label><p className="text-caption text-muted-foreground">{t(($)=>$.internal_mcp.credential_hint)}</p><Button variant="outline" disabled={credentialBusy||!credentialToken} onClick={saveCredential}>{credentialBusy?t(($)=>$.internal_mcp.saving):t(($)=>$.internal_mcp.save_credential)}</Button>{credentialError && <p role="alert" className="text-caption text-destructive">{credentialError}</p>}</div>}
      </div>}
      {error && <p role="alert" className="text-body text-destructive">{error}</p>}
      <div className="flex justify-end gap-2"><Button variant="outline" onClick={()=>{setEditing(null);setForm(empty);setBearerInput("");setCredentialToken("")}}>{t(($)=>$.internal_mcp.cancel)}</Button><Button disabled={busy} onClick={save}>{busy?t(($)=>$.internal_mcp.saving):selectedConnector&&form.enabled!==selectedConnector.enabled?form.enabled?t(($)=>$.internal_mcp.save_enable):t(($)=>$.internal_mcp.save_disable):t(($)=>$.internal_mcp.save)}</Button></div>
    </div></div>}
  </main>;
}
