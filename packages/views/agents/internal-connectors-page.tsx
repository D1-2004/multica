"use client";

import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api, type InternalConnectorInput, type InternalConnectorTest } from "@multica/core/api";
import { useAuthStore } from "@multica/core/auth";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { agentListOptions, memberListOptions } from "@multica/core/workspace/queries";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { ArrowUpRight, Copy, Network, Plus } from "lucide-react";
import { useT } from "../i18n";
import { useNavigation } from "../navigation";

const empty: InternalConnectorInput = {name:"",upstream_url:"",allowed_tools:[],agent_ids:[],enabled:false};

export function InternalConnectorsPage() {
  const {t}=useT("agents");
  const workspaceId=useWorkspaceId();
  const paths=useWorkspacePaths();
  const navigation=useNavigation();
  const queryClient=useQueryClient();
  const userId=useAuthStore((s)=>s.user?.id);
  const members=useQuery(memberListOptions(workspaceId));
  const agents=useQuery(agentListOptions(workspaceId));
  const canManage=members.data?.some((member)=>member.user_id===userId && (member.role==="owner" || member.role==="admin"))===true;
  const key=["workspaces",workspaceId,"internal-connectors"];
  const list=useQuery({queryKey:key,queryFn:()=>api.listInternalConnectors(workspaceId),enabled:!!workspaceId&&canManage,refetchOnWindowFocus:true});
  const available=useQuery({queryKey:[...key,"available"],queryFn:()=>api.listAvailableInternalConnectors(workspaceId),enabled:!!workspaceId,refetchOnWindowFocus:true});
  const legacy=useQuery({queryKey:[...key,"legacy"],queryFn:()=>api.getSemanticaMCPStatus(workspaceId),enabled:!!workspaceId,refetchOnWindowFocus:true});
  const [editing,setEditing]=useState<string|null>(null);
  const [form,setForm]=useState<InternalConnectorInput>(empty);
  const [toolLines,setToolLines]=useState("");
  const [error,setError]=useState("");
  const [busy,setBusy]=useState(false);
  const [question,setQuestion]=useState("");
  const [copyError,setCopyError]=useState(false);
  const [credentialToken,setCredentialToken]=useState("");
  const [credentialBusy,setCredentialBusy]=useState(false);
  const [credentialError,setCredentialError]=useState("");
  const [testing,setTesting]=useState<string|null>(null);
  const [testResults,setTestResults]=useState<Record<string,InternalConnectorTest>>({});
  const credentialReady=editing!=="new" && !!list.data?.find((c)=>c.id===editing)?.credentialReady;

  function edit(connectorId?:string) {
    const c=list.data?.find((item)=>item.id===connectorId);
    setEditing(c?.id??"new");
    setForm(c?{name:c.name,upstream_url:c.upstreamUrl,allowed_tools:c.allowedTools,agent_ids:c.agentIds,enabled:c.enabled}:empty);
    setToolLines(c?.allowedTools.join("\n")??"");
    setError("");
    setCredentialToken("");setCredentialError("");
  }
  async function save() {
    if (!canManage || !editing) return;
    setBusy(true);setError("");
    const data={...form,allowed_tools:toolLines.split(/[,\n]/).map((name)=>name.trim()).filter(Boolean)};
    try {
      let createdId:string|undefined;
      if(editing==="new") createdId=(await api.createInternalConnector(workspaceId,data)).id;
      else await api.updateInternalConnector(workspaceId,editing,data);
      await Promise.all([queryClient.invalidateQueries({queryKey:key}),queryClient.invalidateQueries({queryKey:[...key,"available"]})]);
      setEditing(createdId??null);
      if(!createdId) setCredentialToken("");
    } catch(e) {setError(e instanceof Error?e.message:t(($)=>$.internal_mcp.save_failed));}
    finally {setBusy(false)}
  }
  async function saveCredential() {
    if(!editing || editing==="new" || !credentialToken) return;
    setCredentialBusy(true);setCredentialError("");
    try {
      await api.setInternalConnectorCredential(workspaceId,editing,credentialToken);
      setCredentialToken("");
      await queryClient.invalidateQueries({queryKey:key});
    } catch(e) {setCredentialError(e instanceof Error?e.message:t(($)=>$.internal_mcp.credential_failed));}
    finally {setCredentialBusy(false)}
  }
  async function testConnection(id:string) {
    setTesting(id);
    try {const result=await api.testInternalConnector(workspaceId,id);setTestResults((old)=>({...old,[id]:result}));}
    catch {setTestResults((old)=>({...old,[id]:{reachable:false,message:t(($)=>$.internal_mcp.test_failed)}}));}
    finally {setTesting(null)}
  }
  async function copyAndOpen(agentId:string,prompt:string) {
    try {await navigator.clipboard.writeText(prompt);setCopyError(false);navigation.push(paths.chatWithAgent(agentId));}
    catch {setCopyError(true);setQuestion(prompt)}
  }
  function promptFor(name:string,id:string) {return t(($)=>$.internal_mcp.example,{name,server:`internal-${id}`});}

  const present=list.data??[];
  const visible=available.data??[];
  const hasGenericSemantica=visible.some((c)=>c.name.toLowerCase()==="semantica");
  const showLegacy=legacy.data?.available===true && !!legacy.data.agentId && !hasGenericSemantica;

  return <main className="mx-auto min-h-0 w-full max-w-6xl flex-1 space-y-7 overflow-y-auto px-6 py-8">
    <header className="flex flex-wrap items-end justify-between gap-4 border-b border-border pb-5">
      <div><p className="text-caption text-muted-foreground">{t(($)=>$.internal_mcp.kicker)}</p>
        <h1 className="mt-2 text-display-sm font-semibold tracking-tight">{t(($)=>$.internal_mcp.title)}</h1>
        <p className="mt-2 max-w-2xl text-body text-muted-foreground">{t(($)=>$.internal_mcp.description)}</p></div>
      {canManage && <Button onClick={()=>edit()}><Plus className="mr-2 size-4"/>{t(($)=>$.internal_mcp.create)}</Button>}
    </header>

    <section className="grid gap-3 md:grid-cols-3">
      {[t(($)=>$.internal_mcp.step_define),t(($)=>$.internal_mcp.step_credential),t(($)=>$.internal_mcp.step_use)].map((label,i)=><div key={label} className="rounded-xl border border-border bg-card p-4"><span className="text-caption font-mono text-muted-foreground">{String(i+1).padStart(2,"0")}</span><p className="mt-2 text-body font-medium">{label}</p></div>)}
    </section>

    {canManage && <section className="space-y-3">
      <h2 className="text-title font-semibold">{t(($)=>$.internal_mcp.managed)}</h2>
      {list.isError && <p role="alert" className="text-body text-destructive">{t(($)=>$.internal_mcp.load_failed)}</p>}
      {list.isLoading && <p className="text-body text-muted-foreground">{t(($)=>$.internal_mcp.loading)}</p>}
      {!list.isLoading && !list.isError && present.length===0 && <p className="rounded-xl border border-dashed border-border p-6 text-body text-muted-foreground">{t(($)=>$.internal_mcp.empty)}</p>}
      <div className="grid gap-3 md:grid-cols-2">{present.map((c)=><article key={c.id} className="space-y-3 rounded-xl border border-border bg-card p-5">
        <div className="flex items-start justify-between gap-3"><div><h3 className="text-title-sm font-semibold">{c.name}</h3><p className="mt-1 break-all text-caption text-muted-foreground">{c.upstreamUrl}</p></div><Badge variant={c.enabled&&c.credentialReady?"default":"secondary"}>{!c.credentialReady?t(($)=>$.internal_mcp.waiting_credential):c.enabled?t(($)=>$.internal_mcp.enabled):t(($)=>$.internal_mcp.disabled)}</Badge></div>
        <div className="flex flex-wrap gap-1">{c.allowedTools.map((name)=><code key={name} className="rounded bg-muted px-2 py-1 text-caption">{name}</code>)}</div>
        <p className="text-caption text-muted-foreground">{t(($)=>$.internal_mcp.agent_count,{count:c.agentIds.length})}</p>
        <p className="break-all text-caption text-muted-foreground">{t(($)=>$.internal_mcp.credential_ref)}: <code>{c.credentialRef}</code></p>
        <p className="text-caption text-muted-foreground">{t(($)=>$.internal_mcp.credential_source)}: {c.credentialSource==="workspace"?t(($)=>$.internal_mcp.credential_workspace):c.credentialSource==="environment"?t(($)=>$.internal_mcp.credential_environment):t(($)=>$.internal_mcp.waiting_credential)}</p>
        <div className="flex flex-wrap gap-2"><Button variant="outline" onClick={()=>edit(c.id)}>{t(($)=>$.internal_mcp.manage)}</Button><Button variant="outline" disabled={testing===c.id} onClick={()=>testConnection(c.id)}>{testing===c.id?t(($)=>$.internal_mcp.testing):t(($)=>$.internal_mcp.test_connection)}</Button></div>
        {testResults[c.id] && <p role="status" className={`text-caption ${testResults[c.id]?.reachable?"text-foreground":"text-destructive"}`}>{testResults[c.id]?.reachable?t(($)=>$.internal_mcp.test_success,{tools:testResults[c.id]?.tools?.join(", ")||"—"}):testResults[c.id]?.message||t(($)=>$.internal_mcp.test_failed)}</p>}
      </article>)}</div>
    </section>}

    <section className="space-y-3">
      <h2 className="flex items-center gap-2 text-title font-semibold"><Network className="size-5"/>{t(($)=>$.internal_mcp.available)}</h2>
      {available.isLoading && <p className="text-body text-muted-foreground">{t(($)=>$.internal_mcp.loading)}</p>}
      {available.isError && <p role="alert" className="text-body text-destructive">{t(($)=>$.internal_mcp.load_failed)}</p>}
      {!available.isLoading && !available.isError && visible.length===0 && !showLegacy && <p className="text-body text-muted-foreground">{t(($)=>$.internal_mcp.none_available)}</p>}
      <div className="grid gap-3 md:grid-cols-2">
        {visible.map((c)=><article key={`${c.id}-${c.agentId}`} className="space-y-3 rounded-xl border border-border bg-card p-5">
          <h3 className="text-title-sm font-semibold">{c.name}</h3><p className="text-body text-muted-foreground">{t(($)=>$.internal_mcp.with_agent,{name:c.agentName})}</p>
          <div className="flex flex-wrap gap-1">{c.tools.map((name)=><code key={name} className="rounded bg-muted px-2 py-1 text-caption">{name}</code>)}</div>
          <div className="flex flex-wrap gap-2"><Button onClick={()=>copyAndOpen(c.agentId,promptFor(c.name,c.id))}><Copy className="mr-2 size-4"/>{t(($)=>$.internal_mcp.open_chat)}</Button><Button variant="outline" onClick={()=>navigation.push(paths.chatWithAgent(c.agentId))}>{t(($)=>$.semantica.open_chat)}</Button></div>
        </article>)}
        {showLegacy && <article className="space-y-3 rounded-xl border border-border border-l-4 border-l-primary bg-card p-5">
          <div className="flex items-center justify-between gap-2"><h3 className="text-title-sm font-semibold">{t(($)=>$.internal_mcp.legacy_name)}</h3><Badge variant="secondary">{t(($)=>$.internal_mcp.legacy)}</Badge></div>
          <p className="text-body text-muted-foreground">{t(($)=>$.internal_mcp.with_agent,{name:legacy.data?.agentName??t(($)=>$.semantica.agent_fallback)})}</p>
          <div className="flex flex-wrap gap-2"><Button onClick={()=>copyAndOpen(legacy.data!.agentId!,t(($)=>$.semantica.example))}><ArrowUpRight className="mr-2 size-4"/>{t(($)=>$.internal_mcp.open_chat)}</Button><Button variant="outline" onClick={()=>navigation.push(paths.chatWithAgent(legacy.data!.agentId!))}>{t(($)=>$.semantica.open_chat)}</Button></div>
        </article>}
      </div>
      {copyError && <p role="alert" className="text-caption text-destructive">{t(($)=>$.internal_mcp.copy_failed)} <span>{question}</span></p>}
      <p className="text-caption text-muted-foreground">{t(($)=>$.internal_mcp.usage_note)}</p>
    </section>

    {editing && <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4"><div className="max-h-[90vh] w-full max-w-xl space-y-4 overflow-y-auto rounded-2xl border border-border bg-background p-6 shadow-xl">
      <h2 className="text-title font-semibold">{editing==="new"?t(($)=>$.internal_mcp.create):t(($)=>$.internal_mcp.manage)}</h2>
      <p className="text-body text-muted-foreground">{t(($)=>$.internal_mcp.form_hint)}</p>
      <label className="block text-label font-medium">{t(($)=>$.internal_mcp.name)}<Input className="mt-1" value={form.name} onChange={(e)=>setForm({...form,name:e.target.value})}/></label>
      <label className="block text-label font-medium">{t(($)=>$.internal_mcp.upstream)}<Input className="mt-1" placeholder="https://approved.example/mcp" value={form.upstream_url} disabled={editing!=="new"} onChange={(e)=>setForm({...form,upstream_url:e.target.value})}/></label>
      <label className="block text-label font-medium">{t(($)=>$.internal_mcp.tools)}<textarea className="mt-1 min-h-28 w-full rounded-md border border-input bg-background p-3 font-mono text-body" value={toolLines} onChange={(e)=>setToolLines(e.target.value)}/></label>
      <fieldset><legend className="text-label font-medium">{t(($)=>$.internal_mcp.agents)}</legend><div className="mt-2 max-h-40 space-y-2 overflow-y-auto rounded-lg border border-border p-3">{agents.data?.map((agent)=><label key={agent.id} className="flex items-center gap-2 text-body"><input type="checkbox" checked={form.agent_ids.includes(agent.id)} onChange={(e)=>setForm({...form,agent_ids:e.target.checked?[...form.agent_ids,agent.id]:form.agent_ids.filter((id)=>id!==agent.id)})}/>{agent.name}</label>)}</div></fieldset>
      <label className="flex items-center gap-2 text-body"><input type="checkbox" disabled={!credentialReady&&!form.enabled} checked={form.enabled} onChange={(e)=>setForm({...form,enabled:e.target.checked})}/>{t(($)=>$.internal_mcp.enable)}</label>
      {!credentialReady && <p className="text-caption text-muted-foreground">{t(($)=>$.internal_mcp.enable_hint)}</p>}
      {editing!=="new" && <div className="space-y-2 rounded-lg border border-border p-4"><label className="block text-label font-medium">{t(($)=>$.internal_mcp.credential)}<Input className="mt-1" type="password" autoComplete="off" value={credentialToken} onChange={(e)=>setCredentialToken(e.target.value)}/></label><p className="text-caption text-muted-foreground">{t(($)=>$.internal_mcp.credential_hint)}</p><Button variant="outline" disabled={credentialBusy||!credentialToken} onClick={saveCredential}>{credentialBusy?t(($)=>$.internal_mcp.saving):t(($)=>$.internal_mcp.save_credential)}</Button>{credentialError && <p role="alert" className="text-caption text-destructive">{credentialError}</p>}</div>}
      {error && <p role="alert" className="text-body text-destructive">{error}</p>}
      <div className="flex justify-end gap-2"><Button variant="outline" onClick={()=>{setEditing(null);setCredentialToken("")}}>{t(($)=>$.internal_mcp.cancel)}</Button><Button disabled={busy} onClick={save}>{busy?t(($)=>$.internal_mcp.saving):t(($)=>$.internal_mcp.save)}</Button></div>
    </div></div>}
  </main>;
}
